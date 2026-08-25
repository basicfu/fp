package connector_test

import (
	"context"
	"errors"
	"testing"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
)

// fakeCodes 记录最后一次校验请求，并按预置结果返回。
type fakeCodes struct {
	err             error
	purpose, target string
	code            string
	calls           int
}

func (f *fakeCodes) Verify(_ context.Context, purpose, target, code string) error {
	f.calls++
	f.purpose, f.target, f.code = purpose, target, code
	return f.err
}

func TestSMSCodeType(t *testing.T) {
	c := connector.NewSMSCode(&fakeCodes{})
	if c.Type() != connector.TypeSMSCode {
		t.Fatalf("Type = %q, want %q", c.Type(), connector.TypeSMSCode)
	}
	if len(c.ConfigSchema()) == 0 {
		t.Fatal("ConfigSchema 不应为空")
	}
}

func TestSMSCodeAuthenticateSuccess(t *testing.T) {
	codes := &fakeCodes{}
	c := connector.NewSMSCode(codes)

	res, err := c.Authenticate(context.Background(), nil, connector.Credentials{
		"phone": "13800138000",
		"code":  "123456",
	})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if res.IdentityType != domain.IdentityTypePhone {
		t.Errorf("IdentityType = %q", res.IdentityType)
	}
	if res.Subject != "13800138000" {
		t.Errorf("Subject = %q", res.Subject)
	}
	// 短信验证码本身证明了手机号归属，允许首次登录即建号。
	if !res.AllowCreate {
		t.Error("AllowCreate = false, want true")
	}

	if codes.calls != 1 {
		t.Fatalf("Verify 调用次数 = %d, want 1", codes.calls)
	}
	if codes.target != "13800138000" || codes.code != "123456" {
		t.Fatalf("传给 Verify 的参数不对: %+v", codes)
	}
}

func TestSMSCodeAuthenticateWrongCode(t *testing.T) {
	codes := &fakeCodes{err: domain.Errorf(domain.ErrInvalidCredential, "验证码不正确")}
	c := connector.NewSMSCode(codes)

	_, err := c.Authenticate(context.Background(), nil, connector.Credentials{
		"phone": "13800138000", "code": "000000",
	})
	if !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", err)
	}
}

func TestSMSCodeRejectsMalformedPhone(t *testing.T) {
	codes := &fakeCodes{}
	c := connector.NewSMSCode(codes)
	ctx := context.Background()

	for _, phone := range []string{"", "1380013800", "23800138000", "abcdefghijk"} {
		_, err := c.Authenticate(ctx, nil, connector.Credentials{"phone": phone, "code": "123456"})
		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("phone %q err = %v, want ErrInvalidArgument", phone, err)
		}
	}
	// 格式不合法时不应浪费一次验证码校验（那会消耗尝试次数）。
	if codes.calls != 0 {
		t.Fatalf("Verify 调用次数 = %d, want 0", codes.calls)
	}
}

func TestSMSCodeRejectsEmptyCode(t *testing.T) {
	c := connector.NewSMSCode(&fakeCodes{})
	_, err := c.Authenticate(context.Background(), nil, connector.Credentials{"phone": "13800138000"})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}

// 配置可以关掉「首次登录自动注册」，此时 AllowCreate 为 false，
// 未注册的手机号会在 AuthService 那一层被拒绝。
func TestSMSCodeAutoRegisterConfigurable(t *testing.T) {
	c := connector.NewSMSCode(&fakeCodes{})
	res, err := c.Authenticate(context.Background(), map[string]any{"autoRegister": false},
		connector.Credentials{"phone": "13800138000", "code": "123456"})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if res.AllowCreate {
		t.Fatal("autoRegister=false 时 AllowCreate 应为 false")
	}
}

func TestSMSCodeSubjectFrom(t *testing.T) {
	c := connector.NewSMSCode(&fakeCodes{})

	typ, subj := c.SubjectFrom(connector.Credentials{"phone": "13800138000"})
	if typ != domain.IdentityTypePhone || subj != "13800138000" {
		t.Fatalf("SubjectFrom = (%q, %q)", typ, subj)
	}
	if typ, subj := c.SubjectFrom(connector.Credentials{}); typ != "" || subj != "" {
		t.Fatalf("空凭据 SubjectFrom = (%q, %q), want 两个空串", typ, subj)
	}
}
