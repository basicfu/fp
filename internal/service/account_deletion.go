package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/basicfu/fp/internal/domain"
)

// DefaultDeletionCoolingOff 是没配 account_deletion.cooling_off_days 时的冷静期。
const DefaultDeletionCoolingOff = 15 * 24 * time.Hour

// AccountOption 调整 AccountService 的可选参数。
type AccountOption func(*AccountService)

// WithDeletionCoolingOff 设置注销冷静期。不大于 0 时保持默认值。
func WithDeletionCoolingOff(d time.Duration) AccountOption {
	return func(s *AccountService) {
		if d > 0 {
			s.coolingOff = d
		}
	}
}

// RequestDeletion 替用户提交注销，返回预计永久删除的时间（unix 毫秒）。
//
// ACTIVE 进入冷静期；已在冷静期内时幂等，按原提交时间算，不重新计时。
// FROZEN 不许自助注销：冻结多半是风控或纠纷，让被冻结的人借注销把账号连同证据一起删掉不合适。
//
// 顺序与 SetStatus 同一条论证：状态先落库，再递增纪元，最后清扫会话。
// 已在冷静期内的那一支同样重跑纪元与撤销——上一次可能停在"状态已改、会话未清"，
// 业务方重试必须能自愈，否则账号在冷静期里一直挂着活会话。
func (s *AccountService) RequestDeletion(ctx context.Context, userID uuid.UUID) (int64, error) {
	u, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return 0, err
	}
	switch u.Status {
	case domain.UserStatusActive:
		if u, err = s.users.SetStatus(ctx, userID, domain.UserStatusPendingDelete); err != nil {
			return 0, err
		}
		if err := s.revokeForDeletion(ctx, userID, true); err != nil {
			return 0, err
		}
	case domain.UserStatusPendingDelete:
		if err := s.revokeForDeletion(ctx, userID, false); err != nil {
			return 0, err
		}
	default:
		// 调用方是受信任的业务服务端，按状态如实回码不构成账号枚举。
		return 0, accountUnavailableError(u)
	}
	return u.DeleteSubmittedAt + s.coolingOff.Milliseconds(), nil
}

// revokeForDeletion 作废该用户全部应用、全部设备的会话。
// audit 为假时只有真撤到了会话才记审计：幂等重试不该每次都留一条"撤销了 0 个"。
func (s *AccountService) revokeForDeletion(ctx context.Context, userID uuid.UUID, audit bool) error {
	if _, err := s.epochs.Bump(ctx, userID); err != nil {
		return err
	}
	n, err := s.sessions.RevokeUser(ctx, userID, domain.RevokeReasonDeletion)
	if err != nil {
		return err
	}
	if audit || n > 0 {
		s.writeRevokeLog(ctx, userID, domain.RevokeReasonDeletion, "")
	}
	return nil
}

// SweepDeletions 跑一轮注销清理：先让冷静期满的注销生效，再删掉留存期满的归档。
//
// 每个用户一个事务、行锁 SKIP LOCKED 领取，多实例同时跑各领各的，不会重复处理也不会互相等。
// 中途出错就停下返回，已处理的不回滚；剩下的留给下一轮。
func (s *AccountService) SweepDeletions(ctx context.Context) (purged, expired int, err error) {
	for {
		id, ok, err := s.users.purgeOneDue(ctx, s.coolingOff)
		if err != nil {
			return purged, expired, err
		}
		if !ok {
			break
		}
		purged++
		// 正常情况下这时已没有会话：提交时撤过，冷静期里登录又会复活成 ACTIVE、轮不到这里。
		// 兜的是管理员在控制台直接置 PENDING_DELETE（不撤会话）和提交时与登录并发的那一次签发。
		// 失败只记日志：注销已经生效，残留的会话也会随空闲超时消失。
		if _, err := s.epochs.Bump(ctx, id); err != nil {
			slog.Error("service: 注销生效后递增纪元失败", "err", err, "userId", id)
		} else if n, err := s.sessions.RevokeUser(ctx, id, domain.RevokeReasonDeletion); err != nil {
			slog.Error("service: 注销生效后撤销会话失败", "err", err, "userId", id)
		} else if n > 0 {
			s.writeRevokeLog(ctx, id, domain.RevokeReasonDeletion, "")
		}
	}
	for {
		ok, err := s.users.expireOneArchive(ctx)
		if err != nil {
			return purged, expired, err
		}
		if !ok {
			return purged, expired, nil
		}
		expired++
	}
}

// RunDeletionSweep 起动后立即跑一轮，之后每隔 every 跑一轮，直到 ctx 取消。在独立 goroutine 里调用。
func (s *AccountService) RunDeletionSweep(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		purged, expired, err := s.SweepDeletions(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Error("service: 注销清理失败，下一轮重试", "err", err, "purged", purged, "expired", expired)
		} else if purged > 0 || expired > 0 {
			slog.Info("service: 注销清理完成", "purged", purged, "expired", expired)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// purgeOneDue 领取一个冷静期已满的用户，在一个事务里让他的注销生效：
// 归档最少身份信息 → 删登录标识、注册关系、角色、头像 → 清资料 → 置 DELETED。没有到期的用户时 ok 为假。
//
// app_user 行留作墓碑：业务方（kkdesk）按 user_id 问状态时要能看到"已注销"，
// 而不是一个分不清"注销了"还是"id 写错了"的 USER_NOT_FOUND；归档期满时随归档一起删。
// 登录标识删掉之后手机号、微信就释放了，再登录会注册成一个全新的用户。
func (s *UserService) purgeOneDue(ctx context.Context, coolingOff time.Duration) (uuid.UUID, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("service: 开启事务: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// 时间一律取数据库的 now()：多实例各自的时钟不一致时，谁先到期不该取决于哪台机器在跑。
	// 冷静期内的登录复活走 SetStatus 的条件更新，会在这把行锁上排队，提交后看到 DELETED 而失败。
	var id uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT id FROM app_user
		WHERE status = $1 AND delete_submitted_at <= now() - $2::float8 * interval '1 millisecond'
		ORDER BY delete_submitted_at
		LIMIT 1
		FOR UPDATE SKIP LOCKED`, domain.UserStatusPendingDelete, coolingOff.Milliseconds()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("service: 领取到期的注销: %w", err)
	}

	// 归档留存期写死 6 个月而不做成配置：业务方（kkdesk 的 retainMonths）按同一个期限删自己的数据，两边要一起改。
	// 用日历月（interval '6 months'）而不是固定天数，和业务方的按月计算对得上。
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_deletion_archive (user_id, phones, wechat_union_ids, submitted_at, deleted_at, expires_at)
		SELECT u.id,
		       coalesce((SELECT array_agg(DISTINCT subject ORDER BY subject) FROM identity
		                 WHERE user_id = u.id AND type = $2), '{}'),
		       coalesce((SELECT array_agg(DISTINCT union_key ORDER BY union_key) FROM identity
		                 WHERE user_id = u.id AND union_key <> ''), '{}'),
		       u.delete_submitted_at, now(), now() + interval '6 months'
		FROM app_user u WHERE u.id = $1`, id, domain.IdentityTypePhone); err != nil {
		return uuid.Nil, false, fmt.Errorf("service: 写入注销归档: %w", err)
	}
	for _, stmt := range []string{
		`DELETE FROM identity WHERE user_id = $1`,
		`DELETE FROM user_extra WHERE user_id = $1`,
		`DELETE FROM user_role WHERE user_id = $1`,
		`DELETE FROM user_avatar WHERE user_id = $1`,
	} {
		if _, err := tx.Exec(ctx, stmt, id); err != nil {
			return uuid.Nil, false, fmt.Errorf("service: 注销清理 %q: %w", stmt, err)
		}
	}
	// delete_submitted_at 也清掉：提交时间已在归档里，墓碑只留"这个 id 已注销"这一件事。
	if _, err := tx.Exec(ctx, `
		UPDATE app_user
		SET status = $2, password_hash = '', nickname = '', avatar_url = '', avatar_hash = '', gender = '',
		    delete_submitted_at = NULL, updated_at = now()
		WHERE id = $1`, id, domain.UserStatusDeleted); err != nil {
		return uuid.Nil, false, fmt.Errorf("service: 注销置墓碑: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, false, fmt.Errorf("service: 提交事务: %w", err)
	}
	return id, true, nil
}

// expireOneArchive 领取一条留存期满的归档，删掉它、该用户的登录日志与墓碑。没有到期的归档时返回假。
//
// 登录日志必须显式删：login_log.user_id 是 ON DELETE SET NULL，只删墓碑的话日志会变成
// 一堆带 IP、UA 却查不到主人的行，留存期满了却没真删掉。
func (s *UserService) expireOneArchive(ctx context.Context) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("service: 开启事务: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var id uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT user_id FROM user_deletion_archive
		WHERE expires_at <= now()
		ORDER BY expires_at
		LIMIT 1
		FOR UPDATE SKIP LOCKED`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("service: 领取到期的注销归档: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM login_log WHERE user_id = $1`, id); err != nil {
		return false, fmt.Errorf("service: 删除注销用户的登录日志: %w", err)
	}
	// 只删墓碑：万一同一个 id 不是 DELETED（不该发生），宁可留着一个活账号也不能误删。
	if _, err := tx.Exec(ctx, `DELETE FROM app_user WHERE id = $1 AND status = $2`, id, domain.UserStatusDeleted); err != nil {
		return false, fmt.Errorf("service: 删除注销墓碑: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_deletion_archive WHERE user_id = $1`, id); err != nil {
		return false, fmt.Errorf("service: 删除注销归档: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("service: 提交事务: %w", err)
	}
	return true, nil
}
