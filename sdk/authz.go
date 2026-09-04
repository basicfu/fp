package fpsdk

import (
	"context"
	"errors"
	"sync"

	"github.com/basicfu/fp/sdk/authzcore"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// ErrPolicyUnavailable 表示本地还没有可用的策略快照。
//
// 与"没权限"是两回事：前者是**没能判定**，业务方可能要降级、告警、或按
// 自己的策略放行内部接口；后者是明确拒绝、该回 403。Allow 用
// (bool, error) 两个返回值把它们分开，正是为了这个区别。
var ErrPolicyUnavailable = errors.New("fpsdk: 本地策略尚未就绪")

// ErrNoIdentity 表示 context 里没有已认证的身份——通常是认证中间件没挂上。
//
// 与"没权限"分开：回 403 会让调用方以为是权限配置问题，而真实原因是装配错误。
var ErrNoIdentity = errors.New("fpsdk: context 中没有已认证的身份")

// Authz 是 SDK 的鉴权入口。
//
// 判定完全在本地完成、不走网络：策略快照由 fp 推送并缓存在进程内，用户的
// 角色随 token 校验一起回来。这是设计原则 2（不走网络）的落地——若每次
// 鉴权都调一次 fp，每个请求会多一次往返，且 fp 挂则所有业务方全挂。
type Authz struct {
	mu     sync.RWMutex
	policy *fpv1.AppPolicy
}

// Allow 判定当前请求的用户能否对 (method, pattern) 执行操作。
//
// 用户的角色从 context 里的身份取——认证中间件已经把它放在那里了，角色
// 又是随身份一起从 fp 回来的，所以这里不需要任何额外查询，也不需要调用方
// 再传一遍 userID（传了反而可能与实际认证到的身份不一致）。
//
// pattern 必须是**匹配到的路由模式**（"/orders/{id}"）而不是原始 URL
// （"/orders/123"），否则每个 id 都会变成一个不同的权限点。框架适配器
// 负责取出它；直接调用时由调用方负责传对。
//
// 返回值的两种失败要分清：
//
//	false, nil  明确拒绝 → 业务方回 403，照常运行
//	_,     err  没能判定 → 可用性事件，按业务方自己的降级策略处理
func (a *Authz) Allow(ctx context.Context, method, pattern string) (bool, error) {
	id, ok := IdentityFrom(ctx)
	if !ok {
		// 没有身份说明没过认证中间件。这是"没能判定"而不是"没权限"——
		// 回 403 会让调用方以为是权限配置问题，而真实原因是中间件没挂对。
		return false, ErrNoIdentity
	}
	return a.AllowRoles(id.Roles, method, pattern)
}

// AllowRoles 用显式给出的角色做判定，供不使用 fpsdk 认证中间件的调用方使用。
func (a *Authz) AllowRoles(roles []string, method, pattern string) (bool, error) {
	a.mu.RLock()
	pol := a.policy
	a.mu.RUnlock()

	if pol == nil {
		return false, ErrPolicyUnavailable
	}
	// 生成类型转成 authzcore 的纯结构：判定逻辑不绑定 proto 的演进。
	rp := make([]authzcore.RolePolicy, 0, len(pol.GetRoles()))
	for _, r := range pol.GetRoles() {
		rp = append(rp, authzcore.RolePolicy{RoleKey: r.GetRoleKey(), Allow: r.GetAllow(), Deny: r.GetDeny()})
	}
	return authzcore.Allow(rp, roles, authzcore.PermissionKey(method, pattern)), nil
}

// setPolicy 替换本地策略快照。由策略推送与启动时的全量拉取调用。
func (a *Authz) setPolicy(p *fpv1.AppPolicy) {
	a.mu.Lock()
	a.policy = p
	a.mu.Unlock()
}

// PolicyVersion 返回当前本地策略的版本号，0 表示尚未就绪。供可观测使用。
func (a *Authz) PolicyVersion() int64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.policy == nil {
		return 0
	}
	return a.policy.Version
}

// PermissionKindAPI 是本期唯一启用的权限点种类。menu / button 留给以后。
const PermissionKindAPI = "api"

// PermissionPoint 是上报给 fp 的一条权限点。
type PermissionPoint struct {
	// Key 形如 "GET:/orders/{id}"。用 PermissionKey 构造，不要自己拼字符串。
	Key string
	// Kind 为空时按 api 处理。
	Kind string
	// Parent 是父权限点的 key。本期恒为空。
	Parent string
	// Name 是显示名，可选。只在权限点首次创建时写入，之后由人在控制台维护，
	// 后续上报不覆盖。
	Name string
}

// PermissionKey 拼出权限点的 key。
//
// 上报与判定必须用同一个函数而不是各自拼字符串：格式一旦漂移，鉴权会
// **静默地全部拒绝**（本地查不到对应条目就是默认拒绝），没有任何报错
// 指向真实原因。
func PermissionKey(method, pattern string) string {
	return authzcore.PermissionKey(method, pattern)
}
