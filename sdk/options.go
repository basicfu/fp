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
// 本任务只定义连接层用得到的字段；缓存与降级相关的选项由 Task 9、Task 10
// 各自随其读取方一起加入，避免出现"字段存在但没人读"的悬空配置。
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

	// Logger 是 SDK 内部日志。为 nil 时用 slog.Default()。
	Logger *slog.Logger
}

const defaultValidateTimeout = 2 * time.Second

func (o *Options) applyDefaults() {
	if o.ValidateTimeout <= 0 {
		o.ValidateTimeout = defaultValidateTimeout
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
