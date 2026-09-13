package fpsdk

import (
	"errors"
	"testing"
)

func TestNonceStoreRejectsReuseUntilExpiry(t *testing.T) {
	s := newNonceStore(10)
	if err := s.add("ak:n1", 100, 50); err != nil {
		t.Fatal(err)
	}
	if err := s.add("ak:n1", 100, 60); !errors.Is(err, errNonceUsed) {
		t.Fatalf("窗口内重复应拒绝，got %v", err)
	}
	if err := s.add("ak:n1", 200, 101); err != nil {
		t.Fatalf("过期后应能再用，got %v", err)
	}
}

// 【辨别力】满了就拒绝新的，不挤掉仍有效的旧记录——挤掉等于关掉防重放。
func TestNonceStoreFullRejectsInsteadOfEvicting(t *testing.T) {
	s := newNonceStore(2)
	_ = s.add("a", 100, 0)
	_ = s.add("b", 100, 0)
	if err := s.add("c", 100, 1); !errors.Is(err, errNonceFull) {
		t.Fatalf("满了应返回 errNonceFull，got %v", err)
	}
	if err := s.add("a", 100, 1); !errors.Is(err, errNonceUsed) {
		t.Fatalf("旧记录不能被挤掉，got %v", err)
	}
	if err := s.add("c", 200, 101); err != nil {
		t.Fatalf("旧记录过期后应腾出位置，got %v", err)
	}
}
