package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
)

// SessionRevoker 是 AccountService 需要的最小撤销能力。
// 用窄接口而不是直接依赖 *SessionService，是为了让这层的意图一目了然：
// 它只需要"把某个用户踢下线"，不需要会话管理的其余部分。
type SessionRevoker interface {
	RevokeUser(ctx context.Context, userID uuid.UUID, reason string) (int, error)
}

// AccountService 承载那些"改完账号状态必须同时作废会话"的操作。
//
// 这类耦合绝不能留在传输层：SessionService.Validate 不看用户状态，
// 所以撤销调用本身就是冻结/改密唯一的执行点。放在 handler 里意味着
// 每新增一个传输层（计划二的 gRPC、将来的自助改密）都要重新实现一遍，
// 漏掉一次就静默失去保护，且没有任何测试会因此变红。
type AccountService struct {
	users    *UserService
	sessions SessionRevoker
}

// NewAccountService 构造 AccountService。
func NewAccountService(users *UserService, sessions SessionRevoker) *AccountService {
	return &AccountService{users: users, sessions: sessions}
}

// SetStatus 迁移用户状态；迁移到不可登录的状态时连带撤销其全部会话。
//
// 撤销放在状态写入**之后**：状态是权威事实，先落库；撤销失败时状态已改、
// 会话未清，重试本操作可自愈（SetStatus 对相同状态是空操作，撤销会重跑）。
// 反过来先撤销再改状态的话，改状态失败会留下"被踢下线但仍是正常状态"的用户，
// 那才是真正难以察觉的中间态。
func (s *AccountService) SetStatus(ctx context.Context, userID uuid.UUID, status string) (*domain.User, error) {
	u, err := s.users.SetStatus(ctx, userID, status)
	if err != nil {
		return nil, err
	}
	if !u.CanLogin() {
		if _, err := s.sessions.RevokeUser(ctx, userID, domain.RevokeReasonFreeze); err != nil {
			return nil, err
		}
	}
	return u, nil
}

// ResetPassword 重置密码并作废该用户的全部会话。
//
// 不撤销的话，"密码泄露了赶紧改密码"这个动作等于什么都没做——
// 攻击者手里已经拿到的会话照常有效。
func (s *AccountService) ResetPassword(ctx context.Context, userID uuid.UUID, plain string) error {
	if err := s.users.SetPassword(ctx, userID, plain); err != nil {
		return err
	}
	_, err := s.sessions.RevokeUser(ctx, userID, domain.RevokeReasonPasswordChanged)
	return err
}
