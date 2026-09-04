// Package auth 定义 fp-im 对外部身份与配置的两个依赖接口。
// im 的其他包只认这两个接口；fp 的具体实现在 fpauth / appcfg。
package auth

import (
	"context"
	"errors"

	"github.com/basicfu/fp/internal/im/model"
)

var (
	ErrUnauthorized = errors.New("auth: 凭证无效")
	ErrUnavailable  = errors.New("auth: 身份服务不可用")
)

// Authenticator 用 app 的凭据去验 client 的 token。
type Authenticator interface {
	Verify(ctx context.Context, app, token string) (model.Subject, error)
}

// AppConfigSource 提供 app 级配置。Get 必须是纯内存读：握手与 Push 的热路径都会调它。
type AppConfigSource interface {
	Get(app string) (model.AppConfig, bool)
	Apps() []string
}
