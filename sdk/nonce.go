package fpsdk

import (
	"errors"
	"sync"
)

var (
	errNonceUsed = errors.New("fpsdk: nonce 已使用过")
	errNonceFull = errors.New("fpsdk: nonce 表已满")
)

// nonceStore 记住本进程见过的 (AK, nonce)，直到对应请求的时间戳超出窗口。
// 只在单个进程内去重：重放到另一台实例能通过，这是 spec 里已接受的限制。
type nonceStore struct {
	mu   sync.Mutex
	cap  int
	seen map[string]int64 // 键 → 失效时刻（Unix 秒）
}

func newNonceStore(capacity int) *nonceStore {
	return &nonceStore{cap: capacity, seen: make(map[string]int64)}
}

// add 记录一个 nonce。仍在有效期内的重复返回 errNonceUsed；满了且清掉过期记录后
// 仍然满，返回 errNonceFull——不挤掉旧记录，挤掉等于关掉防重放。
func (s *nonceStore) add(key string, expireAt, now int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if exp, ok := s.seen[key]; ok && exp > now {
		return errNonceUsed
	}
	if len(s.seen) >= s.cap {
		for k, exp := range s.seen {
			if exp <= now {
				delete(s.seen, k)
			}
		}
		if len(s.seen) >= s.cap {
			return errNonceFull
		}
	}
	s.seen[key] = expireAt
	return nil
}
