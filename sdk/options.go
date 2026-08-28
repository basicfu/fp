package fpsdk

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"time"
)

// Options 是 SDK 的全部配置。
//
// 本任务只定义连接层与缓存层用得到的字段；降级相关的选项由 Task 10
// 随其读取方一起加入，避免出现"字段存在但没人读"的悬空配置。
type Options struct {
	// Addr 是 fp 的 gRPC 地址，形如 "fp.internal:9090"。
	Addr string
	// AppID / AppSecret 是应用凭据，来自 fp 控制台。
	AppID     string
	AppSecret string

	// Insecure 允许明文连接。
	//
	// **生产绝不要开。** appSecret 随每个 RPC 的 metadata 发送，明文传输
	// 等于把一个能签发任意用户会话的凭据印在网线上。只在本地开发用。
	Insecure bool
	// TLSConfig 自定义 TLS 配置。为 nil 且 Insecure 为 false 时用系统根证书。
	TLSConfig *tls.Config

	// ValidateTimeout 是单次回源的超时。默认 2 秒。
	ValidateTimeout time.Duration

	// CacheSize 是本地校验结果缓存的容量上限（条）。默认 10000。
	CacheSize int

	// DegradedCacheTTL 是**推送流断开时**的缓存时长上限。默认 5 秒。
	//
	// 推送断开意味着撤销的"加速"能力消失，只剩 TTL 兜底。主动收紧窗口
	// 把安全性拉回来——这是 gRPC 流状态可感知才做得了的事。
	DegradedCacheTTL time.Duration

	// AllowStaleOnOutage 决定 fp 不可达时能否继续使用**已过期**的缓存条目。
	//
	// 它不是通常意义上的 "fail-open"。鉴权中间件放行却拿不出用户身份是
	// 讲不通的——那等于接受一个无法验证的 token。真正有意义的降级是
	// "继续用刚才验过的那个身份"：身份已知，只是刷新不了。
	// 完全没有缓存条目时，无论本开关如何都必须拒绝。
	//
	// 零值 false = 不允许。命名为"允许"而非它的反面，是为了让放宽的方向
	// 必须被显式写出来——零值必须落在安全的一侧。
	AllowStaleOnOutage bool

	// MaxStaleness 是 AllowStaleOnOutage 生效时，缓存条目最多能被延用多久
	// （从它本该过期的时刻起算）。默认 5 分钟。
	//
	// 必须有上限：没有上限的话，fp 停机一整天，一个早已被踢下线的会话
	// 就能畅通一整天。
	MaxStaleness time.Duration

	// Logger 是 SDK 内部日志。为 nil 时用 slog.Default()。
	Logger *slog.Logger
}

const (
	defaultValidateTimeout  = 2 * time.Second
	defaultCacheSize        = 10000
	defaultDegradedCacheTTL = 5 * time.Second
	defaultMaxStaleness     = 5 * time.Minute
)

func (o *Options) applyDefaults() {
	if o.ValidateTimeout <= 0 {
		o.ValidateTimeout = defaultValidateTimeout
	}
	if o.CacheSize <= 0 {
		o.CacheSize = defaultCacheSize
	}
	if o.DegradedCacheTTL <= 0 {
		o.DegradedCacheTTL = defaultDegradedCacheTTL
	}
	if o.MaxStaleness <= 0 {
		o.MaxStaleness = defaultMaxStaleness
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

func (o Options) validate() error {
	switch {
	case o.Addr == "":
		return errors.New("fpsdk: Options.Addr 不能为空")
	case o.AppID == "":
		return errors.New("fpsdk: Options.AppID 不能为空")
	case o.AppSecret == "":
		return errors.New("fpsdk: Options.AppSecret 不能为空")
	case o.ValidateTimeout < 0:
		return errors.New("fpsdk: Options.ValidateTimeout 不能为负")
	case o.CacheSize < 0:
		return errors.New("fpsdk: Options.CacheSize 不能为负")
	case o.DegradedCacheTTL < 0:
		return errors.New("fpsdk: Options.DegradedCacheTTL 不能为负")
	case o.MaxStaleness < 0:
		return errors.New("fpsdk: Options.MaxStaleness 不能为负")
	}
	return nil
}

// appCredentials 把应用凭据附加到每个 RPC 的 metadata 上。
type appCredentials struct {
	appID, secret string
	insecure      bool
}

func newAppCredentials(o Options) appCredentials {
	return appCredentials{appID: o.AppID, secret: o.AppSecret, insecure: o.Insecure}
}

// GetRequestMetadata 实现 credentials.PerRPCCredentials。
func (c appCredentials) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{
		"fp-app-id":     c.appID,
		"fp-app-secret": c.secret,
	}, nil
}

// RequireTransportSecurity 实现 credentials.PerRPCCredentials。
//
// 返回 true 时 grpc-go 会拒绝在明文连接上发送这些凭据——这正是我们要的：
// appSecret 一旦被抓包，攻击者就能签发任意用户的会话。只有显式设了
// Insecure 才放行明文。
func (c appCredentials) RequireTransportSecurity() bool { return !c.insecure }
