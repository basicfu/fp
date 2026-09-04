package domain

import (
	"time"

	"github.com/google/uuid"
)

// 权限点的来源。
const (
	// PermissionSourceApp 表示由业务方 SDK 启动时上报。
	PermissionSourceApp = "app"
	// PermissionSourceManual 表示人在控制台手动添加。
	// 上报**永远不触碰**这一类——业务方的路由清单里当然没有人手动加的东西。
	PermissionSourceManual = "manual"
)

// 权限点的种类。本期只启用 api；menu 与 button 留着不启用，见迁移里的注释。
const (
	PermissionKindAPI    = "api"
	PermissionKindMenu   = "menu"
	PermissionKindButton = "button"
)

// 授权效果。deny-override：一个角色集合里只要有一条 deny 就拒。
const (
	EffectAllow = "allow"
	EffectDeny  = "deny"
)

// PermissionStatus 是权限点在控制台上的展示状态。
//
// **不存库，由 Source 与 LastSeenAt 算出来**——存下来的话就要有人负责在
// 每次上报后更新它，而"忘了更新"不会有任何报错。
type PermissionStatus string

const (
	// PermissionStatusNormal：app 上报的，最近还在报。
	PermissionStatusNormal PermissionStatus = "normal"
	// PermissionStatusStale：app 上报的，超过阈值没有任何实例报过它。
	//
	// 判据刻意是"多久没被报过"而不是"这次上报里有没有"：业务服务多半多实例，
	// 滚动发布时新旧版本同时在跑，交替上报会让权限点在两个状态间反复横跳。
	PermissionStatusStale PermissionStatus = "stale"
	// PermissionStatusManual：人手动加的，不参与消失判定。
	PermissionStatusManual PermissionStatus = "manual"
)

// DefaultStaleAfter 是判定"过渡中"的默认阈值。
//
// 30 分钟：滚动发布通常几分钟内完成，窗口内新旧两个版本都在报，不会误判。
const DefaultStaleAfter = 30 * time.Minute

// Role 是一个全局角色。
//
// 不带 application_id：角色的应用归属由它挂了哪些应用的权限点决定。
// 应用专属的角色靠命名约定区分（商城管理员 / 视频管理员）。
type Role struct {
	ID uuid.UUID
	// Key 是身份，不可修改。理由见 00007 迁移里的注释。
	Key  string
	Name string
	// ParentID 是继承的父角色。子角色拥有父角色的全部权限，展开在服务端完成。
	ParentID  *uuid.UUID
	CreatedAt int64
	UpdatedAt int64
}

// Permission 是一个权限点。
type Permission struct {
	ID            uuid.UUID
	ApplicationID uuid.UUID
	// Key 形如 "GET:/orders/{id}"。可以修改——role_permission 按 ID 引用。
	Key      string
	Name     string
	Kind     string
	ParentID *uuid.UUID
	Source   string
	// LastSeenAt 为 0 表示从未被上报过（manual 的权限点）。
	LastSeenAt int64
	CreatedAt  int64
}

// Status 按当前时刻算出展示状态。
func (p Permission) Status(now time.Time, staleAfter time.Duration) PermissionStatus {
	if p.Source == PermissionSourceManual {
		return PermissionStatusManual
	}
	if p.LastSeenAt == 0 {
		return PermissionStatusStale
	}
	seen := time.UnixMilli(p.LastSeenAt)
	if now.Sub(seen) > staleAfter {
		return PermissionStatusStale
	}
	return PermissionStatusNormal
}

// 策略类型（RolePolicy / AppPolicy）与判定逻辑定义在 sdk/gen/fp/v1：
// 它们是线上类型，且必须由服务端与 SDK 共用同一份实现——两边各写一份的话
// 迟早漂移，而漂移在鉴权上的表现是线上某些请求两边判断不一致，极难复现。
// sdk/ 不得 import internal/（见 sdk/arch_test.go），所以唯一能同时被两边
// 引用的位置就是那个生成类型所在的包。
