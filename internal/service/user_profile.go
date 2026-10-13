package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/basicfu/fp/internal/domain"
)

// UserProfile 是一个用户的全部个人信息。
//
// 用户全局互通、不与应用关联：任何应用都可以按 user_id 查询、修改任何用户，
// 所以这里的读写都不带应用参数，也没有"该用户是否在本应用登录过"的检查。
type UserProfile struct {
	User       domain.User
	Identities []domain.Identity
	// RegisteredAppID / RegisteredAppName 是注册来源应用的对外 app_id 与名称。
	// 管理员手动建的号、或来源应用已被删除时为空串。仅供展示与统计，不参与权限判断。
	RegisteredAppID   string
	RegisteredAppName string
}

// UserProfileUpdate 是一次资料修改。nil 的字段不改。
type UserProfileUpdate struct {
	Nickname *string
	Gender   *string
}

// GetProfile 按用户 ID 取个人信息。
func (s *UserService) GetProfile(ctx context.Context, id uuid.UUID) (*UserProfile, error) {
	u, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	ids, err := s.ListIdentities(ctx, id)
	if err != nil {
		return nil, err
	}
	p := &UserProfile{User: *u, Identities: ids}
	err = s.pool.QueryRow(ctx, `
		SELECT a.app_id, a.name FROM user_extra e
		JOIN application a ON a.id = e.application_id
		WHERE e.user_id = $1 AND e.is_registration`, id).Scan(&p.RegisteredAppID, &p.RegisteredAppName)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("service: 查询注册来源: %w", err)
	}
	return p, nil
}

// UpdateProfile 修改昵称、性别，返回改完的完整资料。
func (s *UserService) UpdateProfile(ctx context.Context, id uuid.UUID, in UserProfileUpdate) (*UserProfile, error) {
	if in.Nickname != nil {
		nickname, err := domain.NormalizeNickname(*in.Nickname)
		if err != nil {
			return nil, err
		}
		in.Nickname = &nickname
	}
	if in.Gender != nil && !domain.ValidGender(*in.Gender) {
		return nil, domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument,
			"性别只能是 %s、%s 或留空", domain.GenderMale, domain.GenderFemale)
	}
	if in.Nickname != nil || in.Gender != nil {
		tag, err := s.pool.Exec(ctx, `
			UPDATE app_user
			SET nickname = coalesce($2::text, nickname), gender = coalesce($3::text, gender), updated_at = now()
			WHERE id = $1`, id, in.Nickname, in.Gender)
		if err != nil {
			return nil, fmt.Errorf("service: 更新用户资料: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return nil, domain.Failf(domain.ErrNotFound, domain.CodeUserNotFound, "用户不存在")
		}
	}
	return s.GetProfile(ctx, id)
}

// UpdateAvatar 把 image 存为该用户的头像，返回改完的完整资料。image 为空表示清除头像。
//
// fp 对图片只存不处理：不解码、不裁剪、不压缩。规格由业务方在上传前自行处理。
func (s *UserService) UpdateAvatar(ctx context.Context, id uuid.UUID, image []byte) (*UserProfile, error) {
	var hash, contentType string
	if len(image) > 0 {
		var err error
		if contentType, err = domain.CheckAvatar(image); err != nil {
			return nil, err
		}
		hash = domain.AvatarHash(image)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("service: 开启事务: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// 先改 app_user：这一行的行锁让同一用户的并发换头像排队，两张表不会各留一方的结果。
	tag, err := tx.Exec(ctx,
		`UPDATE app_user SET avatar_hash = $2, updated_at = now() WHERE id = $1`, id, hash)
	if err != nil {
		return nil, fmt.Errorf("service: 更新头像哈希: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, domain.Failf(domain.ErrNotFound, domain.CodeUserNotFound, "用户不存在")
	}
	if hash == "" {
		_, err = tx.Exec(ctx, `DELETE FROM user_avatar WHERE user_id = $1`, id)
	} else {
		_, err = tx.Exec(ctx, `
			INSERT INTO user_avatar (user_id, hash, content_type, data) VALUES ($1, $2, $3, $4)
			ON CONFLICT (user_id) DO UPDATE
			SET hash = EXCLUDED.hash, content_type = EXCLUDED.content_type, data = EXCLUDED.data, updated_at = now()`,
			id, hash, contentType, image)
	}
	if err != nil {
		return nil, fmt.Errorf("service: 写入头像: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("service: 提交事务: %w", err)
	}
	return s.GetProfile(ctx, id)
}

// GetAvatar 按内容哈希取头像。
//
// 按哈希而不是按用户取：哈希即版本，同一个哈希的内容永不变化，业务方可以无限期缓存。
// 两个用户传了同一张图时各存一行，取哪行都一样。
func (s *UserService) GetAvatar(ctx context.Context, hash string) (contentType string, image []byte, err error) {
	notFound := domain.Fail(domain.ErrNotFound, domain.CodeAvatarNotFound, "头像不存在")
	if !domain.IsAvatarHash(hash) {
		return "", nil, notFound
	}
	err = s.pool.QueryRow(ctx,
		`SELECT content_type, data FROM user_avatar WHERE hash = $1 LIMIT 1`, hash).Scan(&contentType, &image)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, notFound
	}
	if err != nil {
		return "", nil, fmt.Errorf("service: 查询头像: %w", err)
	}
	return contentType, image, nil
}
