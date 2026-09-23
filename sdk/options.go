package fpsdk

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// Options 是 SDK 的全部配置。
//
// 本任务只定义连接层与缓存层用得到的字段；降级相关的选项由 Task 10
// 随其读取方一起加入，避免出现"字段存在但没人读"的悬空配置。
type Options struct {
	// Addr 是 fp 的 gRPC 地址，支持两种写法：
	//   - 裸 "host:port"（比如 "fp.internal:9090"）：明文还是 TLS 完全由
	//     下面的 TLS 字段决定，这是兼容原有全部调用方的形态，一个字节都
	//     不用变（fp-im 连 fp、examples 下的示例都是这么写的）。
	//   - 带 scheme 的 "https://host:port" / "http://host:port"：图省事
	//     用一条地址同时表达"连哪儿"和"要不要加密"，不用维护两个独立
	//     配置项。scheme 只在这里被解析、剥掉，最终传给 grpc-go 拨号的
	//     还是纯粹的 "host:port"——gRPC 自己的 target 语法里没有
	//     http/https 这两个 scheme，这层转换是 fpsdk 替调用方做的，不是
	//     grpc-go 原生认识的写法。
	//
	// 两种写法与 TLS 字段的优先级见 New() 内部 resolveAddr 的注释：
	// https:// 时 TLS 为 nil 会自动补一个默认的 &tls.Config{}；http:// 时
	// 若 TLS 非 nil 视为自相矛盾，直接报错，不猜哪个是真实意图。
	Addr string
	// TLS 为 nil 时明文连接（零值，兼容原有的全部调用方，行为不变）；
	// 非 nil 时用这份配置建 TLS 连接（credentials.NewTLS(o.TLS)），原样
	// 交给 grpc-go，SDK 自己不对内容做任何校验或改写——多数情况下
	// &tls.Config{} 空结构体就够（用系统根证书池验证），自签名证书场景
	// 按需要填 RootCAs/ServerName。
	TLS *tls.Config
	// AppID / AppSecret 是应用凭据，来自 fp 控制台。
	//
	// CallerType 为 CallerTypeIM 时 AppID 可以为空：那种客户端的连接没有
	// 固定 app，作用域逐调用附上（见 WithAppID）。
	AppID     string
	AppSecret string

	// CallerType 声明调用方类型，空表示普通业务应用（默认，行为与不设这个
	// 字段完全一致）。业务方**不要**设它。
	//
	// 设为 CallerTypeIM 时：AppID 允许为空；凭据里输出 fp-caller-type: im
	// 而不输出 fp-app-id。fp 对这类调用方开放的是与业务方完全不同的一组
	// 接口——不能 Login、不能读配置中心，能调 ValidateToken/Watch 与
	// IMGateway 的两个 RPC。
	CallerType string

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

	// OnRevoke 在收到 fp 的撤销事件后被调用（本地缓存已先清掉）。
	// 给需要在撤销发生时做额外动作的宿主进程用，比如 fp-im 关闭持有该
	// token 的 ws 连接。
	//
	// 回调在 watch 流的读循环里同步执行，会阻塞同一条流后续事件的处理——
	// 一个慢回调等于拖慢整条推送流，回调必须快（例如只做一次非阻塞的
	// channel 发送/map 查找，重活另起 goroutine）。
	//
	// 为 nil 表示不关心，是绝大多数 SDK 使用方的默认状态，不会因此崩溃。
	OnRevoke func(RevokeEvent)

	// MaxSignedBodyBytes 是访问密钥签名请求的 body 上限，超出回 413。默认 10MB。
	MaxSignedBodyBytes int64
	// NonceCapacity 是本进程记住的 nonce 条数上限。满了拒绝新的签名请求（503），
	// 不挤掉旧记录——挤掉等于关掉防重放。默认 200000。
	NonceCapacity int

	// usageFlushInterval、policyRefreshInterval 只供包内测试缩短周期，零值取默认。
	usageFlushInterval    time.Duration
	policyRefreshInterval time.Duration
}

const (
	defaultValidateTimeout       = 2 * time.Second
	defaultCacheSize             = 10000
	defaultDegradedCacheTTL      = 5 * time.Second
	defaultMaxStaleness          = 5 * time.Minute
	defaultMaxSignedBodyBytes    = 10 << 20
	defaultNonceCapacity         = 200_000
	defaultUsageFlushInterval    = time.Minute
	defaultPolicyRefreshInterval = 5 * time.Minute
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
	if o.MaxSignedBodyBytes <= 0 {
		o.MaxSignedBodyBytes = defaultMaxSignedBodyBytes
	}
	if o.NonceCapacity <= 0 {
		o.NonceCapacity = defaultNonceCapacity
	}
	if o.usageFlushInterval <= 0 {
		o.usageFlushInterval = defaultUsageFlushInterval
	}
	if o.policyRefreshInterval <= 0 {
		o.policyRefreshInterval = defaultPolicyRefreshInterval
	}
}

func (o Options) validate() error {
	switch {
	case o.Addr == "":
		return errors.New("fpsdk: Options.Addr 不能为空")
	case o.CallerType != "" && o.CallerType != CallerTypeIM:
		return errors.New("fpsdk: Options.CallerType 只能为空或 " + CallerTypeIM)
	case o.AppID == "" && o.CallerType != CallerTypeIM:
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
	case o.MaxSignedBodyBytes < 0:
		return errors.New("fpsdk: Options.MaxSignedBodyBytes 不能为负")
	case o.NonceCapacity < 0:
		return errors.New("fpsdk: Options.NonceCapacity 不能为负")
	}
	return nil
}

// CallerTypeIM 表示本客户端是 fp-im 网关，凭据是 IM secret 而不是某个应用
// 的 appSecret。**业务方不要用它。**
const CallerTypeIM = "im"

// appCredentials 把应用凭据附加到每个 RPC 的 metadata 上。
type appCredentials struct {
	appID, secret string
	callerType    string
}

func newAppCredentials(o Options) appCredentials {
	return appCredentials{appID: o.AppID, secret: o.AppSecret, callerType: o.CallerType}
}

// GetRequestMetadata 实现 credentials.PerRPCCredentials。
func (c appCredentials) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	if c.callerType == CallerTypeIM {
		// 不输出 fp-app-id：作用域由调用方逐调用附上（WithAppID）。两个都出
		// 的话 metadata 里会有两个值，服务端取哪个是未定义行为。
		return map[string]string{
			"fp-app-secret":  c.secret,
			"fp-caller-type": CallerTypeIM,
		}, nil
	}
	return map[string]string{
		"fp-app-id":     c.appID,
		"fp-app-secret": c.secret,
	}, nil
}

// RequireTransportSecurity 实现 credentials.PerRPCCredentials。恒为
// false——明文和 TLS 两种传输都允许带这份凭据，由调用方通过
// Options.TLS 决定实际走哪种，这里不做二次把关、不因为选了明文就报错
// 或打日志。真要挡明文传凭据的风险，交给部署方式本身（内网直连 vs
// 过反代）决定，不是 SDK 该替调用方做的判断。
func (c appCredentials) RequireTransportSecurity() bool { return false }

// transportCredentials 按 tlsConfig 是否为 nil 选传输层：nil 用明文，
// 非 nil 用 TLS，原样把 tlsConfig 交给 credentials.NewTLS，不做任何
// 校验或改写——调用方给什么就用什么。
func transportCredentials(tlsConfig *tls.Config) credentials.TransportCredentials {
	if tlsConfig == nil {
		return insecure.NewCredentials()
	}
	return credentials.NewTLS(tlsConfig)
}

// resolveAddr 解析 Options.Addr，按需要从 tlsConfig 推出实际要用的
// TLS 配置，返回值分别是"传给 grpc.NewClient 的纯 host:port"和"最终
// 生效的 *tls.Config"。
//
// addr 没有 "://" 时原样返回、tlsConfig 也原样返回——这是兼容既有调用方
// 的关键，New() 对这一支的行为必须跟加这个函数之前逐字节一致。
//
// 带 scheme 时：
//   - "https://host:port"：一定走 TLS。tlsConfig 为 nil 时补一个默认的
//     &tls.Config{}（用系统根证书池验证）；tlsConfig 非 nil 时原样用它
//     （比如调用方要自定义 RootCAs/ServerName），两者不矛盾，非 nil 的
//     那份配置说了算。
//   - "http://host:port"：一定走明文。这时 tlsConfig 非 nil 说明调用方
//     一边用 http:// 表达"要明文"、一边又传了 TLS 配置表达"要加密"，
//     自相矛盾——直接报错，不猜哪个是真实意图，参见本文件其余地方
//     "不静默、错误要在最早能发现的地方暴露"这条一贯的原则。
//   - 其余 scheme（grpc://、dns:// 这些 gRPC 自己的 target scheme，
//     含义跟这里完全不同）一律报错，不静默当成明文处理。
func resolveAddr(addr string, tlsConfig *tls.Config) (string, *tls.Config, error) {
	if !strings.Contains(addr, "://") {
		return addr, tlsConfig, nil
	}
	u, err := url.Parse(addr)
	if err != nil {
		return "", nil, fmt.Errorf("fpsdk: 解析 Options.Addr %q: %w", addr, err)
	}
	switch u.Scheme {
	case "https":
		if tlsConfig == nil {
			tlsConfig = &tls.Config{}
		}
		return u.Host, tlsConfig, nil
	case "http":
		if tlsConfig != nil {
			return "", nil, fmt.Errorf(
				"fpsdk: Options.Addr 是 http:// 但同时设置了 Options.TLS，两者矛盾——去掉其中一个")
		}
		return u.Host, nil, nil
	default:
		return "", nil, fmt.Errorf(
			"fpsdk: Options.Addr 的 scheme %q 不认识，只支持 http:// 或 https://（也可以不写 scheme，直接用 host:port）",
			u.Scheme)
	}
}
