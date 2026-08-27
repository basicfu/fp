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
	// verifyCalls 记录 VerifyPassword 被调用的次数，用于验证时序抹平。
	verifyCalls int
	// findErr / verifyErr 非 nil 时由对应方法直接返回，用来模拟数据库故障。
	findErr   error
	verifyErr error
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
	if f.findErr != nil {
		return nil, nil, f.findErr
	}
	id, ok := f.byIdentity[identityType+"|"+subject]
	if !ok {
		return nil, nil, domain.Errorf(domain.ErrNotFound, "登录标识不存在")
	}
	return &domain.User{ID: id, Status: f.statuses[id]},
		&domain.Identity{UserID: id, Type: identityType, Subject: subject}, nil
}

func (f *fakeLookup) VerifyPassword(_ context.Context, userID uuid.UUID, plain string) error {
	f.verifyCalls++
	if f.verifyErr != nil {
		return f.verifyErr
	}
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

// 账号不存在时也必须走一次口令校验，否则"跳过 bcrypt"会让响应时间
// 泄露该账号是否注册过——同错误的防枚举设计就被时序旁路架空了。
func TestPasswordAlwaysVerifiesToEqualizeTiming(t *testing.T) {
	lookup := newFakeLookup()
	c := connector.NewPassword(lookup)

	_, err := c.Authenticate(context.Background(), nil, connector.Credentials{
		"account": "13800138000", "password": "whatever",
	})
	if !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", err)
	}
	if lookup.verifyCalls != 1 {
		t.Fatalf("账号不存在时 VerifyPassword 调用次数 = %d, want 1（用于抹平时序）", lookup.verifyCalls)
	}

	// 账号存在时同样只调一次，不能变成两次
	lookup2 := newFakeLookup()
	lookup2.add(domain.IdentityTypePhone, "13800138000", "hunter2hunter2", domain.UserStatusActive)
	c2 := connector.NewPassword(lookup2)
	if _, err := c2.Authenticate(context.Background(), nil, connector.Credentials{
		"account": "13800138000", "password": "hunter2hunter2",
	}); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if lookup2.verifyCalls != 1 {
		t.Fatalf("账号存在时 VerifyPassword 调用次数 = %d, want 1", lookup2.verifyCalls)
	}
}

// 数据库故障不能被伪装成"密码错误"。
//
// 把 FindByIdentity 的任何错误都折成 invalid 的话，一次 Postgres 抖动会让每一次
// 密码登录都返回 401：调用方看到的是"凭据不对"而不是 5xx，监控上看不出故障，
// 审计表还会被灌进一批凭据失败记录，事后跟真正的爆破尝试混在一起分不开。
func TestPasswordAuthenticateSurfacesLookupOutage(t *testing.T) {
	outage := errors.New("connection refused")
	lookup := newFakeLookup()
	lookup.findErr = outage
	c := connector.NewPassword(lookup)

	_, err := c.Authenticate(context.Background(), nil, connector.Credentials{
		"account": "13800138000", "password": "hunter2hunter2",
	})
	if !errors.Is(err, outage) {
		t.Fatalf("err = %v, want 原样上报的 %v", err, outage)
	}
	if errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatal("数据库故障被伪装成了凭据错误")
	}
}

// 同一条规则对 VerifyPassword 也成立：读不到密码哈希是故障，不是密码错。
func TestPasswordAuthenticateSurfacesVerifyOutage(t *testing.T) {
	outage := errors.New("connection refused")
	lookup := newFakeLookup()
	lookup.add(domain.IdentityTypePhone, "13800138000", "hunter2hunter2", domain.UserStatusActive)
	lookup.verifyErr = outage
	c := connector.NewPassword(lookup)

	_, err := c.Authenticate(context.Background(), nil, connector.Credentials{
		"account": "13800138000", "password": "hunter2hunter2",
	})
	if !errors.Is(err, outage) {
		t.Fatalf("err = %v, want 原样上报的 %v", err, outage)
	}
	if errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatal("数据库故障被伪装成了凭据错误")
	}
}

// 账号不存在必须仍然走"同错误"那条路，不能被上面那条故障分支顺手改掉。
func TestPasswordAuthenticateNotFoundStillLooksLikeWrongPassword(t *testing.T) {
	lookup := newFakeLookup()
	c := connector.NewPassword(lookup)

	_, err := c.Authenticate(context.Background(), nil, connector.Credentials{
		"account": "13800138000", "password": "whatever",
	})
	if !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", err)
	}
	if errors.Is(err, domain.ErrNotFound) {
		t.Fatal("ErrNotFound 泄露到了调用方——账号是否存在被暴露了")
	}
	// 时序抹平仍然生效
	if lookup.verifyCalls != 1 {
		t.Fatalf("VerifyPassword 调用次数 = %d, want 1", lookup.verifyCalls)
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

func TestPasswordSubjectFrom(t *testing.T) {
	c := connector.NewPassword(newFakeLookup())

	typ, subj := c.SubjectFrom(connector.Credentials{"account": "13800138000"})
	if typ != domain.IdentityTypePhone || subj != "13800138000" {
		t.Fatalf("SubjectFrom = (%q, %q)", typ, subj)
	}
	// 凭据里没有 account 时返回空串，而不是 panic 或臆造值
	if typ, subj := c.SubjectFrom(connector.Credentials{}); typ != "" || subj != "" {
		t.Fatalf("空凭据 SubjectFrom = (%q, %q), want 两个空串", typ, subj)
	}
}
