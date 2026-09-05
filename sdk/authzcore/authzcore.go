// Package authzcore 是授权判定的**唯一实现**，由服务端与 SDK 共用。
//
// 为什么单独一个包：判定逻辑必须两边共用同一份，否则迟早漂移——而漂移在
// 鉴权上的表现是线上某些请求两边判断不一致，极难复现。而 sdk/ 不得 import
// internal/（见 sdk/arch_test.go），internal/ 也不宜 import sdk 的业务包，
// sdk/gen 又是 buf 的 clean 目标（放手写文件会被下一次 ./scripts/gen.sh
// 删掉）。所以需要一个两边都能引用、且不属于生成产物的位置。
//
// 本包**不依赖任何生成类型**，只吃基本类型——这样它既不绑定 proto 的演进，
// 也让穷举测试不需要构造 protobuf 消息。
package authzcore

// RolePolicy 是一个角色的隐式权限全集（角色继承已在服务端展开）。
type RolePolicy struct {
	RoleKey string
	Allow   []string
	Deny    []string
}

// PermissionKey 拼出权限点的 key。
//
// 上报、服务端存储、SDK 判定三处必须用同一个函数而不是各自拼字符串：格式
// 一旦漂移，鉴权会**静默地全部拒绝**（本地查不到对应条目就是默认拒绝），
// 而没有任何报错指向真实原因。
func PermissionKey(method, pattern string) string {
	return method + ":" + pattern
}

// Allow 判定一组角色对某个权限点是否放行。
//
// 规则：**有 deny 即拒，有 allow 即过，都没有则拒**——默认拒绝 + deny-override。
//
// 刻意做成不依赖任何外部状态的纯函数：这是整个授权模块唯一有真实逻辑的
// 地方，纯函数才能穷举测试。
func Allow(roles []RolePolicy, roleKeys []string, permissionKey string) bool {
	return Compile(roles).Allow(roleKeys, permissionKey)
}

// Snapshot 是编译好的策略。判定时**零分配**：角色与权限点都进了 map。
//
// 存在的理由是热路径的形状：判定每个请求跑一次，而策略几分钟才换一次。
// 早先 SDK 每次判定都把整份策略从 proto 转成 []RolePolicy 再线性扫描，
// 开销随**整份策略**的大小涨（50 角色 × 500 权限时是 3.9µs / 3.4KB
// 每请求），而不是随这个用户持有的角色数涨。编译一次之后，判定只跟
// 用户持有几个角色有关。
type Snapshot struct {
	roles map[string]roleSets
}

type roleSets struct {
	allow map[string]struct{}
	deny  map[string]struct{}
}

// Compile 把角色列表编译成可反复判定的快照。策略更新时调一次。
func Compile(roles []RolePolicy) *Snapshot {
	s := &Snapshot{roles: make(map[string]roleSets, len(roles))}
	for _, rp := range roles {
		// 同一个 roleKey 出现多次时合并，不是后者覆盖前者——覆盖会悄悄
		// 丢掉前一条里的 deny，而 deny 丢失是放行，不是拒绝。
		sets, ok := s.roles[rp.RoleKey]
		if !ok {
			sets = roleSets{allow: map[string]struct{}{}, deny: map[string]struct{}{}}
		}
		for _, a := range rp.Allow {
			sets.allow[a] = struct{}{}
		}
		for _, d := range rp.Deny {
			sets.deny[d] = struct{}{}
		}
		s.roles[rp.RoleKey] = sets
	}
	return s
}

// Allow 判定持有 roleKeys 的用户能否使用 permissionKey。
//
// 规则与 Compile 之前完全一致：**有 deny 即拒，有 allow 即过，都没有则拒**。
// nil 快照一律拒绝——调用方应当在此之前就用"策略未就绪"把请求拦下，
// 这里兜底成默认拒绝而不是放行。
func (s *Snapshot) Allow(roleKeys []string, permissionKey string) bool {
	if s == nil {
		return false
	}
	allowed := false
	for _, k := range roleKeys {
		sets, ok := s.roles[k]
		if !ok {
			continue
		}
		if _, denied := sets.deny[permissionKey]; denied {
			// deny 一票否决，不必再看其余角色。
			return false
		}
		if _, ok := sets.allow[permissionKey]; ok {
			allowed = true
		}
	}
	// 不能在找到 allow 时提前 return：后面的角色可能带 deny，而 deny-override
	// 要求 deny 胜出。必须把持有的角色全部看完。
	return allowed
}
