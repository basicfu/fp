package domain_test

import (
	"testing"
	"time"

	"github.com/basicfu/fp/internal/domain"
)

func TestAccessKeyState(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	cases := []struct {
		name string
		k    domain.AccessKey
		want domain.AccessKeyState
	}{
		{"正常、永不过期", domain.AccessKey{Status: domain.AccessKeyStatusActive}, domain.AccessKeyStateActive},
		{"未到期", domain.AccessKey{Status: domain.AccessKeyStatusActive, ExpiresAt: 1_000_001}, domain.AccessKeyStateActive},
		{"恰好到期算过期", domain.AccessKey{Status: domain.AccessKeyStatusActive, ExpiresAt: 1_000_000}, domain.AccessKeyStateExpired},
		{"停用优先于过期", domain.AccessKey{Status: domain.AccessKeyStatusDisabled, ExpiresAt: 1}, domain.AccessKeyStateDisabled},
	}
	for _, c := range cases {
		if got := c.k.State(now); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}
