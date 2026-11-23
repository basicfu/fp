package grpcapi

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// testDevicePub 是一个合法的设备公钥写法：32 字节 hex。
var testDevicePub = strings.Repeat("0a", 32)

func TestLoginDeviceFieldsComeBackFromValidateToken(t *testing.T) {
	env := newGRPCEnv(t)
	ctx := env.authed(context.Background())
	phone := randomPhone()
	user, _, _, err := env.users.EnsureUserWithIdentity(context.Background(), service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: phone,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.users.SetPassword(context.Background(), user.ID, "hunter2hunter2"); err != nil {
		t.Fatal(err)
	}

	login, err := env.client.Login(ctx, &fpv1.LoginRequest{
		ConnectorType: connector.TypePassword,
		Credentials:   map[string]string{"account": phone, "password": "hunter2hunter2"},
		DevicePub:     testDevicePub,
		ClientRef:     "device-7",
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	res, err := env.client.ValidateToken(ctx, &fpv1.ValidateTokenRequest{Token: login.GetToken()})
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if res.GetClientRef() != "device-7" || res.GetDevicePub() != testDevicePub {
		t.Fatalf("ValidateToken 带回 client_ref=%q device_pub=%q", res.GetClientRef(), res.GetDevicePub())
	}

	// 不带设备字段的登录照旧，校验结果里为空。
	plain := env.loginWithPassword(t, ctx)
	res, err = env.client.ValidateToken(ctx, &fpv1.ValidateTokenRequest{Token: plain})
	if err != nil || res.GetClientRef() != "" || res.GetDevicePub() != "" {
		t.Fatalf("没绑设备的会话: %+v err=%v", res, err)
	}
}

// device_pub 只认 32 字节 hex：随便什么串刻进会话的话，之后每次请求都比对不上，用户登录了也用不了。
func TestLoginRejectsMalformedDevicePub(t *testing.T) {
	env := newGRPCEnv(t)
	ctx := env.authed(context.Background())
	for _, pub := range []string{"0a0b0c", strings.Repeat("zz", 32), strings.Repeat("0a", 33)} {
		_, err := env.client.Login(ctx, &fpv1.LoginRequest{
			ConnectorType: connector.TypePassword,
			Credentials:   map[string]string{"account": randomPhone(), "password": "x"},
			DevicePub:     pub,
		})
		wantStatus(t, "device_pub="+pub, err, codes.InvalidArgument, domain.CodeInvalidArgument)
		_, err = env.client.ExchangeOAuthTicket(ctx, &fpv1.ExchangeOAuthTicketRequest{Ticket: "t", DevicePub: pub})
		wantStatus(t, "oauth device_pub="+pub, err, codes.InvalidArgument, domain.CodeInvalidArgument)
	}
}
