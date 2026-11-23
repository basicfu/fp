package model

import "github.com/basicfu/fp/internal/cluster"

// ClusterKeys 生成节点存活表 fp:im:node、服务流登记 fp:im:srv:{app}、节点频道 fp:im:node:{id}。
// 前缀一旦改了，滚动发布期间新旧节点就互相看不见。用函数而非包级变量：谁都改不动它。
func ClusterKeys() cluster.Keys { return cluster.Keys{Prefix: "fp:im"} }

// ConnKey 是某 subject 的连接表。花括号是 Cluster 的 hash tag：
// 同一 subject 的所有操作落在同一槽，脚本才能合法地只碰这一个 key。
func ConnKey(app, subject string) string { return "fp:im:{" + app + ":" + subject + "}:conn" }

// GroupIdxKey 是某分组在某节点上的在线索引：set，成员是裸 uid。
// 每个节点只写自己那份，key 里带节点 id；hash tag 包住整段，每条命令只碰一个 key。
func GroupIdxKey(app, group, node string) string {
	return "fp:im:{" + app + ":grp:" + group + ":" + node + "}:idx"
}
