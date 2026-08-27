package service

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
)

// SessionRevoker 是 AccountService 需要的最小撤销能力。
// 用窄接口而不是直接依赖 *SessionService，是为了让这层的意图一目了然：
// 它只需要"把某个用户（或一批用户）踢下线"，不需要会话管理的其余部分。
type SessionRevoker interface {
	RevokeUser(ctx context.Context, userID uuid.UUID, reason string) (int, error)
	RevokeUsers(ctx context.Context, userIDs []uuid.UUID, reason string) (int, error)
	RevokeSession(ctx context.Context, userID uuid.UUID, sessionID, reason string) (int, error)
}

// EpochBumper 是 AccountService 需要的最小纪元能力。
type EpochBumper interface {
	Bump(ctx context.Context, userID uuid.UUID) (int64, error)
}

// AccountService 承载那些"改完账号状态必须同时作废会话"的操作。
//
// 这类耦合绝不能留在传输层：SessionService.Validate 不看用户状态，
// 所以撤销调用本身就是冻结/改密唯一的执行点。放在 handler 里意味着
// 每新增一个传输层（计划二的 gRPC、将来的自助改密）都要重新实现一遍，
// 漏掉一次就静默失去保护，且没有任何测试会因此变红。
//
// 同一条理由适用于审计：每一次撤销都必须留痕，所以写审计也在这一层，
// 不在 handler 里。
type AccountService struct {
	users    *UserService
	sessions SessionRevoker
	epochs   EpochBumper
	logs     *LoginLogService
}

// NewAccountService 构造 AccountService。
func NewAccountService(users *UserService, sessions SessionRevoker, epochs EpochBumper, logs *LoginLogService) *AccountService {
	return &AccountService{users: users, sessions: sessions, epochs: epochs, logs: logs}
}

// SetStatus 迁移用户状态；迁移到不可登录的状态时连带撤销其全部会话。
//
// 撤销放在状态写入**之后**：状态是权威事实，先落库；撤销失败时状态已改、
// 会话未清，重试本操作可自愈（SetStatus 对相同状态是空操作，撤销会重跑）。
// 反过来先撤销再改状态的话，改状态失败会留下"被踢下线但仍是正常状态"的用户，
// 那才是真正难以察觉的中间态。
//
// 状态写入与纪元递增之间的顺序同样是死的：先状态、再纪元、最后清扫会话。
// 纪元递增早于状态写入的话，会破坏"纪元 + AuthService.recheckLoginable
// 合起来才是签发竞态的围栏"这个论证（见 SDD 简报），而所有测试仍然是绿的——
// 这个顺序错误没有任何单元测试能直接抓到，只能靠代码审查守住。
func (s *AccountService) SetStatus(ctx context.Context, userID uuid.UUID, status string) (*domain.User, error) {
	u, err := s.users.SetStatus(ctx, userID, status)
	if err != nil {
		return nil, err
	}
	if !u.CanLogin() {
		// 顺序是死的：状态已在上一步落库，纪元必须在清扫**之前**递增。
		// 挪到状态写入之前会破坏本任务开头论证的围栏，且测试不会变红。
		if _, err := s.epochs.Bump(ctx, userID); err != nil {
			return nil, err
		}
		if _, err := s.sessions.RevokeUser(ctx, userID, domain.RevokeReasonFreeze); err != nil {
			return nil, err
		}
		s.writeRevokeLog(ctx, userID, domain.RevokeReasonFreeze, "")
	}
	return u, nil
}

// ResetPassword 重置密码并作废该用户的全部会话。
//
// 不撤销的话，"密码泄露了赶紧改密码"这个动作等于什么都没做——
// 攻击者手里已经拿到的会话照常有效。纪元递增同理插在密码落库之后、
// 撤销之前：正在签发路上的会话也要被拦住，不能只清扫已存在的那些。
func (s *AccountService) ResetPassword(ctx context.Context, userID uuid.UUID, plain string) error {
	if err := s.users.SetPassword(ctx, userID, plain); err != nil {
		return err
	}
	if _, err := s.epochs.Bump(ctx, userID); err != nil {
		return err
	}
	if _, err := s.sessions.RevokeUser(ctx, userID, domain.RevokeReasonPasswordChanged); err != nil {
		return err
	}
	s.writeRevokeLog(ctx, userID, domain.RevokeReasonPasswordChanged, "")
	return nil
}

// RevokeAllSessions 是管理员手动"踢下线全部设备"。
//
// 它不改账号状态，纯粹是把当前的会话清掉；用户下次照样能登录。
// 是 RevokeUsersSessions 的单元素包装——批量与单用户共用同一个实现。
func (s *AccountService) RevokeAllSessions(ctx context.Context, userID uuid.UUID) (int, error) {
	return s.RevokeUsersSessions(ctx, []uuid.UUID{userID})
}

// RevokeUsersSessions 批量踢下线。逐个递增纪元——纪元是用户级的，
// 不存在"批次纪元"，漏掉谁就拦不住谁正在签发路上的会话。
//
// 踢下线常常是"怀疑账号失陷"的应急动作，所以也递增纪元，把正在签发路上的
// 那个会话一并拦下。代价：与踢下线并发的一次合法登录会失败一次，用户重登
// 即可——竞态窗口是微秒级，而漏掉一个失陷会话的代价大得多。
func (s *AccountService) RevokeUsersSessions(ctx context.Context, userIDs []uuid.UUID) (int, error) {
	for _, id := range userIDs {
		if _, err := s.epochs.Bump(ctx, id); err != nil {
			return 0, err
		}
	}
	n, err := s.sessions.RevokeUsers(ctx, userIDs, domain.RevokeReasonKick)
	if err != nil {
		return n, err
	}
	for _, id := range userIDs {
		s.writeRevokeLog(ctx, id, domain.RevokeReasonKick, "")
	}
	return n, nil
}

// RevokeSession 是管理员手动踢掉某一台设备。
// sessionID 不属于该用户时返回 0，此时不写审计——什么都没发生。
//
// 绝不能递增纪元：纪元是用户级的，递增会把该用户全部设备一起踢下线——
// "踢掉这台平板"会连累手机、电脑一起掉线。
func (s *AccountService) RevokeSession(ctx context.Context, userID uuid.UUID, sessionID string) (int, error) {
	n, err := s.sessions.RevokeSession(ctx, userID, sessionID, domain.RevokeReasonKick)
	if err != nil {
		return n, err
	}
	if n == 0 {
		return 0, nil
	}
	s.writeRevokeLog(ctx, userID, domain.RevokeReasonKick, sessionID)
	return n, nil
}

// writeRevokeLog 记一条撤销审计。
//
// 没有它的话，审计表在"最后一次登录"和"下一次登录"之间是完全空白的：
// 管理员冻结了账号、重置了密码、踢掉了全部设备，事后一条都查不到。
// 而这三件事恰恰是账号生命周期里最需要追责的操作。
//
// Reason 写的是 domain.RevokeReason* 常量本身而不是一句人话，这样
// "查出所有因改密而触发的撤销"是一次等值匹配，不用去 LIKE 一段中文。
// ApplicationID 留空：撤销跨全部应用，硬塞一个应用 ID 反而是假信息。
//
// 写失败只记日志，不回传错误——撤销已经真实发生了，让管理员看到一个
// 失败结果去重试一个已经成功的操作，比丢一条审计更糟。
func (s *AccountService) writeRevokeLog(ctx context.Context, userID uuid.UUID, reason, sessionID string) {
	if s.logs == nil {
		return
	}
	err := s.logs.Write(ctx, domain.LoginLog{
		UserID:    &userID,
		Event:     domain.LoginEventRevoke,
		Success:   true,
		Reason:    reason,
		SessionID: sessionID,
	})
	if err != nil {
		slog.Error("service: 写入撤销审计失败", "err", err, "userId", userID, "reason", reason)
	}
}
