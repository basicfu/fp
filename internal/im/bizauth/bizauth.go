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
	a := &Authenticator{
		apps:   cfg.Apps,
		client: cfg.Client,
		log:    cfg.Logger,
		now:    cfg.Now,
		cache:  newCacheSet(),
	}
	if a.client == nil {
		a.client = &http.Client{}
	}
	if a.log == nil {
		a.log = slog.Default()
	}
	if a.now == nil {
		a.now = time.Now
	}
	return a, nil
}

// verifyResponse 是业务方接口的响应约定。
// CacheSeconds 由业务方决定：只有它知道自己的令牌撤销有多频繁、
// 能容忍多长的失效窗口。不带就不缓存，默认安全。
type verifyResponse struct {
	UserID       string `json:"user_id"`
	CacheSeconds int    `json:"cache_seconds"`
}

func (a *Authenticator) Verify(ctx context.Context, req auth.VerifyRequest) (model.Subject, error) {
	cfg, ok := a.apps.Get(req.App)
	if !ok || cfg.BizAuth == nil {
		// app 不存在，或者存在但没开业务方认证。对 client 是同一件事：
		// 你这个令牌在这里验不了。不区分是为了不泄露"哪些 app 开了什么"。
		return model.Subject{}, auth.ErrUnauthorized
	}
	if sub, hit := a.cache.get(req.App, req.Token, a.now()); hit {
		return sub, nil
	}

	rctx, cancel := context.WithTimeout(ctx, cfg.BizAuth.Timeout.Std())
	defer cancel()
	httpReq, err := http.NewRequestWithContext(rctx, http.MethodPost, cfg.BizAuth.VerifyURL, bytes.NewReader(req.Raw))
	if err != nil {
		return model.Subject{}, fmt.Errorf("%w: 构造请求失败: %v", auth.ErrUnavailable, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		a.log.Warn("bizauth: 调业务方验证接口失败", "app", req.App, "err", err)
		return model.Subject{}, fmt.Errorf("%w: %v", auth.ErrUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		// 业务方明确说这个令牌不对，client 该去重新登录而不是重连。
		return model.Subject{}, auth.ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		a.log.Warn("bizauth: 业务方验证接口返回异常状态", "app", req.App, "status", resp.StatusCode)
		return model.Subject{}, fmt.Errorf("%w: 业务方返回 %d", auth.ErrUnavailable, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBody))
	if err != nil {
		return model.Subject{}, fmt.Errorf("%w: 读响应失败: %v", auth.ErrUnavailable, err)
	}
	var vr verifyResponse
	if err := json.Unmarshal(body, &vr); err != nil {
		// 响应体坏了是业务方的接口出问题，不是 client 的令牌有问题，
		// 所以归 Unavailable 让 client 退避重连，等业务方修好。
		a.log.Warn("bizauth: 业务方响应不是合法 JSON", "app", req.App, "err", err)
		return model.Subject{}, fmt.Errorf("%w: 响应不是合法 JSON", auth.ErrUnavailable)
	}
	if err := model.ValidateUserID(vr.UserID); err != nil {
		// user_id 会成为 Redis key 的一部分，非法值必须在这里拦住。
		a.log.Warn("bizauth: 业务方返回了非法的 user_id", "app", req.App, "err", err)
		return model.Subject{}, fmt.Errorf("%w: %v", auth.ErrUnauthorized, err)
	}

	sub := model.Biz(vr.UserID)
	// 只缓存成功的结果。失败不缓存：业务方接口恢复之后 client 应当立刻能连上，
	// 而不是等一个负缓存过期。
	a.cache.put(req.App, req.Token, sub, time.Duration(vr.CacheSeconds)*time.Second, cfg.BizAuth.CacheSize, a.now())
	return sub, nil
}
