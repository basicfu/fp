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
	held := make(map[string]bool, len(roleKeys))
	for _, k := range roleKeys {
		held[k] = true
	}

	allowed := false
	for _, rp := range roles {
		if !held[rp.RoleKey] {
			continue
		}
		for _, d := range rp.Deny {
			if d == permissionKey {
				// deny 一票否决，不必再看其余角色。
				return false
			}
		}
		for _, al := range rp.Allow {
			if al == permissionKey {
				allowed = true
			}
		}
	}
	// 不能在找到 allow 时提前 return：后面的角色可能带 deny，而 deny-override
	// 要求 deny 胜出。必须把持有的角色全部看完。
	return allowed
}
