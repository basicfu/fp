package domain_test

import (
	"testing"
	"time"

	"github.com/basicfu/fp/internal/domain"
)

func TestDefaultSessionPolicyIsValid(t *testing.T) {
	if err := domain.DefaultSessionPolicy().Validate(); err != nil {
		t.Fatalf("默认策略应当合法: %v", err)
	}
}

func TestSessionPolicyValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*domain.SessionPolicy)
		wantErr bool
	}{
		{"默认值", func(*domain.SessionPolicy) {}, false},
		{"空闲超时为零", func(p *domain.SessionPolicy) { p.IdleTimeoutSeconds = 0 }, true},
		{"缓存 TTL 为零", func(p *domain.SessionPolicy) { p.TokenCacheTTLSeconds = 0 }, true},
		{"绝对上限为零", func(p *domain.SessionPolicy) { p.MaxLifetimeSeconds = 0 }, true},
		{"延期间隔不小于空闲超时", func(p *domain.SessionPolicy) {
			p.ExtendIntervalSeconds = p.IdleTimeoutSeconds
		}, true},
		{"轮换间隔大于绝对上限", func(p *domain.SessionPolicy) {
			p.RotateIntervalSeconds = p.MaxLifetimeSeconds + 1
		}, true},
		{"缓存 TTL 大于空闲超时", func(p *domain.SessionPolicy) {
			p.TokenCacheTTLSeconds = p.IdleTimeoutSeconds + 1
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := domain.DefaultSessionPolicy()
			tt.mutate(&p)
			err := p.Validate()
			if tt.wantErr && err == nil {
				t.Fatal("want error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("want nil, got %v", err)
			}
		})
	}
}

func TestIdleTimeoutFor(t *testing.T) {
	p := domain.DefaultSessionPolicy()

	if got, want := p.IdleTimeoutFor(false), 7*24*time.Hour; got != want {
		t.Errorf("web = %v, want %v", got, want)
	}
	if got, want := p.IdleTimeoutFor(true), 30*24*time.Hour; got != want {
		t.Errorf("mobile = %v, want %v", got, want)
	}

	// mobile 值为 0 时回落到 web 值
	p.IdleTimeoutMobileSeconds = 0
	if got, want := p.IdleTimeoutFor(true), 7*24*time.Hour; got != want {
		t.Errorf("mobile 回落 = %v, want %v", got, want)
	}
}
