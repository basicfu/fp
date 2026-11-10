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
	// SendFeatures 打开后回调带 HeaderFeatures，告诉业务方本节点认得分组。
	// 默认关：业务 server 的流可能落在还不认分组的旧节点上，那里的 Push/
	// Kick 会拒掉分组主体。全部节点都升级之后才能打开。
	SendFeatures bool
}

type Authenticator struct {
	apps         auth.AppConfigSource
	client       *http.Client
	log          *slog.Logger
	now          func() time.Time
	cache        *cacheSet
	sendFeatures bool
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

		sendFeatures: cfg.SendFeatures,
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

// groupResponse 是同一响应里的分组字段，只在请求带了特性头时才单独解析。
// 不并进 verifyResponse：业务方现有响应里可能本来就有自己语义的同名字段
// （比如对象形式的 group），并进去会让它类型不符时整个响应解析失败。
//
// 分组由回调决定，与 uid 同一个信任模型；策略跟着分组走，只对分组主体生效。
type groupResponse struct {
	Group      string       `json:"group"`
	ConnPolicy model.Policy `json:"conn_policy"`
	ConnLimit  int          `json:"conn_limit"`
}

// HeaderClientIP 把 client 地址交给业务方。放请求头而不是改写请求体：
// 请求体要逐字节保持为 client 的原始握手帧。
const HeaderClientIP = "X-Fp-Client-Ip"

// HeaderFeatures 声明本节点支持的扩展。业务方看到 FeatureGroup 才返回
// group，否则旧节点会收到它认不出的主体。
const (
	HeaderFeatures = "X-Fp-Im-Features"
	FeatureGroup   = "group"
)

// MaxGroupConnLimit 是分组 limit 策略的上限。连接表是每个 subject 一个
// hash，上限太大时一个 subject 的握手脚本要遍历的 field 也跟着涨。
const MaxGroupConnLimit = 100

func (a *Authenticator) Verify(ctx context.Context, req auth.VerifyRequest) (auth.Identity, error) {
	cfg, ok := a.apps.Get(req.App)
	if !ok || cfg.BizAuth == nil {
		// app 不存在，或者存在但没开业务方认证。对 client 是同一件事：
		// 你这个令牌在这里验不了。不区分是为了不泄露"哪些 app 开了什么"。
		return auth.Identity{}, auth.ErrUnauthorized
	}
	if id, hit := a.cache.get(req.App, req.Token, cfg.BizAuth.VerifyURL, a.now()); hit {
		return id, nil
	}

	rctx, cancel := context.WithTimeout(ctx, cfg.BizAuth.Timeout.Std())
	defer cancel()
	httpReq, err := http.NewRequestWithContext(rctx, http.MethodPost, cfg.BizAuth.VerifyURL, bytes.NewReader(req.Raw))
	if err != nil {
		return auth.Identity{}, fmt.Errorf("%w: 构造请求失败: %v", auth.ErrUnavailable, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if req.IP != "" {
		httpReq.Header.Set(HeaderClientIP, req.IP)
	}
	if a.sendFeatures {
		httpReq.Header.Set(HeaderFeatures, FeatureGroup)
	}

	resp, err := a.client.Do(httpReq)
	if err != nil {
		a.log.Warn("bizauth: 调业务方验证接口失败", "app", req.App, "err", err)
		return auth.Identity{}, fmt.Errorf("%w: %v", auth.ErrUnavailable, err)
	}
	defer resp.Body.Close()

	// 约定里拒绝也走 200 + code，所以任何非 200（含 401）都是业务方接口
	// 本身出了问题，而不是令牌不对。排空（带上限）而不是直接 Close：底层
	// 连接能回连接池复用，省下每次一次 TCP+TLS 握手。
	if resp.StatusCode != http.StatusOK {
		a.log.Warn("bizauth: 业务方验证接口返回异常状态", "app", req.App, "status", resp.StatusCode)
		drainBody(resp.Body)
		return auth.Identity{}, fmt.Errorf("%w: 业务方返回 %d", auth.ErrUnavailable, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBody))
	if err != nil {
		return auth.Identity{}, fmt.Errorf("%w: 读响应失败: %v", auth.ErrUnavailable, err)
	}
	var vr verifyResponse
	if err := json.Unmarshal(body, &vr); err != nil || vr.Code == "" {
		// 响应体不合约定是业务方的接口出问题，不是 client 的令牌有问题，
		// 所以归 Unavailable 让 client 退避重连，等业务方修好。
		a.log.Warn("bizauth: 业务方响应不合约定", "app", req.App, "err", err)
		return auth.Identity{}, fmt.Errorf("%w: 响应缺 code 或不是合法 JSON", auth.ErrUnavailable)
	}
	if vr.Code != model.CodeSuccess {
		return auth.Identity{}, &auth.RejectedError{Code: vr.Code, Result: body}
	}
	if err := model.ValidateUserID(vr.UID); err != nil {
		// uid 会成为 Redis key 的一部分，非法值必须在这里拦住。业务方说了
		// success 却给不出可用的 uid，是它的接口有 bug，按不可用处理。
		a.log.Warn("bizauth: 业务方返回了非法的 uid", "app", req.App, "err", err)
		return auth.Identity{}, fmt.Errorf("%w: %v", auth.ErrUnavailable, err)
	}

	id := auth.Identity{Subject: model.Biz(vr.UID)}
	// 没发特性头就当这三个字段不存在：两步上线的安全性不能只靠业务方自觉
	// 不返回 group——第一步时流可能落在不认分组的旧节点上。
	var gr groupResponse
	if a.sendFeatures {
		if err := json.Unmarshal(body, &gr); err != nil {
			// 业务方已经被告知支持分组，字段类型还不对就是它的 bug，与分组名非法同样处理
			a.log.Warn("bizauth: 业务方返回的分组字段类型不对", "app", req.App, "err", err)
			return auth.Identity{}, fmt.Errorf("%w: 分组字段: %v", auth.ErrUnavailable, err)
		}
	}
	if gr.Group != "" {
		// 分组名与策略非法同样是业务方接口的 bug，与 uid 非法一样按不可用处理
		if err := model.ValidateGroup(gr.Group); err != nil {
			a.log.Warn("bizauth: 业务方返回了非法的分组", "app", req.App, "err", err)
			return auth.Identity{}, fmt.Errorf("%w: %v", auth.ErrUnavailable, err)
		}
		policy, limit, err := groupPolicy(gr.ConnPolicy, gr.ConnLimit)
		if err != nil {
			a.log.Warn("bizauth: 业务方返回了非法的分组策略", "app", req.App, "group", gr.Group, "err", err)
			return auth.Identity{}, fmt.Errorf("%w: %v", auth.ErrUnavailable, err)
		}
		id = auth.Identity{Subject: model.BizGroup(gr.Group, id.Subject.ID), Policy: policy, Limit: limit}
	}
	// 默认分组不认回调给的策略：它只用应用级策略，现有行为逐字不变。
	//
	// 只缓存成功的结果。失败不缓存：业务方接口恢复之后 client 应当立刻能连上，
	// 而不是等一个负缓存过期。
	a.cache.put(req.App, req.Token, cfg.BizAuth.VerifyURL, id, time.Duration(vr.CacheSeconds)*time.Second, cfg.BizAuth.CacheSize, a.now())
	return id, nil
}

// groupPolicy 校验分组策略。空策略表示沿用应用级；PolicyNone 是重登记
// 专用的内部值，放进来等于让业务方跳过一切在线判定。
func groupPolicy(p model.Policy, limit int) (model.Policy, int, error) {
	switch p {
	case "":
		return "", 0, nil
	case model.PolicyReplace, model.PolicyReject:
		return p, 0, nil
	case model.PolicyLimit:
		if limit < 1 || limit > MaxGroupConnLimit {
			return "", 0, fmt.Errorf("conn_limit 必须在 1~%d，实际 %d", MaxGroupConnLimit, limit)
		}
		return p, limit, nil
	}
	return "", 0, fmt.Errorf("未知 conn_policy %q", p)
}

// drainBody 排空响应体（带上限），让底层连接能回连接池复用，而不是靠
// Close 直接丢弃连接逼一次新的 TCP+TLS 握手。上限复用 maxRespBody：
// 业务方接口异常时可能吐出一个巨大的 body，排空不能没有边界。
func drainBody(r io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r, maxRespBody))
}
