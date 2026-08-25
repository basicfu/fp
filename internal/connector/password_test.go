package connector_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
)

// fakeLookup 用内存数据模拟 UserService 的两个查询方法。
type fakeLookup struct {
	// byIdentity 的键是 type + "|" + subject
	byIdentity map[string]uuid.UUID
	passwords  map[uuid.UUID]string
	statuses   map[uuid.UUID]string
}

func newFakeLookup() *fakeLookup {
	return &fakeLookup{
		byIdentity: map[string]uuid.UUID{},
		passwords:  map[uuid.UUID]string{},
		statuses:   map[uuid.UUID]string{},
	}
}

func (f *fakeLookup) add(identityType, subject, password, status string) uuid.UUID {
	id := uuid.New()
	f.byIdentity[identityType+"|"+subject] = id
	f.passwords[id] = password
	f.statuses[id] = status
	return id
}

func (f *fakeLookup) FindByIdentity(_ context.Context, identityType, subject string) (*domain.User, *domain.Identity, error) {
	id, ok := f.byIdentity[identityType+"|"+subject]
	if !ok {
		return nil, nil, domain.Errorf(domain.ErrNotFound, "登录标识不存在")
	}
	return &domain.User{ID: id, Status: f.statuses[id]},
		&domain.Identity{UserID: id, Type: identityType, Subject: subject}, nil
}

func (f *fakeLookup) VerifyPassword(_ context.Context, userID uuid.UUID, plain string) error {
	want, ok := f.passwords[userID]
	if !ok || want == "" || want != plain {
		return domain.Errorf(domain.ErrInvalidCredential, "账号或密码不正确")
	}
	return nil
}

func TestDetectIdentityType(t *testing.T) {
	tests := []struct {
		account string
		want    string
	}{
		{"13800138000", domain.IdentityTypePhone},
		{"18612345678", domain.IdentityTypePhone},
		{"a@b.com", domain.IdentityTypeEmail},
		{"alice", domain.IdentityTypeUsername},
		{"alice123", domain.IdentityTypeUsername},
		// 11 位但不以 1 开头，不是手机号
		{"23800138000", domain.IdentityTypeUsername},
		// 10 位数字不是手机号
		{"1380013800", domain.IdentityTypeUsername},
	}
	for _, tt := range tests {
		if got := connector.DetectIdentityType(tt.account); got != tt.want {
			t.Errorf("DetectIdentityType(%q) = %q, want %q", tt.account, got, tt.want)
		}
	}
}

func TestPasswordType(t *testing.T) {
	c := connector.NewPassword(newFakeLookup())
	if c.Type() != connector.TypePassword {
		t.Fatalf("Type = %q, want %q", c.Type(), connector.TypePassword)
	}
	if len(c.ConfigSchema()) == 0 {
		t.Fatal("ConfigSchema 不应为空")
	}
}

func TestPasswordAuthenticateSuccess(t *testing.T) {
	lookup := newFakeLookup()
	lookup.add(domain.IdentityTypePhone, "13800138000", "hunter2hunter2", domain.UserStatusActive)
	c := connector.NewPassword(lookup)

	res, err := c.Authenticate(context.Background(), nil, connector.Credentials{
		"account":  "13800138000",
		"password": "hunter2hunter2",
	})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if res.IdentityType != domain.IdentityTypePhone || res.Subject != "13800138000" {
		t.Fatalf("res = %+v", res)
	}
	if res.AllowCreate {
		t.Fatal("密码登录不应允许自动建号")
	}
}

func TestPasswordAuthenticateWrongPassword(t *testing.T) {
	lookup := newFakeLookup()
	lookup.add(domain.IdentityTypePhone, "13800138000", "hunter2hunter2", domain.UserStatusActive)
	c := connector.NewPassword(lookup)

	_, err := c.Authenticate(context.Background(), nil, connector.Credentials{
		"account": "13800138000", "password": "wrong",
	})
	if !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", err)
	}
}

// 账号不存在必须返回与密码错误相同的错误，避免账号枚举。
func TestPasswordAuthenticateUnknownAccountLooksLikeWrongPassword(t *testing.T) {
	c := connector.NewPassword(newFakeLookup())

	_, err := c.Authenticate(context.Background(), nil, connector.Credentials{
		"account": "13800138000", "password": "whatever",
	})
	if !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", err)
	}
}

func TestPasswordAuthenticateRequiresBothFields(t *testing.T) {
	c := connector.NewPassword(newFakeLookup())
	ctx := context.Background()

	if _, err := c.Authenticate(ctx, nil, connector.Credentials{"password": "x"}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("缺 account err = %v, want ErrInvalidArgument", err)
	}
	if _, err := c.Authenticate(ctx, nil, connector.Credentials{"account": "a"}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("缺 password err = %v, want ErrInvalidArgument", err)
	}
}

// 配置里关掉某种标识类型后，该类型的账号不能用密码登录。
func TestPasswordAuthenticateRespectsAllowedIdentityTypes(t *testing.T) {
	lookup := newFakeLookup()
	lookup.add(domain.IdentityTypeUsername, "alice", "hunter2hunter2", domain.UserStatusActive)
	c := connector.NewPassword(lookup)
	ctx := context.Background()

	cfg := map[string]any{"allowUsername": false}
	_, err := c.Authenticate(ctx, cfg, connector.Credentials{
		"account": "alice", "password": "hunter2hunter2",
	})
	if !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", err)
	}

	// 打开后可以登录
	cfg["allowUsername"] = true
	if _, err := c.Authenticate(ctx, cfg, connector.Credentials{
		"account": "alice", "password": "hunter2hunter2",
	}); err != nil {
		t.Fatalf("允许后仍失败: %v", err)
	}
}

// 邮箱默认关闭，需要显式打开。
func TestPasswordEmailDisabledByDefault(t *testing.T) {
	lookup := newFakeLookup()
	lookup.add(domain.IdentityTypeEmail, "a@b.com", "hunter2hunter2", domain.UserStatusActive)
	c := connector.NewPassword(lookup)
	ctx := context.Background()

	if _, err := c.Authenticate(ctx, nil, connector.Credentials{
		"account": "a@b.com", "password": "hunter2hunter2",
	}); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("默认应关闭邮箱登录, err = %v", err)
	}
	if _, err := c.Authenticate(ctx, map[string]any{"allowEmail": true}, connector.Credentials{
		"account": "a@b.com", "password": "hunter2hunter2",
	}); err != nil {
		t.Fatalf("打开后仍失败: %v", err)
	}
}
