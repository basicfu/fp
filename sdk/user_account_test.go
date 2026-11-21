package fpsdk

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// accountStub 在 userStub 之上挂账号管理的八个 RPC：err 非 nil 时都返回它，否则回固定结果并记下请求。
type accountStub struct {
	userStub
	reqs []any
}

func (s *accountStub) record(req any) error {
	s.calls++
	s.reqs = append(s.reqs, req)
	return s.err
}

func (s *accountStub) SendVerifyCode(_ context.Context, req *fpv1.SendVerifyCodeRequest) (*fpv1.SendVerifyCodeResponse, error) {
	return &fpv1.SendVerifyCodeResponse{}, s.record(req)
}

func (s *accountStub) VerifyCode(_ context.Context, req *fpv1.VerifyCodeRequest) (*fpv1.VerifyCodeResponse, error) {
	return &fpv1.VerifyCodeResponse{}, s.record(req)
}

func (s *accountStub) BindIdentity(_ context.Context, req *fpv1.BindIdentityRequest) (*fpv1.UserProfile, error) {
	return s.profile, s.record(req)
}

func (s *accountStub) UnbindIdentity(_ context.Context, req *fpv1.UnbindIdentityRequest) (*fpv1.UserProfile, error) {
	return s.profile, s.record(req)
}

func (s *accountStub) StartBindOAuth(_ context.Context, req *fpv1.StartBindOAuthRequest) (*fpv1.StartBindOAuthResponse, error) {
	return &fpv1.StartBindOAuthResponse{State: "st", AuthUrl: "https://open.weixin", ProviderAppId: "wx", RedirectUri: "https://fp/cb", ExpiresInSec: 300}, s.record(req)
}

func (s *accountStub) PollBindOAuth(_ context.Context, req *fpv1.PollBindOAuthRequest) (*fpv1.PollBindOAuthResponse, error) {
	return &fpv1.PollBindOAuthResponse{Result: &fpv1.BindOAuthResult{Status: OAuthPending}}, s.record(req)
}

func (s *accountStub) ExchangeBindOAuthTicket(_ context.Context, req *fpv1.ExchangeBindOAuthTicketRequest) (*fpv1.ExchangeBindOAuthTicketResponse, error) {
	return &fpv1.ExchangeBindOAuthTicketResponse{Result: &fpv1.BindOAuthResult{
		Status: OAuthSuccess, User: s.profile, Nickname: "小红", AvatarUrl: "https://img/x",
	}}, s.record(req)
}

func (s *accountStub) SetPassword(_ context.Context, req *fpv1.SetPasswordRequest) (*fpv1.SetPasswordResponse, error) {
	return &fpv1.SetPasswordResponse{}, s.record(req)
}

func newAccountUsersForTest(t *testing.T, stub *accountStub) *Users {
	t.Helper()
	return newUsersForTest(t, stub)
}

// accountCalls 把八个方法各调一次，返回各自的错误。
func accountCalls(u *Users) map[string]error {
	ctx := context.Background()
	out := map[string]error{}
	out["SendVerifyCode"] = u.SendVerifyCode(ctx, "u-1", VerifyChannelSMS, "")
	out["VerifyCode"] = u.VerifyCode(ctx, "u-1", VerifyChannelSMS, "", "123456")
	_, out["BindIdentity"] = u.BindIdentity(ctx, "u-1", IdentityPhone, "13800138000", "123456")
	_, out["UnbindIdentity"] = u.UnbindIdentity(ctx, "u-1", IdentityWechat)
	_, out["StartBindOAuth"] = u.StartBindOAuth(ctx, "u-1", OAuthStartInput{Mode: OAuthModeWeb, ReturnURL: "https://a/b"})
	_, out["PollBindOAuth"] = u.PollBindOAuth(ctx, "u-1", "st")
	_, out["ExchangeBindOAuth"] = u.ExchangeBindOAuth(ctx, "u-1", "tk")
	out["SetPassword"] = u.SetPassword(ctx, "u-1", "hunter2hunter2")
	return out
}

func TestUsersAccountMethodsSendRequestsAndMapResults(t *testing.T) {
	stub := &accountStub{userStub: userStub{profile: &fpv1.UserProfile{Id: "u-1", Identities: []*fpv1.UserIdentity{{Type: "wechat_web", Subject: "o1"}}}}}
	u := newAccountUsersForTest(t, stub)
	for name, err := range accountCalls(u) {
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if len(stub.reqs) != 8 {
		t.Fatalf("reqs = %d, want 8", len(stub.reqs))
	}
	if r := stub.reqs[4].(*fpv1.StartBindOAuthRequest); r.GetConnectorType() != ConnectorWechatWeb || r.GetUserId() != "u-1" || r.GetReturnUrl() != "https://a/b" {
		t.Fatalf("StartBindOAuth 请求 = %+v（connector 应默认 wechat_web）", r)
	}
	if r := stub.reqs[2].(*fpv1.BindIdentityRequest); r.GetType() != IdentityPhone || r.GetSubject() != "13800138000" || r.GetCode() != "123456" {
		t.Fatalf("BindIdentity 请求 = %+v", r)
	}

	ctx := context.Background()
	st, _ := u.StartBindOAuth(ctx, "u-1", OAuthStartInput{Mode: OAuthModeWeb})
	if st.State != "st" || st.ExpiresIn != 300*time.Second || st.ProviderAppID != "wx" {
		t.Fatalf("start = %+v", st)
	}
	res, _ := u.ExchangeBindOAuth(ctx, "u-1", "tk")
	if res.Status != OAuthSuccess || res.User == nil || res.User.Identities[0].Subject != "o1" || res.Nickname != "小红" {
		t.Fatalf("result = %+v", res)
	}
	if res, _ := u.PollBindOAuth(ctx, "u-1", "st"); res.Status != OAuthPending || res.User != nil {
		t.Fatalf("poll = %+v", res)
	}
}

// 账号管理的错误：验证码 / 扫码票据不对不能被归成 ErrUnauthorized——WriteError 会回 401，客户端就把用户踢回登录页。
func TestUsersAccountErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		code   string
		wantIs error
		http   int
	}{
		{"验证码错", detailed(codes.Unauthenticated, CodeCodeInvalid, "验证码不正确或已过期"), CodeCodeInvalid, ErrInvalidArgument, http.StatusBadRequest},
		{"票据无效", detailed(codes.Unauthenticated, CodeOAuthTicketInvalid, "过期"), CodeOAuthTicketInvalid, ErrInvalidArgument, http.StatusBadRequest},
		{"应用凭据无效", detailed(codes.Unauthenticated, "APP_CREDENTIAL_INVALID", "无效"), "APP_CREDENTIAL_INVALID", ErrUnauthorized, http.StatusUnauthorized},
		{"已被占用", detailed(codes.AlreadyExists, CodeIdentityTaken, "已绑定其他账号"), CodeIdentityTaken, ErrConflict, http.StatusConflict},
		{"最后一个", detailed(codes.AlreadyExists, CodeLastIdentity, "解绑后将无法登录"), CodeLastIdentity, ErrConflict, http.StatusConflict},
		{"限流", detailed(codes.ResourceExhausted, CodeRateLimited, "发送过于频繁"), CodeRateLimited, ErrRateLimited, http.StatusTooManyRequests},
		{"没有手机号", detailed(codes.NotFound, CodeIdentityNotFound, "还没有绑定手机号"), CodeIdentityNotFound, ErrNotFound, http.StatusNotFound},
		{"模板没配", detailed(codes.Internal, CodeVerifyTemplateUnavailable, "模板未配置好"), CodeVerifyTemplateUnavailable, ErrUnavailable, http.StatusServiceUnavailable},
		{"已冻结", detailed(codes.PermissionDenied, CodeAccountFrozen, "账号已被冻结"), CodeAccountFrozen, ErrForbidden, http.StatusForbidden},
		{"没有结构化错误的占用", status.Error(codes.AlreadyExists, "x"), "", ErrConflict, http.StatusConflict},
	}
	for _, c := range cases {
		u := newAccountUsersForTest(t, &accountStub{userStub: userStub{err: c.err}})
		for method, err := range accountCalls(u) {
			var fe *Error
			if c.code != "" && (!errors.As(err, &fe) || fe.Code != c.code) {
				t.Errorf("%s/%s: err = %v, want 结构化错误 %s", c.name, method, err, c.code)
			}
			if !errors.Is(err, c.wantIs) {
				t.Errorf("%s/%s: errors.Is(%v) = false", c.name, method, c.wantIs)
			}
			rec := httptest.NewRecorder()
			WriteError(rec, err)
			if rec.Code != c.http {
				t.Errorf("%s/%s: WriteError = %d, want %d", c.name, method, rec.Code, c.http)
			}
		}
	}
}
