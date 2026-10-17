package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/basicfu/fp/internal/domain"
)

// LinkPhoneInput 是一次「三方身份 + 手机号」绑定。调用方必须已经验过手机验证码。
type LinkPhoneInput struct {
	Phone string
	// Identity 是三方身份。Nickname / AvatarURL 只在新建用户时使用。
	Identity EnsureIdentityInput
}

// LinkPhoneResult 是绑定结果。
type LinkPhoneResult struct {
	User     *domain.User
	Identity *domain.Identity
	Created  bool
	// Replaced 是从目标账号上换下来的旧三方身份（自动换绑）。
	Replaced []domain.Identity
}

// LinkPhone 把三方身份绑到手机号对应的账号上，没有账号就建一个。
//
// 目标账号：手机号的账号 ?? 这个三方身份已挂的账号 ?? 新建。
//
// 自动换绑：目标账号上同类型的旧身份删掉，同一旧 union_key 的其它身份一并删——
// 只删同类型那一行的话，旧微信以后靠 unionId 从公众号 / 小程序登录又会归并回来。
//
// 挪绑：这个三方身份（同 subject 或同 union_key）若挂在别的账号上，从那边摘下。
// 能走到这里说明调用方同时证明了手机号和三方身份的归属，两者理应落在同一个账号。
func (s *UserService) LinkPhone(ctx context.Context, in LinkPhoneInput) (*LinkPhoneResult, error) {
	in.Identity = in.Identity.normalize()
	if err := in.Identity.validate(); err != nil {
		return nil, err
	}
	in.Phone = normalizeSubject(domain.IdentityTypePhone, in.Phone)
	if in.Phone == "" {
		return nil, domain.Fail(domain.ErrInvalidArgument, domain.CodePhoneInvalid, "手机号不能为空")
	}

	res, err := s.linkPhoneOnce(ctx, in)
	// 短信登录建号不拿手机号锁，同一手机号的并发首次登录会让这里撞上唯一约束；重试一次必然看到那个账号。
	if errors.Is(err, domain.ErrConflict) {
		return s.linkPhoneOnce(ctx, in)
	}
	return res, err
}

func (s *UserService) linkPhoneOnce(ctx context.Context, in LinkPhoneInput) (*LinkPhoneResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("service: 开启事务: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// 固定先手机号后 union_key 的加锁顺序，并发的两次绑定不会互相死锁。
	if err := lockUnionKeyTx(ctx, tx, "phone:"+in.Phone); err != nil {
		return nil, err
	}
	tp := in.Identity
	if tp.UnionKey != "" {
		if err := lockUnionKeyTx(ctx, tx, tp.UnionKey); err != nil {
			return nil, err
		}
	}

	var target uuid.UUID
	phoneUser, _, err := findByIdentityTx(ctx, tx, domain.IdentityTypePhone, in.Phone)
	switch {
	case err == nil:
		target = phoneUser.ID
	case !errors.Is(err, domain.ErrNotFound):
		return nil, err
	default:
		if target, err = thirdPartyOwnerTx(ctx, tx, tp); err != nil {
			return nil, err
		}
	}

	res := &LinkPhoneResult{}
	if target == uuid.Nil {
		u, err := scanUser(tx.QueryRow(ctx,
			`INSERT INTO app_user (nickname, avatar_url) VALUES ($1, $2) RETURNING `+userColumns,
			tp.Nickname, tp.AvatarURL))
		if err != nil {
			return nil, fmt.Errorf("service: 创建用户: %w", err)
		}
		target, res.Created = u.ID, true
	}
	if phoneUser == nil {
		if _, err := insertIdentityTx(ctx, tx, target, EnsureIdentityInput{Type: domain.IdentityTypePhone, Subject: in.Phone}); err != nil {
			return nil, err
		}
	}

	if res.Replaced, err = deleteIdentitiesTx(ctx, tx,
		`DELETE FROM identity WHERE user_id = $1 AND type = $2 AND subject <> $3 RETURNING `+identityColumns,
		target, tp.Type, tp.Subject); err != nil {
		return nil, err
	}
	for _, old := range res.Replaced {
		if old.UnionKey == "" || old.UnionKey == tp.UnionKey {
			continue
		}
		more, err := deleteIdentitiesTx(ctx, tx,
			`DELETE FROM identity WHERE user_id = $1 AND union_key = $2 RETURNING `+identityColumns, target, old.UnionKey)
		if err != nil {
			return nil, err
		}
		res.Replaced = append(res.Replaced, more...)
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM identity
		WHERE user_id <> $1 AND ((type = $2 AND subject = $3) OR ($4 <> '' AND union_key = $4))`,
		target, tp.Type, tp.Subject, tp.UnionKey); err != nil {
		return nil, fmt.Errorf("service: 从其它账号摘下三方身份: %w", err)
	}

	_, res.Identity, err = findByIdentityTx(ctx, tx, tp.Type, tp.Subject)
	if errors.Is(err, domain.ErrNotFound) {
		res.Identity, err = insertIdentityTx(ctx, tx, target, tp)
	}
	if err != nil {
		return nil, err
	}

	if res.User, err = getUserTx(ctx, tx, target); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("service: 提交事务: %w", err)
	}
	return res, nil
}

// thirdPartyOwnerTx 找三方身份已挂的账号：先按 (type, subject)，再按 union_key。没有返回 uuid.Nil。
func thirdPartyOwnerTx(ctx context.Context, tx pgx.Tx, tp EnsureIdentityInput) (uuid.UUID, error) {
	u, _, err := findByIdentityTx(ctx, tx, tp.Type, tp.Subject)
	if err == nil {
		return u.ID, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return uuid.Nil, err
	}
	if tp.UnionKey == "" {
		return uuid.Nil, nil
	}
	var owner uuid.UUID
	err = tx.QueryRow(ctx, `SELECT user_id FROM identity WHERE union_key = $1 LIMIT 1`, tp.UnionKey).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("service: 按 unionKey 查询归属: %w", err)
	}
	return owner, nil
}

func deleteIdentitiesTx(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]domain.Identity, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("service: 删除 identity: %w", err)
	}
	defer rows.Close()
	var out []domain.Identity
	for rows.Next() {
		id, err := scanIdentity(rows)
		if err != nil {
			return nil, fmt.Errorf("service: 扫描 identity: %w", err)
		}
		out = append(out, *id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service: 删除 identity: %w", err)
	}
	return out, nil
}
