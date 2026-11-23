// Package cluster 是多节点共用的跨实例基础件：节点心跳与服务流登记、节点私有频道、
// 稳定选节点（HRW）、Redis 写管道。Redis 键前缀由调用方经 Keys 传入，本包不依赖任何业务包。
package cluster

// Keys 生成同一组集群键。fp-im 用 "fp:im"、fp 核心用 "fp:core"，前缀不同键就不重叠；
// 生产上 fp-im 已有数据，所以拼接格式不能变。
type Keys struct{ Prefix string }

// Nodes 是节点存活表：hash nodeId → 心跳毫秒时间戳。
func (k Keys) Nodes() string { return k.Prefix + ":node" }

// Srv 是"哪些节点持有该 app 的 server 流"表：hash nodeId → 心跳毫秒时间戳。
func (k Keys) Srv(app string) string { return k.Prefix + ":srv:" + app }

// NodeChannel 是节点私有的 sharded pub/sub 频道，节点启动时订阅一次。
func (k Keys) NodeChannel(node string) string { return k.Prefix + ":node:" + node }
