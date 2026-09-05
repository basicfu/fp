package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/basicfu/fp/internal/domain"
)

// ReportedPoint 是 SDK 上报的一条权限点。
type ReportedPoint struct {
	Key  string
	Kind string
	// Parent 是父权限点的 **key**（SDK 不知道 uuid）。本期恒为空。
	Parent string
	Name   string
}

// ReportPermissions 处理一次全量快照上报。
//
// 四条规则：
//
//  1. 快照里的每一条 → 存在则刷新 last_seen_at；不存在则新建（source='app'）
//  2. **快照里没有的一律不动**——状态是算出来的，不需要写库。"消失"的判据是
//     "多久没有任何实例报过它"，不是"这次上报里有没有"：业务服务多半多实例，
//     滚动发布时新旧版本同时在跑，交替上报会让权限点在两个状态间反复横跳
//  3. source='manual' 的永远不被触碰——业务方的路由清单里当然没有人手动加的
//  4. name 与 parent 只在**首次创建时**由上报写入，之后不覆盖。否则每次重启，
//     运营填的中文名和整理好的归类就没了
func (s *AuthzService) ReportPermissions(ctx context.Context, appID uuid.UUID, points []ReportedPoint) error {
	if len(points) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("service: 开启事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, p := range points {
		if p.Key == "" {
			return domain.Fail(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "权限点 key 不能为空")
		}
		kind := p.Kind
		if kind == "" {
			kind = domain.PermissionKindAPI
		}
		// ON CONFLICT 只更新 last_seen_at：name 与 parent_id 是人维护的，
		// 上报不许覆盖（规则 4）。source 也不改——manual 的权限点即使
		// 恰好与上报的 key 重名，也不会被降级成 app（规则 3）。
		if _, err := tx.Exec(ctx, `
			INSERT INTO permission (application_id, key, name, kind, source, last_seen_at)
			VALUES ($1, $2, $3, $4, $5, now())
			ON CONFLICT (application_id, key)
			DO UPDATE SET last_seen_at = now()`,
			appID, p.Key, p.Name, kind, domain.PermissionSourceApp); err != nil {
			return fmt.Errorf("service: 写入权限点: %w", err)
		}
	}

	// 父子关系单独一轮：首轮结束后所有节点都已存在，按 key 解析 parent 才不会
	// 因为父节点排在子节点后面而失败。解析不到就留空——不因为一个父节点没上报
	// 而让整批失败。
	for _, p := range points {
		if p.Parent == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE permission SET parent_id = parent.id
			FROM permission parent
			WHERE permission.application_id = $1 AND permission.key = $2
			  AND parent.application_id = $1 AND parent.key = $3
			  AND permission.parent_id IS NULL`,
			appID, p.Key, p.Parent); err != nil {
			return fmt.Errorf("service: 解析权限点父节点: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("service: 提交权限点上报: %w", err)
	}
	return nil
}

// CreatePermission 手动新建一个权限点。
func (s *AuthzService) CreatePermission(ctx context.Context, appID uuid.UUID, key, name, kind string) (*domain.Permission, error) {
	if key == "" {
		return nil, domain.Fail(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "权限点 key 不能为空")
	}
	if kind == "" {
		kind = domain.PermissionKindAPI
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO permission (application_id, key, name, kind, source)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+permissionColumns, appID, key, name, kind, domain.PermissionSourceManual)
	p, err := scanPermission(row)
	if isUniqueViolation(err) {
		return nil, domain.Failf(domain.ErrConflict, domain.CodePermissionKeyTaken, "权限点 %q 已存在", key)
	}
	if err != nil {
		return nil, fmt.Errorf("service: 创建权限点: %w", err)
	}
	return p, nil
}

// UpdatePermission 改权限点的 key 与显示名。
//
// **key 可以改**——role_permission 按 permission_id 引用，授权关系自动跟随；
// 会话里也不含权限点 key。改完推一次 PolicyChanged 让 SDK 重拉即可。
//
// 改完之后下次上报会如实反映代码：代码里的路由确实是新值就匹配上并刷新
// last_seen_at；代码里还是旧值就把旧的重新建出来（那个路由真的存在，在控制台
// 改成代码不提供的路径本来就是撒谎）。
func (s *AuthzService) UpdatePermission(ctx context.Context, id uuid.UUID, key, name string) (*domain.Permission, error) {
	if key == "" {
		return nil, domain.Fail(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "权限点 key 不能为空")
	}
	row := s.pool.QueryRow(ctx, `
		UPDATE permission SET key = $2, name = $3 WHERE id = $1
		RETURNING `+permissionColumns, id, key, name)
	p, err := scanPermission(row)
	if isUniqueViolation(err) {
		return nil, domain.Failf(domain.ErrConflict, domain.CodePermissionKeyTaken, "权限点 %q 已存在", key)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Fail(domain.ErrNotFound, domain.CodePermissionNotFound, "权限点不存在")
	}
	if err != nil {
		return nil, fmt.Errorf("service: 更新权限点: %w", err)
	}
	return p, nil
}

// DeletePermission 删除权限点。授权关系由数据库的 ON DELETE CASCADE 连带删除。
//
// 调用方（控制台）在删除前应当先用 RolesHolding 提示"当前有 N 个角色持有它"
// ——这是唯一的安全网，因为设计上没有可逆的"停用"中间态。
func (s *AuthzService) DeletePermission(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM permission WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("service: 删除权限点: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.Fail(domain.ErrNotFound, domain.CodePermissionNotFound, "权限点不存在")
	}
	return nil
}

// RolesHolding 返回持有某个权限点的角色 key，供删除前的提示使用。
func (s *AuthzService) RolesHolding(ctx context.Context, permissionID uuid.UUID) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.key FROM role_permission rp
		JOIN role r ON r.id = rp.role_id
		WHERE rp.permission_id = $1 ORDER BY r.key`, permissionID)
	if err != nil {
		return nil, fmt.Errorf("service: 查询持有该权限点的角色: %w", err)
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("service: 扫描角色: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// PermissionListItem 是控制台列表里的一行：权限点加上算出来的状态。
type PermissionListItem struct {
	Permission domain.Permission
	Status     domain.PermissionStatus
	// StaleFor 是"过渡中"已经持续了多久。其余状态为 0。
	StaleFor time.Duration
}

// ListPermissions 返回某个应用的全部权限点，带算出来的状态。
func (s *AuthzService) ListPermissions(ctx context.Context, appID uuid.UUID) ([]PermissionListItem, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+permissionColumns+` FROM permission WHERE application_id = $1 ORDER BY key`, appID)
	if err != nil {
		return nil, fmt.Errorf("service: 查询权限点列表: %w", err)
	}
	defer rows.Close()

	now := time.Now()
	out := []PermissionListItem{}
	for rows.Next() {
		p, err := scanPermission(rows)
		if err != nil {
			return nil, fmt.Errorf("service: 扫描权限点: %w", err)
		}
		item := PermissionListItem{Permission: *p, Status: p.Status(now, s.staleAfter)}
		if item.Status == domain.PermissionStatusStale && p.LastSeenAt > 0 {
			item.StaleFor = now.Sub(time.UnixMilli(p.LastSeenAt))
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// SetRolePermission 给角色授予或收回一个权限点。effect 为空表示收回。
func (s *AuthzService) SetRolePermission(ctx context.Context, roleID, permissionID uuid.UUID, effect string) error {
	if effect == "" {
		if _, err := s.pool.Exec(ctx,
			`DELETE FROM role_permission WHERE role_id = $1 AND permission_id = $2`,
			roleID, permissionID); err != nil {
			return fmt.Errorf("service: 收回权限: %w", err)
		}
		return nil
	}
	if effect != domain.EffectAllow && effect != domain.EffectDeny {
		return domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument,
			"未知的授权效果 %q，只接受 %s 或 %s", effect, domain.EffectAllow, domain.EffectDeny)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO role_permission (role_id, permission_id, effect) VALUES ($1, $2, $3)
		ON CONFLICT (role_id, permission_id) DO UPDATE SET effect = EXCLUDED.effect`,
		roleID, permissionID, effect); err != nil {
		return fmt.Errorf("service: 授予权限: %w", err)
	}
	return nil
}

const permissionColumns = `id, application_id, key, name, kind, parent_id, source,
	coalesce((extract(epoch from last_seen_at) * 1000)::bigint, 0),
	(extract(epoch from created_at) * 1000)::bigint`

func scanPermission(row rowScanner) (*domain.Permission, error) {
	var p domain.Permission
	if err := row.Scan(&p.ID, &p.ApplicationID, &p.Key, &p.Name, &p.Kind,
		&p.ParentID, &p.Source, &p.LastSeenAt, &p.CreatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}

// RoleGrant 是一条授权关系：某个角色对某个权限点的效果。
type RoleGrant struct {
	PermissionID uuid.UUID
	Effect       string
}

// RoleGrants 返回某个角色**直接**持有的授权关系。
//
// 刻意不展开继承：控制台的授权编辑器要改的是这个角色自己的那一条，
// 展开之后人会以为能取消从父角色继承来的授权，而实际取消不了（那条
// role_permission 属于父角色）。继承的效果由 CompilePolicy 在推送时算，
// 展示继承来的权限是另一件事，需要单独标注来源，本期不做。
func (s *AuthzService) RoleGrants(ctx context.Context, roleID uuid.UUID) ([]RoleGrant, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT permission_id, effect FROM role_permission WHERE role_id = $1`, roleID)
	if err != nil {
		return nil, fmt.Errorf("service: 查询角色的授权关系: %w", err)
	}
	defer rows.Close()

	out := []RoleGrant{}
	for rows.Next() {
		var g RoleGrant
		if err := rows.Scan(&g.PermissionID, &g.Effect); err != nil {
			return nil, fmt.Errorf("service: 扫描授权关系: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
