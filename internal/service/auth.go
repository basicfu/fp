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

// loginCodeTemplate 是登录验证码使用的 fp 内部模板 key。
// 各供应商把它映射到自己的模板 ID（见 notify.AliyunConfig.Templates）。
const loginCodeTemplate = "login_code"

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
		return domain.Errorf(domain.ErrInvalidArgument, "手机号格式不正确")
	}

	code, err := s.deps.Codes.Issue(ctx, notify.PurposeLogin, phone)
	if err != nil {
		return err
	}
	return s.deps.Notifier.Send(ctx, notify.Message{
		Channel:  notify.ChannelSMS,
		To:       phone,
		Template: loginCodeTemplate,
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
		err := domain.Errorf(domain.ErrForbidden, "账号已被冻结或注销")
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

	// 签发之后再读一次用户状态，不可登录就把刚发的会话撤掉。
	if err := s.recheckLoginable(ctx, user.ID, sess); err != nil {
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

// recheckLoginable 在会话写入之后复查用户状态，不可登录就撤销这次签发。
//
// 为什么需要它：上面那次 CanLogin() 和这里的 Sessions.Issue 之间隔着三次
// 数据库往返（EnsureRegistration、TouchIdentityLogin，以及读用户本身），
// 而 SessionService.Validate 是**刻意不看用户状态**的。于是存在这样的交错：
//
//	T1（登录）                        T2（管理员冻结）
//	resolveUser -> U{ACTIVE}
//	CanLogin() -> true
//	                                  users.SetStatus -> FROZEN 已提交
//	                                  sessions.RevokeUser -> 枚举到 0 个 token
//	EnsureRegistration
//	TouchIdentityLogin
//	Sessions.Issue -> 写入新 token
//
// 冻结返回 200、管理员以为生效了，login_log 里还记着一次成功登录，而攻击者
// 手上那个 token 在整个空闲窗口内（默认 7 天，移动端 30 天）一直有效。
//
// **这不是一道完整的栅栏，别把它当成一道。** 它把出问题所需的条件收窄成：
// 状态读取严格早于 FROZEN 提交，**并且** RevokeUser 的枚举严格早于 Issue 的写入。
// 两个条件同时成立的窗口仍然存在——真正的栅栏需要把状态检查和会话写入放进
// 同一个事务边界里（例如给用户加一个撤销版本号，随状态一起递增，签发时带上、
// 校验时比对），那是计划二的事。这里做的只是把"静默失守好几天"换成一个窄得多的
// 竞态，而且它自愈：管理员下一次操作、或者任何一次后续的撤销都会把它清掉。
//
// 读不到用户时按不可登录处理：宁可让一次合法登录失败，也不放行一个可能
// 已经被冻结的会话。
//
// 注意它**盖不住改密**那条同形状的竞态：AccountService.ResetPassword 也会先
// SetPassword 再 RevokeUser，一次校验过旧密码的登录同样可能在 RevokeUser 之后
// 落地。但改密不改变用户状态，CanLogin() 照样为真，这里的复查看不见它。
func (s *AuthService) recheckLoginable(ctx context.Context, userID uuid.UUID, sess *domain.Session) error {
	fresh, err := s.deps.Users.GetByID(ctx, userID)
	if err != nil {
		s.revokeIssued(ctx, sess)
		return err
	}
	if fresh.CanLogin() {
		return nil
	}
	s.revokeIssued(ctx, sess)
	return domain.Errorf(domain.ErrForbidden, "账号已被冻结或注销")
}

// revokeIssued 撤销刚刚签发、随后发现不该签发的会话。
// 撤销失败只记日志：这里已经在返回错误的路上，会话最终也会随空闲超时消失。
func (s *AuthService) revokeIssued(ctx context.Context, sess *domain.Session) {
	if err := s.deps.Sessions.Revoke(ctx, sess.Token, domain.RevokeReasonFreeze); err != nil {
		slog.Error("service: 撤销刚签发的会话失败", "err", err, "sessionId", sess.ID)
	}
}

// Logout 作废 token 所属的整个会话，并留下审计记录。
//
// 记录这一条不是可有可无：登出是账号生命周期里的一个真实事件，
// 排查"这个账号什么时候在哪台设备上退出的"要靠它。domain 里已经定义了
// LoginEventLogout 常量——定义了却从不写入，就是那种"字段存在但没人用"
// 的死代码，而这个任务本身就在别处反对它。
func (s *AuthService) Logout(ctx context.Context, token string) error {
	// 先取会话，拿到 user/app 归属再撤销；撤销之后就查不到了。
	sess, lookupErr := s.deps.Sessions.SessionByToken(ctx, token)

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
// 应用被停用后，登录、发码、token 校验三条入口都必须立即失效，否则
// "停用应用"只是个不生效的标记位——`status` 列有值、有常量，却没人读取，
// 是最容易在后续阶段酿成事故的一类死字段。
//
// 注意：第一阶段还没有把应用置为 DISABLED 的管理接口，因此这条分支目前
// 只能由直接改库触发。这是刻意的：先让字段有意义，再在后续阶段补上开关，
// 而不是反过来先做开关再发现没人校验。
func (s *AuthService) activeApp(ctx context.Context, appID string) (*domain.Application, error) {
	app, err := s.deps.Apps.GetByAppID(ctx, appID)
	if err != nil {
		return nil, err
	}
	if app.Status != domain.ApplicationStatusActive {
		return nil, domain.Errorf(domain.ErrForbidden, "应用已停用")
	}
	return app, nil
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
		return nil, domain.Errorf(domain.ErrForbidden, "该应用未开放 %s 登录", typ)
	}
	if err != nil {
		return nil, err
	}
	if !ac.Enabled {
		return nil, domain.Errorf(domain.ErrForbidden, "该应用未开放 %s 登录", typ)
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
		return nil, nil, domain.Errorf(domain.ErrInvalidCredential, "账号或凭据不正确")
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
