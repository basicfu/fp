package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
)

// CompilePolicy 编译某个应用的完整策略快照，供推送给该应用的 SDK。
//
// 产物是每个角色的**隐式权限全集**——继承已经展开，SDK 拿到的是一张扁平表，
// 判定退化成纯 map 查找、不依赖 casbin。这么做的三个理由：
//
//  1. SDK 是业务方要 import 的库，不给它塞 casbin 及其传递依赖
//  2. 判定变成纯函数，可以穷举测试
//  3. 服务端保留完整表达力——以后加数据范围时改的是服务端，**不动已发布的 SDK**
//
// **只包含在该应用有权限点的角色。** 用户带着「商城管理员」去视频时，那个角色
// 在视频的策略表里根本不存在，等同于没有——这正是"角色全局、应用归属由权限点
// 决定"这个设计能成立的原因。
func (s *AuthzService) CompilePolicy(ctx context.Context, appID uuid.UUID) (*domain.AppPolicy, error) {
	parents, keys, err := s.roleGraph(ctx)
	if err != nil {
		return nil, err
	}

	// 直接授权：role_id → (permissionKey → effect)。只取本应用的权限点。
	direct := map[uuid.UUID]map[string]string{}
	rows, err := s.pool.Query(ctx, `
		SELECT rp.role_id, p.key, rp.effect
		FROM role_permission rp
		JOIN permission p ON p.id = rp.permission_id
		WHERE p.application_id = $1`, appID)
	if err != nil {
		return nil, fmt.Errorf("service: 查询角色权限: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var roleID uuid.UUID
		var key, effect string
		if err := rows.Scan(&roleID, &key, &effect); err != nil {
			return nil, fmt.Errorf("service: 扫描角色权限: %w", err)
		}
		if direct[roleID] == nil {
			direct[roleID] = map[string]string{}
		}
		direct[roleID][key] = effect
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service: 遍历角色权限: %w", err)
	}

	out := &domain.AppPolicy{ApplicationID: appID}
	for roleID, key := range keys {
		eff := effectiveFor(roleID, parents, direct)
		if len(eff) == 0 {
			// 在本应用没有任何权限的角色不进策略表——它在这个应用里不存在。
			continue
		}
		rp := domain.RolePolicy{RoleKey: key}
		for pk, e := range eff {
			if e == domain.EffectDeny {
				rp.Deny = append(rp.Deny, pk)
			} else {
				rp.Allow = append(rp.Allow, pk)
			}
		}
		out.Roles = append(out.Roles, rp)
	}
	return out, nil
}

// roleGraph 读出全部角色的父子关系与 key。
func (s *AuthzService) roleGraph(ctx context.Context) (parents map[uuid.UUID]*uuid.UUID, keys map[uuid.UUID]string, err error) {
	rows, qerr := s.pool.Query(ctx, `SELECT id, key, parent_id FROM role`)
	if qerr != nil {
		return nil, nil, fmt.Errorf("service: 查询角色图: %w", qerr)
	}
	defer rows.Close()

	parents = map[uuid.UUID]*uuid.UUID{}
	keys = map[uuid.UUID]string{}
	for rows.Next() {
		var id uuid.UUID
		var key string
		var parent *uuid.UUID
		if err := rows.Scan(&id, &key, &parent); err != nil {
			return nil, nil, fmt.Errorf("service: 扫描角色图: %w", err)
		}
		parents[id] = parent
		keys[id] = key
	}
	return parents, keys, rows.Err()
}

// effectiveFor 展开一个角色的隐式权限：自己的 ∪ 祖先的。
//
// **子角色的直接授权优先于祖先的**——「订单管理员」继承自「普通用户」，若它
// 显式 deny 了某条普通用户 allow 的权限，以子为准。否则"用继承表达基线、用
// deny 做减法"这个用法就不成立了。
//
// 深度上限 64 是兜底：CreateRole/UpdateRole 已经拦下了成环，这里防的是库里
// 已有脏数据的情况——宁可少展开几层，也不能让编译策略这一步无限递归，
// 那会让整个应用的鉴权一起挂掉。
func effectiveFor(roleID uuid.UUID, parents map[uuid.UUID]*uuid.UUID, direct map[uuid.UUID]map[string]string) map[string]string {
	out := map[string]string{}
	cur := &roleID
	for depth := 0; depth < 64 && cur != nil; depth++ {
		for k, e := range direct[*cur] {
			// 越靠近自己的越优先：先写入的（自己的）不被祖先覆盖。
			if _, ok := out[k]; !ok {
				out[k] = e
			}
		}
		cur = parents[*cur]
	}
	return out
}

// PolicyVersion 返回某个应用当前策略的版本号。
//
// 用"最近一次相关改动的时间戳"而不是自增列：策略由四张表共同决定
// （role / role_permission / permission / application.default_role_key），
// 维护一个自增列意味着每处写入都要记得 bump 它，漏一处就会让 SDK 拿着
// 陈旧策略而毫不知情。取最大更新时间是同一件事的自然表达，不需要谁"记得"。
func (s *AuthzService) PolicyVersion(ctx context.Context, appID uuid.UUID) (int64, error) {
	var v int64
	err := s.pool.QueryRow(ctx, `
		SELECT coalesce(max(t), 0) FROM (
			SELECT (extract(epoch from updated_at) * 1000)::bigint AS t FROM role
			UNION ALL
			SELECT (extract(epoch from created_at) * 1000)::bigint FROM permission WHERE application_id = $1
		) x`, appID).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("service: 查询策略版本: %w", err)
	}
	return v, nil
}
