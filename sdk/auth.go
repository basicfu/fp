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

		// 用 context.WithoutCancel 剥掉取消信号：ctx 是"领导者"这一个调用方
		// 的 ctx，而这次 RPC 的结果是共享的，要发给所有正在等同一个 token
		// 的跟随者。领导者的 HTTP 请求被取消（浏览器导航、用户点停止）不
		// 代表 fp 不可达，更不该连累其他参与者——不剥掉的话，领导者 ctx
		// 一取消，RPC 就以 codes.Canceled 收场，落进下面"fp 不可达"的分支，
		// 每一个跟随者都会莫名其妙地拿到 ErrUnavailable，而 fp 全程健康。
		// 共享调用只应该受 ValidateTimeout 支配，不该被任一参与者的取消
		// 拖垮。先例见 internal/service/session.go 的 announceBatch。
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.c.opts.ValidateTimeout)
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
	case codes.NotFound:
		// GetActiveByAppID 找不到应用时返回这个码——fp 给出的是确定答案
		// （这个应用不存在），不是"够不着"。多半意味着接入方的 appId
		// 配错了，或者应用已经被管理端删除，这类配置错误应该立刻失败
		// 关闭，而不是被下面的陈旧兜底悄悄放行 MaxStaleness（默认 5
		// 分钟）——那样接入方会以为自己在正常运行，实际上验证的应用
		// 已经不存在了。用 WARN 而不是普通的鉴权失败去处理，是为了在
		// 日志里把它和"token 单纯过期/被撤销"区分开：后者是正常流量，
		// 前者几乎总是配置问题，值得被人看到。
		a.c.opts.Logger.Warn("fpsdk: fp 返回 NotFound，按配置错误处理——"+
			"应用可能不存在或已被删除，请检查 Options.AppID", "err", err)
		return nil, ErrUnauthorized
	}

	// 走到这里的 err 覆盖 Unavailable / DeadlineExceeded / 连接错误（真正
	// 够不着 fp），以及除上面两个 case 之外的其他 gRPC 状态码（例如
	// Internal、ResourceExhausted）——fp 给出了响应，但响应本身是"我这边
	// 出问题了"，语义上与"够不着"是同一类：调用方拿不到一个可信的鉴权
	// 判定，能做的只有和真正不可达时一样的处理。NotFound 不属于这一类，
	// 上面已经把它并入确定拒绝分支提前返回。
	//
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
//
// codes.NotFound 归进 ErrUnauthorized，与 Validate 那侧的判断保持一致
// （见其注释）：它是 fp 给出的确定答案，不是"够不着"。
func translate(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.Unauthenticated, codes.PermissionDenied, codes.NotFound:
		return errors.Join(ErrUnauthorized, err)
	case codes.Unavailable, codes.DeadlineExceeded:
		return errors.Join(ErrUnavailable, err)
	}
	return err
}
