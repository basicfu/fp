package fpsdk

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// ErrIMNotAvailable 表示这个应用现在不能接入 fp-im：不存在、已停用、或者
// 没有在控制台打开 IM 接入。
//
// 与 ErrUnauthorized 分开是必需的，不是分类癖：fp-im 拿它决定给 client 的
// 关闭码。这一类是 4002（策略拒绝，别重连），而 ErrUnauthorized 是 4001
// （去重新登录）。给错了的话 client 会去重新登录，登录完还是连不上，
// 变成死循环。
var ErrIMNotAvailable = errors.New("fpsdk: 该应用未启用 IM 接入")

// WithAppID 把 app 作用域附在这一次调用上。
//
// 只有 CallerType 为 CallerTypeIM 的客户端需要它：那种客户端的连接凭据里
// 没有 fp-app-id，作用域来自每个 client 的 ws 握手帧，一条连接上会连着为
// 不同的应用发起调用。
//
// metadata 里的 app-id 之所以看起来"钉死在连接上"，只是因为普通客户端把它
// 放进了 PerRPCCredentials，不代表不能逐调用带。正是这一点让 ValidateToken
// 与 Watch 一个字段都不用改。
func WithAppID(ctx context.Context, appID string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "fp-app-id", appID)
}

// IMGateway 是 fp-im 网关专用的那组接口。业务方用不到。
type IMGateway struct{ c *Client }

// IMGateway 返回网关接口。只有 CallerType 为 CallerTypeIM 的客户端调它才有
// 意义——普通凭据调用会得到 PermissionDenied。
func (c *Client) IMGateway() *IMGateway { return c.imGateway }

// AppIMConfig 是一个应用的 IM 接入配置。
type AppIMConfig struct {
	AllowGuest bool
	// ConnPolicy 取值 replace / reject / limit。
	ConnPolicy  string
	ConnLimit   int32
	GuestIPRate int32
	// BizAuth 为 nil 表示这个应用不支持业务方令牌。
	BizAuth *AppIMBizAuth
}

// AppIMBizAuth 是业务方自有认证的回调配置。
type AppIMBizAuth struct {
	VerifyURL string
	TimeoutMs int32
	CacheSize int32
}

// GetAppIMConfig 拉取 appID 那个应用的 IM 接入配置。
//
// 应用不可用（不存在 / 已停用 / 未启用 IM）时返回 ErrIMNotAvailable，
// 调用方据此给 client 关闭码 4002 而不是 4001。
func (g *IMGateway) GetAppIMConfig(ctx context.Context, appID string) (*AppIMConfig, error) {
	resp, err := g.c.imRPC.GetAppIMConfig(WithAppID(ctx, appID), &fpv1.GetAppIMConfigRequest{})
	if err != nil {
		return nil, translateIM(err)
	}
	cfg := &AppIMConfig{
		AllowGuest:  resp.GetAllowGuest(),
		ConnPolicy:  resp.GetConnPolicy(),
		ConnLimit:   resp.GetConnLimit(),
		GuestIPRate: resp.GetGuestIpRate(),
	}
	if b := resp.GetBizAuth(); b != nil {
		cfg.BizAuth = &AppIMBizAuth{
			VerifyURL: b.GetVerifyUrl(),
			TimeoutMs: b.GetTimeoutMs(),
			CacheSize: b.GetCacheSize(),
		}
	}
	return cfg, nil
}

// VerifyAppCredential 核实 secret 是否为 appID 那个应用的 appSecret。
//
// 凭据无效返回 ErrUnauthorized；凭据有效但该应用没开 IM 接入返回
// ErrIMNotAvailable——运维需要这个区分才知道是去查凭据还是去翻开关。
func (g *IMGateway) VerifyAppCredential(ctx context.Context, appID, secret string) error {
	_, err := g.c.imRPC.VerifyAppCredential(WithAppID(ctx, appID),
		&fpv1.VerifyAppCredentialRequest{Secret: secret})
	return translateIM(err)
}

// translateIM 在 translate 之上多认一个 FailedPrecondition。
//
// translate 不认它（会落到"原样返回"那一支），而网关这两个 RPC 恰恰用它
// 表达"这个应用现在不能接入"——那是调用方唯一需要与"凭据不对"区分开的
// 情况，也是关闭码 4002 与 4001 的分界。
func translateIM(err error) error {
	if err == nil {
		return nil
	}
	if status.Code(err) == codes.FailedPrecondition {
		return ErrIMNotAvailable
	}
	return translate(err)
}

// effectiveAppID 返回本次调用的 app 作用域。
//
// 普通客户端恒为 Options.AppID。CallerType 为 CallerTypeIM 的客户端连接级
// 凭据里没有 app-id，作用域由 WithAppID 逐调用附在 outgoing metadata 上，
// 一条连接会连着为不同应用发起调用——校验缓存与 singleflight 都必须按它
// 分桶，否则会拿一个应用的判定去放行另一个应用的握手。
func (c *Client) effectiveAppID(ctx context.Context) string {
	if md, ok := metadata.FromOutgoingContext(ctx); ok {
		if v := md.Get("fp-app-id"); len(v) > 0 && v[0] != "" {
			return v[0]
		}
	}
	return c.opts.AppID
}
