package fpsdk

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// SDK 对外的哨兵错误。业务方用 errors.Is 判定。
var (
	// ErrNoToken 请求里没有 token。
	ErrNoToken = errors.New("fpsdk: 缺少 token")
	// ErrUnauthorized token 无效、已过期或已被撤销。
	ErrUnauthorized = errors.New("fpsdk: token 无效或已过期")
	// ErrUnavailable fp 不可达，且本地没有可用的缓存结果。
	ErrUnavailable = errors.New("fpsdk: fp 不可达且无可用缓存")
)

// Identity 是一次成功校验得到的身份。
type Identity struct {
	UserID    string
	SessionID string
	// RotatedTo 非空时，**必须**把这个新 token 下发给客户端（换 cookie /
	// 回响应头）。不下发的话，该会话会在 fp 的轮换过渡期结束后被登出。
	RotatedTo string
	// Stale 为 true 表示这是 fp 不可达期间返回的陈旧结果。
	// 业务方可据此拒绝高危操作。
	Stale bool
}

// Auth 是认证能力。用 (*Client).Auth() 取得，并发安全。
type Auth struct {
	c     *Client
	cache *cache
	sf    singleflight.Group
}

// Validate 校验 token，命中本地缓存时不产生任何网络往返。
func (a *Auth) Validate(ctx context.Context, token string) (*Identity, error) {
	if token == "" {
		return nil, ErrNoToken
	}

	// 推送流断开 = 收不到撤销事件 = 撤销只剩 TTL 兜底。
	// 主动收紧窗口把安全性拉回来。上限传给 get，因此对**存量条目**同样生效。
	var maxTTL time.Duration
	if !a.c.StreamHealthy() {
		maxTTL = a.c.opts.DegradedCacheTTL
	}
	var maxStale time.Duration
	if a.c.opts.AllowStaleOnOutage {
		maxStale = a.c.opts.MaxStaleness
	}

	if e, state := a.cache.get(token, maxTTL, maxStale); state == cacheFresh {
		return identityFrom(e, false), nil
	}

	// singleflight：缓存过期的瞬间，同一个 token 的并发请求会同时 miss。
	// 不合并的话，一个热门用户的 N 个并发请求会同时打到 fp——正是缓存
	// 要防的惊群。功能上完全正常，只有 fp 的负载会被放大 N 倍。
	v, err, _ := a.sf.Do(token, func() (any, error) {
		// 必须在发 RPC 之前抓这个代际：RPC 在飞行途中，同一个 token 可能被
		// drop（撤销推送、或另一个 goroutine 的 Logout）或 purge，那样这次
		// 回源带回的判定已经不可信，回填时必须能识别出"代际变过"并放弃
		// 写入——见 cache.putIfGen 与 gen 字段的注释。
		gen := a.cache.generation()

		callCtx, cancel := context.WithTimeout(ctx, a.c.opts.ValidateTimeout)
		defer cancel()

		res, err := a.c.rpc.ValidateToken(callCtx, &fpv1.ValidateTokenRequest{Token: token})
		if err != nil {
			return nil, err
		}
		e := entry{
			userID:    res.GetUserId(),
			sessionID: res.GetSessionId(),
			rotatedTo: res.GetNewToken(),
		}
		// cache_ttl_ms == 0 表示"不要缓存"。put 内部会原样忽略，
		// 这里不做任何"没给就用默认值"的兜底——那正是契约禁止的事。
		a.cache.putIfGen(token, e, time.Duration(res.GetCacheTtlMs())*time.Millisecond, gen)
		return e, nil
	})
	if err == nil {
		return identityFrom(v.(entry), false), nil
	}

	switch status.Code(err) {
	case codes.Unauthenticated, codes.PermissionDenied:
		// **不缓存失败结果。** 缓存有容量上限，把失败也塞进去的话，
		// 攻击者用海量随机 token 就能把真实条目全部挤出 LRU，
		// 逼得每个正常请求都回源——一次廉价攻击让 fp 承受全量鉴权流量。
		return nil, ErrUnauthorized
	}

	// 走到这里是 fp 不可达（Unavailable / DeadlineExceeded / 连接错误）。
	// 只有在调用方显式允许时，才用"刚才验过的那个身份"兜底。
	if maxStale > 0 {
		if e, state := a.cache.get(token, maxTTL, maxStale); state == cacheStale {
			a.c.opts.Logger.Warn("fpsdk: fp 不可达，使用陈旧的校验结果",
				"userId", e.userID, "err", err)
			return identityFrom(e, true), nil
		}
	}
	// 没有任何缓存条目时一律拒绝：此刻既验证不了 token，也拿不出身份，
	// "放行"没有任何可以赋予的含义。
	return nil, errors.Join(ErrUnavailable, err)
}

func identityFrom(e entry, stale bool) *Identity {
	return &Identity{
		UserID:    e.userID,
		SessionID: e.sessionID,
		RotatedTo: e.rotatedTo,
		Stale:     stale,
	}
}

// onRevoke 是撤销事件的处理入口，由 Client 的 Watch 循环调用。
func (a *Auth) onRevoke(ev *fpv1.RevokeEvent) {
	a.cache.drop(ev.GetTokens()...)
}

// onPurge 丢弃全部缓存，由 Client 的 Watch 循环在收到 WatchPurge 时调用。
//
// 服务端只在"确知漏读了撤销事件、却不知道漏了哪些"时发这条指令
// （目前唯一触发源是 fp 那侧的 Redis 订阅重建）。代价是一波回源——
// 但只有真正被使用的 token 才会回源，相当于把一个 cache_ttl 周期的
// 回源压缩到更短的窗口里，而不是一次性尖峰。
func (a *Auth) onPurge() {
	a.cache.purge()
}

// LoginInput 是一次登录请求。
type LoginInput struct {
	ConnectorType string
	Credentials   map[string]string
	IP            string
	UserAgent     string
	Mobile        bool
}

// LoginResult 是登录成功的结果。
type LoginResult struct {
	Token     string
	SessionID string
	User      *fpv1.UserInfo
}

// SendLoginCode 给手机号发送登录验证码。
func (a *Auth) SendLoginCode(ctx context.Context, phone string) error {
	_, err := a.c.rpc.SendLoginCode(ctx, &fpv1.SendLoginCodeRequest{Phone: phone})
	return translate(err)
}

// Login 用凭据换取会话 token。
func (a *Auth) Login(ctx context.Context, in LoginInput) (*LoginResult, error) {
	res, err := a.c.rpc.Login(ctx, &fpv1.LoginRequest{
		ConnectorType: in.ConnectorType,
		Credentials:   in.Credentials,
		Ip:            in.IP,
		UserAgent:     in.UserAgent,
		Mobile:        in.Mobile,
	})
	if err != nil {
		return nil, translate(err)
	}
	return &LoginResult{Token: res.GetToken(), SessionID: res.GetSessionId(), User: res.GetUser()}, nil
}

// Logout 撤销一个 token，并立即清掉本地缓存。
//
// 清缓存不能省：撤销推送会异步到达，但同一进程内紧接着的请求可能在
// 事件到达前就命中了那条缓存——用户点了退出，下一个请求还是登录态。
func (a *Auth) Logout(ctx context.Context, token string) error {
	_, err := a.c.rpc.Logout(ctx, &fpv1.LogoutRequest{Token: token})
	a.cache.drop(token)
	return translate(err)
}

// translate 把 gRPC status 转成 SDK 的哨兵错误，
// 让业务方用 errors.Is 判定而不必 import grpc 的 codes 包。
func translate(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.Unauthenticated, codes.PermissionDenied:
		return errors.Join(ErrUnauthorized, err)
	case codes.Unavailable, codes.DeadlineExceeded:
		return errors.Join(ErrUnavailable, err)
	}
	return err
}
