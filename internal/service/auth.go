package service

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/notify"
)

// LoginCodeTemplate 是登录验证码使用的 fp 内部模板 key。
// 各供应商把它映射到自己的模板 ID（见 notify.AliyunConfig.Templates）。
// 导出它是为了 cmd/fp/main.go 装配 notify.AliyunConfig.Templates 时可以
// 直接引用这个常量，而不是重复写一遍 "login_code" 字面量——两处一旦
// 打字不一致，SendLoginCode 会在第一次真实发送时才报"模板未映射"，
// 而不是在装配阶段就暴露出来。
const LoginCodeTemplate = "login_code"

// AuthDeps 是 AuthService 的依赖集合。
type AuthDeps struct {
	Apps     *ApplicationService
	Users    *UserService
	Sessions *SessionService
	Logs     *LoginLogService
	Registry *connector.Registry
	Notifier *notify.Sender
	Codes    *notify.CodeService
}

// AuthService 编排登录流程。它是唯一知道"登录该按什么顺序发生"的地方：
// Connector 只管校验凭据，UserService 只管归并，SessionService 只管令牌。
type AuthService struct {
	deps AuthDeps
}

// NewAuthService 构造 AuthService。
func NewAuthService(d AuthDeps) *AuthService {
	return &AuthService{deps: d}
}

// LoginInput 是一次登录请求。
type LoginInput struct {
	// AppID 是对外的 appId 字符串，不是内部 UUID。
	AppID         string
	ConnectorType string
	Credentials   connector.Credentials
	IP            string
	UA            string
	Mobile        bool
}

// LoginResult 是登录成功的结果。
type LoginResult struct {
	User    *domain.User
	Session *domain.Session
}

// SendLoginCode 给手机号发送登录验证码。
//
// 校验顺序：应用 → 登录方式是否启用 → 手机号格式 → 频率限制 → 发送。
// 格式校验必须早于验证码生成，否则畸形号码也会消耗一次发送额度。
func (s *AuthService) SendLoginCode(ctx context.Context, appID, phone string) error {
	app, err := s.activeApp(ctx, appID)
	if err != nil {
		return err
	}
	if _, err := s.enabledConnector(ctx, app, connector.TypeSMSCode); err != nil {
		return err
	}
	if connector.DetectIdentityType(phone) != domain.IdentityTypePhone {
		return domain.Failf(domain.ErrInvalidArgument, domain.CodePhoneInvalid, "手机号格式不正确")
	}

	code, err := s.deps.Codes.Issue(ctx, notify.PurposeLogin, phone)
	if err != nil {
		return err
	}
	return s.deps.Notifier.Send(ctx, notify.Message{
		Channel:  notify.ChannelSMS,
		To:       phone,
		Template: LoginCodeTemplate,
		Params:   map[string]string{"code": code},
	})
}

// Login 执行一次完整登录。
func (s *AuthService) Login(ctx context.Context, in LoginInput) (*LoginResult, error) {
	app, err := s.activeApp(ctx, in.AppID)
	if err != nil {
		return nil, err
	}

	conn, cfg, err := s.connectorFor(ctx, app, in.ConnectorType)
	if err != nil {
		return nil, err
	}

	result, err := conn.Authenticate(ctx, cfg, in.Credentials)
	if err != nil {
		// 校验失败时没有 Result，从凭据里尽力还原登录标识，
		// 否则爆破攻击在审计日志里只是一串无主记录。
		identityType, subject := conn.SubjectFrom(in.Credentials)
		s.logFailure(ctx, app, in, identityType, subject, err)
		return nil, err
	}

	user, identity, err := s.resolveUser(ctx, result)
	if err != nil {
		s.logFailure(ctx, app, in, result.IdentityType, result.Subject, err)
		return nil, err
	}

	if !user.CanLogin() {
		err := accountUnavailableError(user)
		s.logFailureWithUser(ctx, app, in, result, user.ID, err)
		return nil, err
	}

	// 注销保护期内的登录撤销注销申请——沿用 3s 的行为。
	if user.Status == domain.UserStatusPendingDelete {
		revived, err := s.deps.Users.SetStatus(ctx, user.ID, domain.UserStatusActive)
		if err != nil {
			s.logFailureWithUser(ctx, app, in, result, user.ID, err)
			return nil, err
		}
		user = revived
	}

	// 认证之后的每一步失败，都必须照样留下审计记录。
	//
	// 这些恰恰是"凭据已经验过、一次性验证码已经被消费掉"的那些尝试——
	// 出事故时最想查的就是它们。只在凭据校验失败时记录，等于把成功通过
	// 认证却没能建立会话的那批请求全部丢进黑洞。
	if err := s.deps.Users.EnsureRegistration(ctx, user.ID, app.ID); err != nil {
		s.logFailureWithUser(ctx, app, in, result, user.ID, err)
		return nil, err
	}
	if identity != nil {
		if err := s.deps.Users.TouchIdentityLogin(ctx, identity.ID); err != nil {
			s.logFailureWithUser(ctx, app, in, result, user.ID, err)
			return nil, err
		}
	}

	sess, err := s.deps.Sessions.Issue(ctx, IssueInput{
		UserID: user.ID, App: app, IP: in.IP, UA: in.UA, Mobile: in.Mobile,
	})
	if err != nil {
		s.logFailureWithUser(ctx, app, in, result, user.ID, err)
		return nil, err
	}

	// 签发之后再读一次用户：冻结或改密都要把刚发的会话撤掉。
	if err := s.recheckLoginable(ctx, user, sess); err != nil {
		s.logFailureWithUser(ctx, app, in, result, user.ID, err)
		return nil, err
	}

	s.writeLog(ctx, domain.LoginLog{
		UserID: &user.ID, ApplicationID: &app.ID,
		IdentityType: result.IdentityType,
		Subject:      MaskSubject(result.IdentityType, result.Subject),
		Event:        domain.LoginEventLogin, Success: true,
		IP: in.IP, UA: in.UA, SessionID: sess.ID,
	})

	return &LoginResult{User: user, Session: sess}, nil
}

// recheckLoginable 在会话写入之后复查用户，发现账号在这期间被改动过就撤销这次签发。
//
// 为什么需要它：上面那次 CanLogin() 和这里的 Sessions.Issue 之间隔着三次
// 数据库往返（EnsureRegistration、TouchIdentityLogin，以及读用户本身），
// 而 SessionService.Validate 是**刻意不看用户状态**的。于是存在这样的交错：
//
//	T1（登录）                        T2（管理员冻结 / 改密）
//	resolveUser -> U{ACTIVE}
//	CanLogin() -> true
//	                                  users.SetStatus / SetPassword 已提交
//	                                  sessions.RevokeUser -> 枚举到 0 个 token
//	EnsureRegistration
//	TouchIdentityLogin
//	Sessions.Issue -> 写入新 token
//
// 管理动作返回 200、管理员以为生效了，login_log 里还记着一次成功登录，而攻击者
// 手上那个 token 在整个空闲窗口内（默认 7 天，移动端 30 天）一直有效。
//
// 复查两件事：
//
//  1. CanLogin()——盖住冻结/注销。
//  2. password_hash 是否变过——盖住改密。改密**不改变用户状态**，
//     CanLogin() 照样为真，光看状态是看不见它的。
//
// 第 2 条为什么用哈希比对而不是时间戳：bcrypt 每次都用新的随机盐，
// 所以哪怕把密码改成和原来一模一样的字符串，产出的哈希串也必然不同。
// 于是"哈希不相等"精确等价于"这中间跑过一次 SetPassword"——不会漏、也不会误报。
// **别把它"优化"成比较 updated_at 之类的时间戳**：那既会被其他更新列的操作
// 误触发，又依赖数据库时钟精度，两头都不如直接比哈希准。
//
// 这一条对**每一种登录方式**都成立，包括压根没碰过密码的 sms_code：
// ResetPassword 的语义是"把这个账号现有的会话全部作废"，一次恰好在
// RevokeUser 枚举之后落地的短信登录同样绕过了它，没有理由放行。
//
// 读不到用户时按不可登录处理：宁可让一次合法登录失败，也不放行一个
// 可能已经被冻结、或凭据已经换掉的会话。
//
// **这仍然不是一道完整的栅栏，别把它当成一道。** 它把出问题所需的条件收窄成：
// 快照读取严格早于管理动作提交，**并且** RevokeUser 的枚举严格早于 Issue 的写入。
// 两个条件同时成立的窗口仍然存在。另外还剩一小段它够不着的区间：密码登录时，
// connector 自己校验口令发生在 resolveUser 读快照**之前**，改密若恰好落在这两步
// 之间，快照拿到的已经是新哈希，比对就看不出差异了（短信登录没有这一段，
// 因为它的凭据校验与 password_hash 无关）。真正的栅栏需要把校验和会话写入放进
// 同一个事务边界里——例如给用户加一个撤销版本号，随状态和密码一起递增，
// 签发时带上、校验时比对——那是计划二的事。这里做的只是把"静默失守好几天"
// 换成一个窄得多的竞态，而且它自愈：管理员下一次操作、或任何一次后续撤销都会清掉它。
func (s *AuthService) recheckLoginable(ctx context.Context, user *domain.User, sess *domain.Session) error {
	fresh, err := s.deps.Users.GetByID(ctx, user.ID)
	if err != nil {
		s.revokeIssued(ctx, sess, domain.RevokeReasonFreeze)
		return err
	}
	if !fresh.CanLogin() {
		s.revokeIssued(ctx, sess, domain.RevokeReasonFreeze)
		return accountUnavailableError(fresh)
	}
	if fresh.PasswordHash != user.PasswordHash {
		s.revokeIssued(ctx, sess, domain.RevokeReasonPasswordChanged)
		return domain.Fail(domain.ErrUnauthorized, domain.CodeTokenInvalid, "登录已过期，请重新登录").
			WithDesc("账号凭据已变更（改密或纪元失配），已强制下线")
	}
	return nil
}

// revokeIssued 撤销刚刚签发、随后发现不该签发的会话。
// 撤销失败只记日志：这里已经在返回错误的路上，会话最终也会随空闲超时消失。
func (s *AuthService) revokeIssued(ctx context.Context, sess *domain.Session, reason string) {
	if err := s.deps.Sessions.Revoke(ctx, sess.Token, reason); err != nil {
		slog.Error("service: 撤销刚签发的会话失败", "err", err, "sessionId", sess.ID)
	}
}

// Logout 作废 token 所属的整个会话，并留下审计记录。
//
// 记录这一条不是可有可无：登出是账号生命周期里的一个真实事件，
// 排查"这个账号什么时候在哪台设备上退出的"要靠它。domain 里已经定义了
// LoginEventLogout 常量——定义了却从不写入，就是那种"字段存在但没人用"
// 的死代码，而这个任务本身就在别处反对它。
//
// appID 归属校验与 Validate 是同一等级的安全性质，不是可选项：不比对的话，
// 同一个 token 在 ValidateToken 上跨应用被拒、在 Logout 上却跨应用畅通——
// A 应用的 token 只要能被 B 应用收到（例如两个接入方共享 CookieDomain 时
// 浏览器会把 cookie 一并发给 B），B 就能单方面撤销 A 的会话。
func (s *AuthService) Logout(ctx context.Context, appID, token string) error {
	app, err := s.activeApp(ctx, appID)
	if err != nil {
		return err
	}

	// 先取会话，拿到 user/app 归属再撤销；撤销之后就查不到了。
	sess, lookupErr := s.deps.Sessions.SessionByToken(ctx, token)

	// 查到了但不属于调用方应用：按 Validate 同一套"不区分失败原因"的语义
	// 拒绝——不能提前用 Revoke 之外的错误分支泄露"这个 token 存在，只是
	// 不归你"。必须在调用 Revoke 之前拦下来：Revoke 不认应用归属，一旦
	// 放过去就会把别的应用的会话真的删掉。
	if lookupErr == nil && sess != nil && sess.AppID != app.ID {
		return domain.Failf(domain.ErrUnauthorized, domain.CodeTokenInvalid, "登录已过期，请重新登录")
	}

	if err := s.deps.Sessions.Revoke(ctx, token, domain.RevokeReasonLogout); err != nil {
		return err
	}

	// 查不到会话（重复登出、token 已过期）就没有可记的归属信息，静默略过。
	if lookupErr != nil || sess == nil {
		return nil
	}
	s.writeLog(ctx, domain.LoginLog{
		UserID:        &sess.UserID,
		ApplicationID: &sess.AppID,
		Event:         domain.LoginEventLogout,
		Success:       true,
		IP:            sess.IP,
		UA:            sess.UA,
		SessionID:     sess.ID,
	})
	return nil
}

// ValidateToken 是 SDK 回源的入口：校验 token 并给出缓存时长。
func (s *AuthService) ValidateToken(ctx context.Context, appID, token string) (*ValidateResult, error) {
	app, err := s.activeApp(ctx, appID)
	if err != nil {
		return nil, err
	}
	return s.deps.Sessions.Validate(ctx, token, app)
}

// activeApp 取应用并要求它处于启用状态。
//
// 单一执行点在 ApplicationService.GetActiveByAppID：那条判断的完整理由
// （为什么必须存在、为什么不能散在各调用点）写在那边，这里不重复。
func (s *AuthService) activeApp(ctx context.Context, appID string) (*domain.Application, error) {
	return s.deps.Apps.GetActiveByAppID(ctx, appID)
}

// connectorFor 取出该应用启用的登录方式及其配置。
func (s *AuthService) connectorFor(ctx context.Context, app *domain.Application, typ string) (connector.Connector, map[string]any, error) {
	ac, err := s.enabledConnector(ctx, app, typ)
	if err != nil {
		return nil, nil, err
	}
	conn, err := s.deps.Registry.Get(typ)
	if err != nil {
		return nil, nil, err
	}
	return conn, ac.Config, nil
}

// enabledConnector 校验该应用是否启用了指定登录方式。
// 应用级开关必须在校验凭据之前判断，否则被关掉的方式仍能完成验证。
func (s *AuthService) enabledConnector(ctx context.Context, app *domain.Application, typ string) (*domain.ApplicationConnector, error) {
	ac, err := s.deps.Apps.GetConnector(ctx, app.ID, typ)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, domain.Failf(domain.ErrForbidden, domain.CodeConnectorDisabled, "该应用未开放 %s 登录", typ)
	}
	if err != nil {
		return nil, err
	}
	if !ac.Enabled {
		return nil, domain.Failf(domain.ErrForbidden, domain.CodeConnectorDisabled, "该应用未开放 %s 登录", typ)
	}
	return ac, nil
}

// resolveUser 依据 Connector 给出的结果找到或创建用户。
// 归并规则完整地封装在 UserService 里，这里只决定"允不允许建号"。
func (s *AuthService) resolveUser(ctx context.Context, r *connector.Result) (*domain.User, *domain.Identity, error) {
	if r.AllowCreate {
		user, identity, _, err := s.deps.Users.EnsureUserWithIdentity(ctx, EnsureIdentityInput{
			Type:       r.IdentityType,
			Subject:    r.Subject,
			UnionKey:   r.UnionKey,
			Credential: r.Credential,
			Nickname:   r.Nickname,
		})
		return user, identity, err
	}

	user, identity, err := s.deps.Users.FindByIdentity(ctx, r.IdentityType, r.Subject)
	if errors.Is(err, domain.ErrNotFound) {
		// 不允许建号且账号不存在：返回与凭据错误一致的错误，避免账号枚举。
		return nil, nil, domain.Failf(domain.ErrInvalidCredential, domain.CodeCredentialInvalid, "账号或凭据不正确")
	}
	return user, identity, err
}

// logFailure 记录一次失败登录。
//
// 若该登录标识确实对应一个已有账号，就把 user_id 一并写上——
// 「某个账号被连续尝试 50 次」这个查询依赖它。标识不存在时留空即可，
// 这一步的查询失败绝不能影响返回给调用方的错误。
func (s *AuthService) logFailure(ctx context.Context, app *domain.Application, in LoginInput, identityType, subject string, cause error) {
	var userID *uuid.UUID
	if identityType != "" && subject != "" {
		if u, _, err := s.deps.Users.FindByIdentity(ctx, identityType, subject); err == nil {
			userID = &u.ID
		}
	}
	s.writeLog(ctx, domain.LoginLog{
		UserID:        userID,
		ApplicationID: &app.ID,
		IdentityType:  identityType,
		Subject:       MaskSubject(identityType, subject),
		Event:         domain.LoginEventLogin,
		Success:       false,
		Reason:        cause.Error(),
		IP:            in.IP, UA: in.UA,
	})
}

func (s *AuthService) logFailureWithUser(ctx context.Context, app *domain.Application, in LoginInput, r *connector.Result, userID uuid.UUID, cause error) {
	s.writeLog(ctx, domain.LoginLog{
		UserID:        &userID,
		ApplicationID: &app.ID,
		IdentityType:  r.IdentityType,
		Subject:       MaskSubject(r.IdentityType, r.Subject),
		Event:         domain.LoginEventLogin,
		Success:       false,
		Reason:        cause.Error(),
		IP:            in.IP, UA: in.UA,
	})
}

// writeLog 写审计记录。写失败只记日志——审计不能反过来阻断登录。
func (s *AuthService) writeLog(ctx context.Context, e domain.LoginLog) {
	if err := s.deps.Logs.Write(ctx, e); err != nil {
		slog.Error("service: 写入登录日志失败", "err", err)
	}
}

// accountUnavailableError 按用户状态给出登录被拒的错误。
//
// 按 Status 分流而不是一律返回"已冻结"：当前没有任何生产代码会把用户置成
// DELETED（状态机允许 PENDING_DELETE → DELETED，但没有地方执行该迁移），
// PENDING_DELETE 在登录时又会被复活成 ACTIVE，所以实践中只会走到 FROZEN
// 那一支。留着兜底是为了将来真做了注销任务时**不会把已注销的账号谎报成
// "已冻结"**——那种谎报会让用户和客服都往错的方向排查。
//
// 安全前提：这个错误只在 conn.Authenticate() 成功之后才可能返回，也就是
// 调用方已经证明了自己知道密码或持有有效验证码，所以暴露"账号被冻结"
// 不构成账号枚举泄露。不要把这个判断挪到凭据校验之前。
func accountUnavailableError(u *domain.User) *domain.Error {
	if u.Status == domain.UserStatusFrozen {
		return domain.Fail(domain.ErrForbidden, domain.CodeAccountFrozen, "账号已被冻结").
			WithDesc("user=%s status=%s", u.ID, u.Status)
	}
	return domain.Fail(domain.ErrForbidden, domain.CodeAccountUnavailable, "账号当前不可用").
		WithDesc("user=%s status=%s", u.ID, u.Status)
}
