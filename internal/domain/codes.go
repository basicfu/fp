package domain

// 错误码。机器可读的稳定契约——接入方据此分支判断。
//
// 命名与分组见 docs/superpowers/specs/2026-09-04-fp-error-code-design.md。
// 同一类情形共用一个码，靠 Msg 与 Detail 区分；码的数量刻意收敛，
// 每个都应当是调用方真正会分支判断的东西。
//
// **一经发布不得更名，也不得复用于其他语义。**
const (
	// 登录与认证。这一组的 Msg 会被接入方直接展示给终端用户。

	// CodeCredentialInvalid 同时覆盖账号不存在、账号类型未开放、密码错误
	// 三种情况，**刻意不拆**——拆开等于给攻击者一个账号枚举预言机。
	CodeCredentialInvalid = "CREDENTIAL_INVALID"
	// CodeCodeInvalid 合并"验证码错误"与"验证码已过期"，同样刻意不拆：
	// 拆开会泄露某个手机号有没有被发过验证码。
	CodeCodeInvalid = "CODE_INVALID"
	// CodeAccountFrozen 只在 user.Status == FROZEN 时使用。
	//
	// 能看到这个码的调用方已经先通过了 Authenticate（凭据校验在
	// CanLogin 之前），所以暴露冻结原因不构成账号枚举泄露。
	// 这个顺序是安全前提，不要调换。
	CodeAccountFrozen = "ACCOUNT_FROZEN"
	// CodeAccountUnavailable 是 CanLogin() 为假但状态不是 FROZEN 时的兜底。
	//
	// 当前没有任何生产代码会把用户置成 DELETED，PENDING_DELETE 在登录时
	// 又会被复活成 ACTIVE，所以实践中走不到这里。留着它是为了将来真做了
	// 注销任务时不会把已注销的账号谎报成"已冻结"。
	CodeAccountUnavailable = "ACCOUNT_UNAVAILABLE"
	// CodeTokenInvalid 覆盖 token 不存在、已过期、以及被强制下线
	//（改密码/纪元失配）。对使用者而言都是"要重新登录"，不再细分。
	CodeTokenInvalid = "TOKEN_INVALID"

	CodeAppDisabled          = "APP_DISABLED"
	CodeAppNotFound          = "APP_NOT_FOUND"
	CodeAppCredentialInvalid = "APP_CREDENTIAL_INVALID"
	CodeConnectorDisabled    = "CONNECTOR_DISABLED"
	CodeConnectorUnknown     = "CONNECTOR_UNKNOWN"
	CodeRateLimited          = "RATE_LIMITED"

	// 管理端。

	CodeAdminCredentialInvalid = "ADMIN_CREDENTIAL_INVALID"
	CodeAdminDisabled          = "ADMIN_DISABLED"
	// CodeAdminSessionInvalid 合并"缺少凭据"、"凭据无效或已过期"、
	// "凭据损坏"——后者对使用者没有意义（那是 cookie 被篡改或存储损坏），
	// 差异进 Detail["desc"]。
	CodeAdminSessionInvalid = "ADMIN_SESSION_INVALID"

	// 参数与校验。

	// CodeInvalidArgument 是通用参数错误。各调用点保留自己的具体文案
	// 作为 Msg——消费方是控制台与 SDK 的参数校验，接入方不会按码细分。
	CodeInvalidArgument = "INVALID_ARGUMENT"
	// CodeConnectorConfigInvalid 的 Detail 带 field（出问题的配置项键），
	// 让控制台将来能把错误定位到具体表单字段上。
	CodeConnectorConfigInvalid = "CONNECTOR_CONFIG_INVALID"
	// CodeConfigValueInvalid 是配置值按声明类型转换失败。Detail 里带原值与目标类型。
	CodeConfigValueInvalid = "CONFIG_VALUE_INVALID"
	// CodeConfigTypeInvalid 覆盖分区与值类型两处的取值不合法。
	CodeConfigTypeInvalid  = "CONFIG_TYPE_INVALID"
	CodePhoneInvalid       = "PHONE_INVALID"
	CodePasswordTooShort           = "PASSWORD_TOO_SHORT"
	CodePasswordTooLong            = "PASSWORD_TOO_LONG"
	CodeSlugTaken                  = "SLUG_TAKEN"
	CodeUnionKeyConflict           = "UNION_KEY_CONFLICT"
	CodeConnectorAlreadyRegistered = "CONNECTOR_ALREADY_REGISTERED"

	// 资源不存在。

	CodeUserNotFound           = "USER_NOT_FOUND"
	CodeIdentityNotFound       = "IDENTITY_NOT_FOUND"
	CodeUnionKeyNotFound       = "UNION_KEY_NOT_FOUND"
	CodeSessionNotFound        = "SESSION_NOT_FOUND"
	CodeConnectorNotConfigured = "CONNECTOR_NOT_CONFIGURED"
	CodeNotifyProviderMissing  = "NOTIFY_PROVIDER_MISSING"
	CodeSMSTemplateMissing     = "SMS_TEMPLATE_MISSING"

	// CodeRouteNotFound 是路由层的 404：请求的 HTTP 路径不存在。
	// 与"资源不存在"那一组不同——它意味着调用方把 URL 写错了，
	// 而不是某个 id 查不到。
	CodeRouteNotFound = "ROUTE_NOT_FOUND"

	// 内部错误与不变式违背。
	//
	// 编程错误或启动配置错误，接入方分支判断没有意义。它们仍然显式带码
	// （保持"每个对外错误都有码"），但详细信息只进服务端日志、不进
	// Detail——那些消息可能带连接串、SQL、表名。
	CodeInternal = "INTERNAL"
)

// codeSentinels 是码到哨兵的注册表。
//
// 它是**唯一**的映射来源：httpapi 与 grpcapi 都从哨兵推导状态码，
// 而哨兵从这里取。此前两个传输层各写一份 switch、靠注释约定对齐，
// 没有任何测试守住；现在由 TestCodeRegistryMatchesTransports 断言一致。
var codeSentinels = map[string]error{
	CodeCredentialInvalid:  ErrInvalidCredential,
	CodeCodeInvalid:        ErrInvalidCredential,
	CodeAccountFrozen:      ErrForbidden,
	CodeAccountUnavailable: ErrForbidden,
	CodeTokenInvalid:       ErrUnauthorized,

	CodeAppDisabled:          ErrForbidden,
	CodeAppNotFound:          ErrNotFound,
	CodeAppCredentialInvalid: ErrInvalidCredential,
	CodeConnectorDisabled:    ErrForbidden,
	CodeConnectorUnknown:     ErrNotFound,
	CodeRateLimited:          ErrRateLimited,

	CodeAdminCredentialInvalid: ErrInvalidCredential,
	CodeAdminDisabled:          ErrForbidden,
	CodeAdminSessionInvalid:    ErrUnauthorized,

	CodeInvalidArgument:            ErrInvalidArgument,
	CodeConnectorConfigInvalid:     ErrInvalidArgument,
	CodeConfigValueInvalid:         ErrInvalidArgument,
	CodeConfigTypeInvalid:          ErrInvalidArgument,
	CodePhoneInvalid:               ErrInvalidArgument,
	CodePasswordTooShort:           ErrInvalidArgument,
	CodePasswordTooLong:            ErrInvalidArgument,
	CodeSlugTaken:                  ErrConflict,
	CodeUnionKeyConflict:           ErrConflict,
	CodeConnectorAlreadyRegistered: ErrConflict,

	CodeUserNotFound:           ErrNotFound,
	CodeIdentityNotFound:       ErrNotFound,
	CodeUnionKeyNotFound:       ErrNotFound,
	CodeSessionNotFound:        ErrNotFound,
	CodeConnectorNotConfigured: ErrNotFound,
	CodeNotifyProviderMissing:  ErrNotFound,
	CodeSMSTemplateMissing:     ErrNotFound,
	CodeRouteNotFound:          ErrNotFound,

	CodeInternal: ErrInternal,
}

// SentinelFor 返回某个码登记的哨兵。未登记时第二个返回值为 false。
func SentinelFor(code string) (error, bool) {
	s, ok := codeSentinels[code]
	return s, ok
}

// AllCodes 返回全部已登记的码，供测试遍历。顺序不保证。
func AllCodes() []string {
	out := make([]string, 0, len(codeSentinels))
	for c := range codeSentinels {
		out = append(out, c)
	}
	return out
}
