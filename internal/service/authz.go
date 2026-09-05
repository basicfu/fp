package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/domain"
)

// AuthzService 管理角色、权限点与它们之间的授权关系。
type AuthzService struct {
	pool *pgxpool.Pool
	// staleAfter 是权限点判定"过渡中"的阈值。
	staleAfter time.Duration
}

// NewAuthzService 构造 AuthzService。
func NewAuthzService(pool *pgxpool.Pool) *AuthzService {
	return &AuthzService{pool: pool, staleAfter: domain.DefaultStaleAfter}
}

// ---------------------------------------------------------------------------
// 角色
// ---------------------------------------------------------------------------

// CreateRole 新建全局角色。
func (s *AuthzService) CreateRole(ctx context.Context, key, name string, parentID *uuid.UUID) (*domain.Role, error) {
	if key == "" || name == "" {
		return nil, domain.Fail(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "角色 key 与 name 不能为空")
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO role (key, name, parent_id) VALUES ($1, $2, $3)
		RETURNING `+roleColumns, key, name, parentID)
	r, err := scanRole(row)
	if isUniqueViolation(err) {
		return nil, domain.Failf(domain.ErrConflict, domain.CodeRoleKeyTaken, "角色 %q 已存在", key)
	}
	if err != nil {
		return nil, fmt.Errorf("service: 创建角色: %w", err)
	}
	return r, nil
}

// UpdateRole 改角色的显示名与父角色。
//
// **key 不在可改之列**——user_role.roles 按字符串引用它（没有外键），且已签发
// 的会话里刻着它，改了那批用户会在会话刷新前丢掉这个角色。这条约束是选择
// user_role.roles text[] 的直接后果：哪天它改成按 role_id 的关系表，key 也就
// 能改了。
func (s *AuthzService) UpdateRole(ctx context.Context, id uuid.UUID, name string, parentID *uuid.UUID) (*domain.Role, error) {
	if name == "" {
		return nil, domain.Fail(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "角色 name 不能为空")
	}
	if parentID != nil && *parentID == id {
		return nil, domain.Fail(domain.ErrInvalidArgument, domain.CodeRoleCycle, "角色不能以自己为父角色")
	}
	if parentID != nil {
		cyclic, err := s.wouldCycle(ctx, id, *parentID)
		if err != nil {
			return nil, err
		}
		if cyclic {
			return nil, domain.Fail(domain.ErrInvalidArgument, domain.CodeRoleCycle, "角色继承会形成环")
		}
	}
	row := s.pool.QueryRow(ctx, `
		UPDATE role SET name = $2, parent_id = $3, updated_at = now()
		WHERE id = $1 RETURNING `+roleColumns, id, name, parentID)
	r, err := scanRole(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Fail(domain.ErrNotFound, domain.CodeRoleNotFound, "角色不存在")
	}
	if err != nil {
		return nil, fmt.Errorf("service: 更新角色: %w", err)
	}
	return r, nil
}

// wouldCycle 报告把 parentID 设为 id 的父角色是否会形成环。
//
// 从候选父角色往上走，看会不会走回 id。不做这个检查的话，A→B→A 这种环会让
// 展开继承时无限递归——而那是在**推送策略**的时候才炸，届时整个应用的鉴权
// 一起挂掉，且错误现场离操作现场很远。
func (s *AuthzService) wouldCycle(ctx context.Context, id, parentID uuid.UUID) (bool, error) {
	cur := &parentID
	// 深度上限兜底：库里已有环时不至于在这里死循环。
	for i := 0; i < 64 && cur != nil; i++ {
		if *cur == id {
			return true, nil
		}
		var next *uuid.UUID
		err := s.pool.QueryRow(ctx, `SELECT parent_id FROM role WHERE id = $1`, *cur).Scan(&next)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("service: 检查角色环: %w", err)
		}
		cur = next
	}
	return false, nil
}

// DeleteRole 删除角色，并从所有用户的角色数组里摘掉它。
//
// **这条连带清理是必须的，也是 user_role 用 text[] 的代价。** 数组没有外键，
// 数据库不会替我们做级联；漏了这一步的话，被删角色的 key 会静默残留在
// user_role.roles 里，没有任何报错——只是那些用户带着一个不存在的角色，
// 在策略表里查不到、等同于没有，直到有人建了一个同名角色，他们**突然获得
// 了那个角色的权限**。
//
// role_permission 由数据库的 ON DELETE CASCADE 负责。
func (s *AuthzService) DeleteRole(ctx context.Context, id uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("service: 开启事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var key string
	if err := tx.QueryRow(ctx, `DELETE FROM role WHERE id = $1 RETURNING key`, id).Scan(&key); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Fail(domain.ErrNotFound, domain.CodeRoleNotFound, "角色不存在")
		}
		return fmt.Errorf("service: 删除角色: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE user_role SET roles = array_remove(roles, $1), updated_at = now()
		WHERE roles @> ARRAY[$1::text]`, key); err != nil {
		return fmt.Errorf("service: 清理用户角色: %w", err)
	}
	// 把默认角色指向它的应用也要清掉，否则那些应用的用户会拿到一个不存在的角色。
	if _, err := tx.Exec(ctx,
		`UPDATE application SET default_role_key = '' WHERE default_role_key = $1`, key); err != nil {
		return fmt.Errorf("service: 清理应用默认角色: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("service: 提交删除角色: %w", err)
	}
	return nil
}

// ListRoles 返回全部角色。角色是全局的，数量在几十级别，不分页。
func (s *AuthzService) ListRoles(ctx context.Context) ([]domain.Role, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+roleColumns+` FROM role ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("service: 查询角色列表: %w", err)
	}
	defer rows.Close()

	out := []domain.Role{}
	for rows.Next() {
		r, err := scanRole(rows)
		if err != nil {
			return nil, fmt.Errorf("service: 扫描角色: %w", err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// 用户 → 角色
// ---------------------------------------------------------------------------

// SetUserRoles 设置某人的全局角色。传空切片等于清空（回落到各应用的默认角色）。
func (s *AuthzService) SetUserRoles(ctx context.Context, userID uuid.UUID, roles []string) error {
	if len(roles) == 0 {
		if _, err := s.pool.Exec(ctx, `DELETE FROM user_role WHERE user_id = $1`, userID); err != nil {
			return fmt.Errorf("service: 清空用户角色: %w", err)
		}
		return nil
	}
	// 校验角色都存在：允许写入不存在的角色 key 等于给用户一个永远不生效的
	// 角色，而它会在有人建同名角色的那天突然生效。
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM role WHERE key = ANY($1)`, roles).Scan(&n); err != nil {
		return fmt.Errorf("service: 校验角色存在性: %w", err)
	}
	if n != len(dedup(roles)) {
		return domain.Fail(domain.ErrInvalidArgument, domain.CodeRoleNotFound, "包含不存在的角色")
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO user_role (user_id, roles) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET roles = EXCLUDED.roles, updated_at = now()`,
		userID, dedup(roles)); err != nil {
		return fmt.Errorf("service: 写入用户角色: %w", err)
	}
	return nil
}

// UserRoles 返回某人的全局角色。没有行时返回空切片。
func (s *AuthzService) UserRoles(ctx context.Context, userID uuid.UUID) ([]string, error) {
	var roles []string
	err := s.pool.QueryRow(ctx, `SELECT roles FROM user_role WHERE user_id = $1`, userID).Scan(&roles)
	if errors.Is(err, pgx.ErrNoRows) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("service: 查询用户角色: %w", err)
	}
	return roles, nil
}

// EffectiveRoles 返回某人在某个应用下的有效角色：**全局角色 ∪ 该应用的默认角色**。
//
// 是并集而不是"没有显式角色才用默认"：角色是全局的，若改成后者，给某人加一个
// 「商城管理员」会让他在视频里只剩这一个角色，而它在视频没有任何权限——顺手
// 把他看视频的能力弄没了。
//
// 在**签发会话时**调用一次，结果刻进会话；判定路径不再碰数据库。
func (s *AuthzService) EffectiveRoles(ctx context.Context, userID, appID uuid.UUID) ([]string, error) {
	roles, err := s.UserRoles(ctx, userID)
	if err != nil {
		return nil, err
	}
	var def string
	if err := s.pool.QueryRow(ctx,
		`SELECT default_role_key FROM application WHERE id = $1`, appID).Scan(&def); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("service: 查询应用默认角色: %w", err)
		}
	}
	if def != "" {
		roles = append(roles, def)
	}
	return dedup(roles), nil
}

// dedup 去重且保持顺序。全局角色与应用默认角色可能重复（显式分配的正好就是默认角色）。
func dedup(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

const roleColumns = `id, key, name, parent_id,
	(extract(epoch from created_at) * 1000)::bigint,
	(extract(epoch from updated_at) * 1000)::bigint`

func scanRole(row rowScanner) (*domain.Role, error) {
	var r domain.Role
	if err := row.Scan(&r.ID, &r.Key, &r.Name, &r.ParentID, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

// isUniqueViolation 报告错误是否为 PostgreSQL 的唯一约束冲突。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}
