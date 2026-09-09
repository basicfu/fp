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

// VerifyRequest 是一次身份验证的全部输入。
// 用结构体而不是继续往参数列表里加：不同实现用到的字段不同，
// fp 侧只需要 App 与 Token，业务方侧只需要 App 与 Raw。
// 以后加第三种认证方式时，这里加字段不会波及已有实现的签名。
type VerifyRequest struct {
	App   string
	Kind  string // "" 或 model.TokenKindFP 或 model.TokenKindBiz
	Token string
	// Raw 是 client 发上来的原始握手帧字节。业务方回调把它原样转发，
	// 这样 client 塞的自定义字段（设备指纹之类）也能到业务方手里。
	Raw []byte
}

// Authenticator 验证 client 的凭证，返回它对应的主体。
type Authenticator interface {
	Verify(ctx context.Context, req VerifyRequest) (model.Subject, error)
}

// AppConfigSource 提供 app 级配置。
//
// 拆成 Load 与 Get 两个方法，是因为配置的来源从本地文件变成了 fp：拉取有
// 网络 I/O，而 Get 在握手与 Push 的热路径上被调用，必须是纯内存读。
//
// 调用约定是"冷路径先 Load，之后热路径随便 Get"——某个 app 的握手一定先于
// 它的任何消息投递，所以这个顺序天然成立。
type AppConfigSource interface {
	// Load 确保 app 的配置已在本地缓存里。可能有网络 I/O。
	Load(ctx context.Context, app string) error
	// Get 是纯内存读。
	Get(app string) (model.AppConfig, bool)
	// Apps 返回已缓存的 app 列表。
	Apps() []string
}
