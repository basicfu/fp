// Package rendezvous 实现最高随机权重哈希（HRW）。
package rendezvous

import (
	"hash/fnv"
	"sort"
)

// Pick 在 nodes 中为 key 选出权重最高的节点。
// 用 fnv-1a 而不是 xxhash：这里每秒几万次 64 字节输入，fnv 的差距在纳秒级，
// 而 xxhash 是间接依赖，直接 import 要过依赖白名单。
func Pick(nodes []string, key string) (string, bool) {
	var (
		best   string
		bestW  uint64
		picked bool
	)
	for _, n := range nodes {
		h := fnv.New64a()
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write([]byte(key))
		w := h.Sum64()
		// 权重相等时按节点名字典序打破，保证不同调用方算出同一个结果
		if !picked || w > bestW || (w == bestW && n < best) {
			best, bestW, picked = n, w, true
		}
	}
	return best, picked
}

// Rank 把 nodes 按 key 的权重从高到低排序，返回一份新的切片，不修改入参。
// 用于 hub 的转发候选列表：Pick 只给出第一名，转发需要"第一名不通就试第二
// 名"的完整顺序。
//
// 打破平局的规则必须与 Pick 完全一致（权重相同按节点名字典序在前），否则
// Rank 排出来的第一名可能和 Pick 单独选出的第一名不是同一个节点，两处各自
// 独立维护一套顺序，行为就对不上了。
func Rank(nodes []string, key string) []string {
	out := make([]string, len(nodes))
	copy(out, nodes)
	weight := make(map[string]uint64, len(nodes))
	for _, n := range out {
		h := fnv.New64a()
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write([]byte(key))
		weight[n] = h.Sum64()
	}
	sort.Slice(out, func(i, j int) bool {
		wi, wj := weight[out[i]], weight[out[j]]
		if wi != wj {
			return wi > wj
		}
		return out[i] < out[j]
	})
	return out
}
