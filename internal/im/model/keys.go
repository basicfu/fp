package model

// KeyNodes 是节点存活表：hash nodeId → 心跳毫秒时间戳。
const KeyNodes = "fp:im:node"

// SrvKey 是"哪些节点持有该 app 的 server 流"表：hash nodeId → 心跳毫秒时间戳。
func SrvKey(app string) string { return "fp:im:srv:" + app }

// ConnKey 是某 subject 的连接表。花括号是 Cluster 的 hash tag：
// 同一 subject 的所有操作落在同一槽，脚本才能合法地只碰这一个 key。
func ConnKey(app, subject string) string { return "fp:im:{" + app + ":" + subject + "}:conn" }

// GroupIdxKey 是某分组在某节点上的在线索引：set，成员是裸 uid。
// 每个节点只写自己那份，key 里带节点 id；hash tag 包住整段，每条命令只碰一个 key。
func GroupIdxKey(app, group, node string) string {
	return "fp:im:{" + app + ":grp:" + group + ":" + node + "}:idx"
}

// NodeChannel 是节点私有的 sharded pub/sub 频道，节点启动时订阅一次。
func NodeChannel(nodeID string) string { return "fp:im:node:" + nodeID }
