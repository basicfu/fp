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

func TestIMConfigValidate(t *testing.T) {
	base := domain.DefaultIMConfig()
	base.Enabled = true
	if err := base.Validate(); err != nil {
		t.Fatalf("默认配置打开后被拒：%v", err)
	}

	// 关着的时候不校验其余项：还没配就先拦人，等于逼人一次填全才能存草稿。
	off := domain.IMConfig{Enabled: false, ConnPolicy: "whatever", ConnLimit: -1}
	if err := off.Validate(); err != nil {
		t.Fatalf("im_enabled=false 时不该校验其余项：%v", err)
	}

	for _, tc := range []struct {
		name string
		mut  func(*domain.IMConfig)
	}{
		{"未知策略", func(c *domain.IMConfig) { c.ConnPolicy = "whatever" }},
		{"limit 策略下上限为零", func(c *domain.IMConfig) {
			c.ConnPolicy = domain.IMConnPolicyLimit
			c.ConnLimit = 0
		}},
		{"允许访客但限流为零", func(c *domain.IMConfig) { c.AllowGuest = true; c.GuestIPRate = 0 }},
		{"biz_auth 缺地址", func(c *domain.IMConfig) {
			c.BizAuth = &domain.IMBizAuth{TimeoutMs: 2000, CacheSize: 10}
		}},
		{"biz_auth 明文 http", func(c *domain.IMConfig) {
			c.BizAuth = &domain.IMBizAuth{VerifyURL: "http://x/v", TimeoutMs: 2000, CacheSize: 10}
		}},
		{"biz_auth 超时为零", func(c *domain.IMConfig) {
			c.BizAuth = &domain.IMBizAuth{VerifyURL: "https://x/v", CacheSize: 10}
		}},
		{"biz_auth 缓存容量为零", func(c *domain.IMConfig) {
			c.BizAuth = &domain.IMBizAuth{VerifyURL: "https://x/v", TimeoutMs: 2000}
		}},
	} {
		c := base
		tc.mut(&c)
		if c.Validate() == nil {
			t.Errorf("%s 必须被拒绝", tc.name)
		}
	}

	ok := base
	ok.BizAuth = &domain.IMBizAuth{VerifyURL: "https://x/v", TimeoutMs: 2000, CacheSize: 10}
	if err := ok.Validate(); err != nil {
		t.Fatalf("合法的 biz_auth 被拒：%v", err)
	}
}

// TestDefaultIMConfigIsDisabled 钉住"新版 fp 发布后对现网零影响"的那条依据。
func TestDefaultIMConfigIsDisabled(t *testing.T) {
	if domain.DefaultIMConfig().Enabled {
		t.Fatal("IM 默认必须是关的——这是新版 fp 发布后不影响现网的全部依据")
	}
}
