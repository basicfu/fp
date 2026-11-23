package fpsdk

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

type revokeSessionStub struct {
	stubServer
	got *fpv1.RevokeSessionRequest
	err error
}

func (s *revokeSessionStub) RevokeSession(_ context.Context, req *fpv1.RevokeSessionRequest) (*fpv1.RevokeSessionResponse, error) {
	s.got = req
	if s.err != nil {
		return nil, s.err
	}
	return &fpv1.RevokeSessionResponse{Revoked: 2}, nil
}

func TestAuthRevokeSession(t *testing.T) {
	stub := &revokeSessionStub{stubServer: stubServer{validate: okValidate("u1", 1000)}}
	addr, stop := startStub(t, "", stub)
	defer stop()
	client, err := New(Options{Addr: "grpc://" + addr, AppID: "t", AppSecret: "t"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	n, err := client.Auth().RevokeSession(context.Background(), "u1", "s1", RevokeReasonKick)
	if err != nil || n != 2 {
		t.Fatalf("RevokeSession = %d, %v", n, err)
	}
	if stub.got.GetUserId() != "u1" || stub.got.GetSessionId() != "s1" || stub.got.GetReason() != "kick" {
		t.Fatalf("请求 = %+v", stub.got)
	}

	// 用户不存在是 ErrNotFound，不能落进 ErrUnauthorized（业务方会误清自己的登录态）。
	stub.err = detailed(codes.NotFound, "USER_NOT_FOUND", "用户不存在")
	_, err = client.Auth().RevokeSession(context.Background(), "x", "s1", RevokeReasonKick)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
