package grpcapi

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"

	"github.com/basicfu/fp/internal/domain"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// 业务方按会话 id 撤销：撤掉的 token 再校验是 KICKED（不是 TOKEN_INVALID），只认 kick，撤不到别的应用。
func TestRevokeSessionRPC(t *testing.T) {
	e := newGRPCEnv(t)
	ctx := e.authed(context.Background())
	token := e.loginWithPassword(t, ctx)
	v, err := e.client.ValidateToken(ctx, &fpv1.ValidateTokenRequest{Token: token})
	if err != nil {
		t.Fatal(err)
	}

	_, err = e.client.RevokeSession(ctx, &fpv1.RevokeSessionRequest{UserId: v.GetUserId(), SessionId: v.GetSessionId(), Reason: "logout"})
	wantStatus(t, "非 kick 的原因", err, codes.InvalidArgument, domain.CodeInvalidArgument)

	other := e.newApplication(t)
	res, err := e.client.RevokeSession(other.authed(context.Background()),
		&fpv1.RevokeSessionRequest{UserId: v.GetUserId(), SessionId: v.GetSessionId(), Reason: "kick"})
	if err != nil || res.GetRevoked() != 0 {
		t.Fatalf("别的应用撤销: %v, %v", res, err)
	}

	res, err = e.client.RevokeSession(ctx, &fpv1.RevokeSessionRequest{UserId: v.GetUserId(), SessionId: v.GetSessionId(), Reason: "kick"})
	if err != nil || res.GetRevoked() != 1 {
		t.Fatalf("RevokeSession: %v, %v", res, err)
	}
	_, err = e.client.ValidateToken(ctx, &fpv1.ValidateTokenRequest{Token: token})
	wantStatus(t, "被踢的 token", err, codes.Unauthenticated, domain.CodeKicked)

	_, err = e.client.RevokeSession(ctx, &fpv1.RevokeSessionRequest{UserId: "not-a-uuid", SessionId: "x", Reason: "kick"})
	wantStatus(t, "user_id 不合法", err, codes.NotFound, domain.CodeUserNotFound)
}
