// Package rendezvous 实现最高随机权重哈希（HRW）。
package rendezvous

import "hash/fnv"

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
