// Package bizauth 用 HTTP 回调业务方自己的接口来验证他们签发的令牌。
// 与 fpauth 并列，两者都实现 auth.Authenticator，由 multiauth 按令牌类型分派。
package bizauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/model"
)

// maxRespBody 是响应体的读取上限。业务方接口异常时可能吐出一个巨大的
// HTML 错误页，不设上限的话每次握手都会把它整个读进内存。
const maxRespBody = 64 << 10

type Config struct {
	Apps   auth.AppConfigSource
	Client *http.Client     // nil 则用默认客户端；超时由每次请求的 ctx 控制
	Logger *slog.Logger     // nil 则用 slog.Default()
	Now    func() time.Time // nil 则用 time.Now，测试注入假时钟
}

type Authenticator struct {
	apps   auth.AppConfigSource
	client *http.Client
	log    *slog.Logger
	now    func() time.Time
	cache  *cacheSet
}

func New(cfg Config) (*Authenticator, error) {
	if cfg.Apps == nil {
		return nil, errors.New("bizauth: Apps 必填")
	}
	c := &http.Client{}
	if cfg.Client != nil {
		cp := *cfg.Client // 复制而不是就地改：Client 是调用方的，不该有副作用
		c = &cp
	}
	// 不跟随重定向：Go 默认会跟，且不阻止 https->http 降级；307/308 还会把
	// 带令牌的请求体原样重放过去，配置层"verify_url 必须是 https"这条强制
	// 就形同虚设。重定向的接口按"业务方接口配错了"处理，落到非 200 分支
	// 回 4004 让 client 退避重连，语义正确。
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	a := &Authenticator{
		apps:   cfg.Apps,
		client: c,
		log:    cfg.Logger,
		now:    cfg.Now,
		cache:  newCacheSet(),
	}
	if a.log == nil {
		a.log = slog.Default()
	}
	if a.now == nil {
		a.now = time.Now
	}
	return a, nil
}

// verifyResponse 是业务方接口的响应约定：一律 HTTP 200，body 是
// model.Result，成功时额外带 uid 与可选的 cache_seconds。
// CacheSeconds 由业务方决定：只有它知道自己的令牌撤销有多频繁、
// 能容忍多长的失效窗口。不带就不缓存，默认安全。
type verifyResponse struct {
	model.Result
	UID          string `json:"uid"`
	CacheSeconds int    `json:"cache_seconds"`
}

// HeaderClientIP 把 client 地址交给业务方。放请求头而不是改写请求体：
// 请求体要逐字节保持为 client 的原始握手帧。
const HeaderClientIP = "X-Fp-Client-Ip"

func (a *Authenticator) Verify(ctx context.Context, req auth.VerifyRequest) (model.Subject, error) {
	cfg, ok := a.apps.Get(req.App)
	if !ok || cfg.BizAuth == nil {
		// app 不存在，或者存在但没开业务方认证。对 client 是同一件事：
		// 你这个令牌在这里验不了。不区分是为了不泄露"哪些 app 开了什么"。
		return model.Subject{}, auth.ErrUnauthorized
	}
	if sub, hit := a.cache.get(req.App, req.Token, cfg.BizAuth.VerifyURL, a.now()); hit {
		return sub, nil
	}

	rctx, cancel := context.WithTimeout(ctx, cfg.BizAuth.Timeout.Std())
	defer cancel()
	httpReq, err := http.NewRequestWithContext(rctx, http.MethodPost, cfg.BizAuth.VerifyURL, bytes.NewReader(req.Raw))
	if err != nil {
		return model.Subject{}, fmt.Errorf("%w: 构造请求失败: %v", auth.ErrUnavailable, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if req.IP != "" {
		httpReq.Header.Set(HeaderClientIP, req.IP)
	}

	resp, err := a.client.Do(httpReq)
	if err != nil {
		a.log.Warn("bizauth: 调业务方验证接口失败", "app", req.App, "err", err)
		return model.Subject{}, fmt.Errorf("%w: %v", auth.ErrUnavailable, err)
	}
	defer resp.Body.Close()

	// 约定里拒绝也走 200 + code，所以任何非 200（含 401）都是业务方接口
	// 本身出了问题，而不是令牌不对。排空（带上限）而不是直接 Close：底层
	// 连接能回连接池复用，省下每次一次 TCP+TLS 握手。
	if resp.StatusCode != http.StatusOK {
		a.log.Warn("bizauth: 业务方验证接口返回异常状态", "app", req.App, "status", resp.StatusCode)
		drainBody(resp.Body)
		return model.Subject{}, fmt.Errorf("%w: 业务方返回 %d", auth.ErrUnavailable, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBody))
	if err != nil {
		return model.Subject{}, fmt.Errorf("%w: 读响应失败: %v", auth.ErrUnavailable, err)
	}
	var vr verifyResponse
	if err := json.Unmarshal(body, &vr); err != nil || vr.Code == "" {
		// 响应体不合约定是业务方的接口出问题，不是 client 的令牌有问题，
		// 所以归 Unavailable 让 client 退避重连，等业务方修好。
		a.log.Warn("bizauth: 业务方响应不合约定", "app", req.App, "err", err)
		return model.Subject{}, fmt.Errorf("%w: 响应缺 code 或不是合法 JSON", auth.ErrUnavailable)
	}
	if vr.Code != model.CodeSuccess {
		return model.Subject{}, &auth.RejectedError{Code: vr.Code, Result: body}
	}
	if err := model.ValidateUserID(vr.UID); err != nil {
		// uid 会成为 Redis key 的一部分，非法值必须在这里拦住。业务方说了
		// success 却给不出可用的 uid，是它的接口有 bug，按不可用处理。
		a.log.Warn("bizauth: 业务方返回了非法的 uid", "app", req.App, "err", err)
		return model.Subject{}, fmt.Errorf("%w: %v", auth.ErrUnavailable, err)
	}

	sub := model.Biz(vr.UID)
	// 只缓存成功的结果。失败不缓存：业务方接口恢复之后 client 应当立刻能连上，
	// 而不是等一个负缓存过期。
	a.cache.put(req.App, req.Token, cfg.BizAuth.VerifyURL, sub, time.Duration(vr.CacheSeconds)*time.Second, cfg.BizAuth.CacheSize, a.now())
	return sub, nil
}

// drainBody 排空响应体（带上限），让底层连接能回连接池复用，而不是靠
// Close 直接丢弃连接逼一次新的 TCP+TLS 握手。上限复用 maxRespBody：
// 业务方接口异常时可能吐出一个巨大的 body，排空不能没有边界。
func drainBody(r io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r, maxRespBody))
}
