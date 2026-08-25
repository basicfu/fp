// Package connector 定义登录方式的统一契约。
//
// 新增一种登录方式只需要：实现 Connector 接口，然后在启动时 Register 一行。
// 管理 UI 的配置表单由 ConfigSchema() 自动渲染，无需改动前端。
//
// Connector 只负责「校验凭据并给出登录标识」。建号与账号归并由 AuthService
// 依据 Result.AllowCreate 统一处理，保证归并规则只有一处实现。
package connector

import (
	"context"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
)

// Credentials 是一次登录请求携带的凭据，键名由各 Connector 自行定义。
type Credentials map[string]string

// Get 返回去除首尾空白后的值，键不存在时返回空串。
func (c Credentials) Get(key string) string {
	return strings.TrimSpace(c[key])
}

// Result 是校验成功后 Connector 给出的登录标识。
type Result struct {
	// IdentityType / Subject 唯一确定一个 identity 行。
	IdentityType string
	Subject      string
	// UnionKey 非空时参与跨类型归并（微信 unionId）。
	UnionKey string
	// Credential 存第三方 token 等，密码类不使用。
	Credential string
	// Nickname 仅在需要新建用户时用作初始昵称。
	Nickname string
	// AllowCreate 表示标识不存在时是否允许自动建号。
	// 短信验证码登录为 true（验证码本身证明了手机号归属）；
	// 密码登录为 false（连账号都不存在，谈不上密码正确）。
	AllowCreate bool
}

// Connector 是一种登录方式。
type Connector interface {
	// Type 是该登录方式的稳定标识，同时是 application_connector.connector_type 的值。
	Type() string
	// ConfigSchema 描述该登录方式的可配置项，供管理 UI 渲染表单。
	ConfigSchema() []domain.Field
	// Authenticate 校验凭据。cfg 是该应用为本登录方式保存的配置。
	// 校验失败必须返回 domain.ErrInvalidCredential 的包装。
	Authenticate(ctx context.Context, cfg map[string]any, creds Credentials) (*Result, error)
	// SubjectFrom 尽力从凭据里提取登录标识，仅用于审计日志。
	// 不做校验、不保证标识存在；凭据里没有可用信息时返回两个空串。
	SubjectFrom(creds Credentials) (identityType, subject string)
}

// Registry 是进程内的登录方式注册表。构造后即只读，无需加锁。
type Registry struct {
	m map[string]Connector
}

// NewRegistry 返回空注册表。
func NewRegistry() *Registry {
	return &Registry{m: make(map[string]Connector)}
}

// Register 登记一个登录方式。类型重复或为空时报错。
func (r *Registry) Register(c Connector) error {
	typ := c.Type()
	if typ == "" {
		return domain.Errorf(domain.ErrInvalidArgument, "connector 类型不能为空")
	}
	if _, ok := r.m[typ]; ok {
		return domain.Errorf(domain.ErrConflict, "connector %q 已注册", typ)
	}
	r.m[typ] = c
	return nil
}

// Get 按类型取登录方式。
func (r *Registry) Get(typ string) (Connector, error) {
	c, ok := r.m[typ]
	if !ok {
		return nil, domain.Errorf(domain.ErrNotFound, "未知的登录方式 %q", typ)
	}
	return c, nil
}

// Types 返回已注册的全部类型，按字典序排列。
func (r *Registry) Types() []string {
	out := make([]string, 0, len(r.m))
	for typ := range r.m {
		out = append(out, typ)
	}
	sort.Strings(out)
	return out
}

// Schemas 返回全部登录方式的配置元数据，供管理 UI 一次性拉取。
func (r *Registry) Schemas() map[string][]domain.Field {
	out := make(map[string][]domain.Field, len(r.m))
	for typ, c := range r.m {
		out[typ] = c.ConfigSchema()
	}
	return out
}

// UserLookup 是 password connector 需要的最小用户查询能力。
// 用窄接口而非直接依赖 *service.UserService，避免 connector 包反向依赖 service 包。
type UserLookup interface {
	FindByIdentity(ctx context.Context, identityType, subject string) (*domain.User, *domain.Identity, error)
	VerifyPassword(ctx context.Context, userID uuid.UUID, plain string) error
}

// ConfigInt 从配置里读整数。JSONB 反序列化出的数字是 float64，两种都要接受。
func ConfigInt(cfg map[string]any, key string, def int) int {
	switch v := cfg[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return def
	}
}

// ConfigBool 从配置里读布尔值。
func ConfigBool(cfg map[string]any, key string, def bool) bool {
	if v, ok := cfg[key].(bool); ok {
		return v
	}
	return def
}

// ConfigString 从配置里读字符串。
func ConfigString(cfg map[string]any, key, def string) string {
	if v, ok := cfg[key].(string); ok && v != "" {
		return v
	}
	return def
}
