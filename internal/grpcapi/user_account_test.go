package grpcapi

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// 走一遍 kkdesk 换绑手机号的调用序列：验旧号 → 给新号发码 → 换绑；各错误码经 gRPC 的形状。
func TestUserServiceRebindPhoneFlow(t *testing.T) {
	e := newGRPCEnv(t)
	ctx := e.authed(context.Background())
	u := e.newBareUser(t, "小明")
	uid := u.ID.String()
	newPhone := randomPhone()

	if _, err := e.userClient.SendVerifyCode(ctx, &fpv1.SendVerifyCodeRequest{UserId: uid, Channel: service.VerifyChannelSMS}); err != nil {
		t.Fatalf("SendVerifyCode 旧号: %v", err)
	}
	_, err := e.userClient.VerifyCode(ctx, &fpv1.VerifyCodeRequest{UserId: uid, Channel: service.VerifyChannelSMS, Code: "000000x"})
	wantStatus(t, "错码", err, codes.Unauthenticated, domain.CodeCodeInvalid)
	if _, err := e.userClient.VerifyCode(ctx, &fpv1.VerifyCodeRequest{UserId: uid, Channel: service.VerifyChannelSMS, Code: e.sms.LastCode()}); err != nil {
		t.Fatalf("VerifyCode: %v", err)
	}

	if _, err := e.userClient.SendVerifyCode(ctx, &fpv1.SendVerifyCodeRequest{UserId: uid, Channel: service.VerifyChannelSMS, Target: newPhone}); err != nil {
		t.Fatalf("SendVerifyCode 新号: %v", err)
	}
	p, err := e.userClient.BindIdentity(ctx, &fpv1.BindIdentityRequest{UserId: uid, Type: domain.IdentityTypePhone, Subject: newPhone, Code: e.sms.LastCode()})
	if err != nil {
		t.Fatalf("BindIdentity: %v", err)
	}
	if ids := p.GetIdentities(); len(ids) != 1 || ids[0].GetType() != domain.IdentityTypePhone || ids[0].GetSubject() != newPhone {
		t.Fatalf("identities = %v", ids)
	}

	other := e.newBareUser(t, "小红")
	_, err = e.userClient.SendVerifyCode(ctx, &fpv1.SendVerifyCodeRequest{UserId: other.ID.String(), Channel: service.VerifyChannelSMS, Target: newPhone})
	wantStatus(t, "号码被占用", err, codes.AlreadyExists, domain.CodeIdentityTaken)
	_, err = e.userClient.UnbindIdentity(ctx, &fpv1.UnbindIdentityRequest{UserId: uid, Type: domain.IdentityTypePhone})
	wantStatus(t, "解绑最后一个", err, codes.AlreadyExists, domain.CodeLastIdentity)
	_, err = e.userClient.UnbindIdentity(ctx, &fpv1.UnbindIdentityRequest{UserId: uid, Type: service.UnbindKindWechat})
	wantStatus(t, "没绑微信", err, codes.NotFound, domain.CodeIdentityNotFound)
	_, err = e.userClient.SendVerifyCode(ctx, &fpv1.SendVerifyCodeRequest{UserId: "not-a-uuid", Channel: service.VerifyChannelSMS})
	wantStatus(t, "坏 user_id", err, codes.NotFound, domain.CodeUserNotFound)
}

func TestUserServiceSetPasswordAndBindStateChecks(t *testing.T) {
	e := newGRPCEnv(t)
	ctx := e.authed(context.Background())
	u := e.newBareUser(t, "小明")
	uid := u.ID.String()

	_, err := e.userClient.SetPassword(ctx, &fpv1.SetPasswordRequest{UserId: uid, Password: "short"})
	wantStatus(t, "密码过短", err, codes.InvalidArgument, domain.CodePasswordTooShort)
	if _, err := e.userClient.SetPassword(ctx, &fpv1.SetPasswordRequest{UserId: uid, Password: "Abcdefgh1234567890Abcdefgh123456"}); err != nil {
		t.Fatalf("SetPassword 32 位: %v", err)
	}
	p, err := e.userClient.GetUser(ctx, &fpv1.GetUserRequest{UserId: uid})
	if err != nil || !p.GetHasPassword() {
		t.Fatalf("设完密码 has_password = %v, err = %v", p.GetHasPassword(), err)
	}

	// 本 env 没配扫码存储：state / 票据一律无效，而不是内部错误。
	_, err = e.userClient.PollBindOAuth(ctx, &fpv1.PollBindOAuthRequest{UserId: uid, State: "nope"})
	wantStatus(t, "PollBindOAuth", err, codes.Unauthenticated, domain.CodeOAuthStateInvalid)
	_, err = e.userClient.ExchangeBindOAuthTicket(ctx, &fpv1.ExchangeBindOAuthTicketRequest{UserId: uid, Ticket: "nope"})
	wantStatus(t, "ExchangeBindOAuthTicket", err, codes.Unauthenticated, domain.CodeOAuthTicketInvalid)
}
