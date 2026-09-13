package integration_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// rawAuthClient 不经 SDK、直接用应用凭据调 AuthService：本文件测的是传输层契约。
func rawAuthClient(t *testing.T, e *phase2Env) (fpv1.AuthServiceClient, context.Context) {
	t.Helper()
	conn, err := grpc.NewClient(e.addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx := metadata.AppendToOutgoingContext(context.Background(), "fp-app-id", e.appID, "fp-app-secret", e.appSecret)
	return fpv1.NewAuthServiceClient(conn), ctx
}

func wantErrorCode(t *testing.T, err error, grpcCode codes.Code, code string) {
	t.Helper()
	st, _ := status.FromError(err)
	if st.Code() != grpcCode {
		t.Fatalf("gRPC code = %v, want %v（err=%v）", st.Code(), grpcCode, err)
	}
	for _, d := range st.Details() {
		if ed, ok := d.(*fpv1.ErrorDetail); ok && ed.GetCode() == code {
			return
		}
	}
	t.Fatalf("缺少错误码 %s: %v", code, err)
}

func TestGetAccessKeyRPC(t *testing.T) {
	e := newPhase2Env(t)
	ctx := context.Background()
	role, err := e.authz.CreateRole(ctx, "合作方", "合作方", nil)
	if err != nil {
		t.Fatal(err)
	}
	k, err := e.accessKeys.Create(ctx, service.CreateAccessKeyInput{Remark: "顺丰", RoleKey: role.Key, AllowedIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	client, rpcCtx := rawAuthClient(t, e)

	res, err := client.GetAccessKey(rpcCtx, &fpv1.GetAccessKeyRequest{AccessKeyId: k.AccessKeyID})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetSecret() != k.Secret || res.GetRemark() != "顺丰" ||
		len(res.GetRoles()) != 1 || res.GetRoles()[0] != role.Key ||
		len(res.GetAllowedIps()) != 1 || res.GetAllowedIps()[0] != "127.0.0.1/32" ||
		res.GetCacheTtlMs() != int64(e.app.Session.TokenCacheTTLSeconds)*1000 {
		t.Fatalf("响应不对: %+v", res)
	}

	_, err = client.GetAccessKey(rpcCtx, &fpv1.GetAccessKeyRequest{AccessKeyId: "FPAKNOTEXIST000000000000"})
	wantErrorCode(t, err, codes.Unauthenticated, domain.CodeAccessKeyInvalid)

	if _, err := e.accessKeys.SetStatus(ctx, k.ID, domain.AccessKeyStatusDisabled); err != nil {
		t.Fatal(err)
	}
	_, err = client.GetAccessKey(rpcCtx, &fpv1.GetAccessKeyRequest{AccessKeyId: k.AccessKeyID})
	wantErrorCode(t, err, codes.PermissionDenied, domain.CodeAccessKeyDisabled)
}

func TestReportAccessKeyUsageRPC(t *testing.T) {
	e := newPhase2Env(t)
	ctx := context.Background()
	k, err := e.accessKeys.Create(ctx, service.CreateAccessKeyInput{Remark: "r"})
	if err != nil {
		t.Fatal(err)
	}
	client, rpcCtx := rawAuthClient(t, e)
	at := time.Now().Add(-time.Minute).Truncate(time.Minute)
	if _, err := client.ReportAccessKeyUsage(rpcCtx, &fpv1.ReportAccessKeyUsageRequest{
		Usages: []*fpv1.AccessKeyUsage{{AccessKeyId: k.AccessKeyID, LastUsedAtMs: at.UnixMilli()}},
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.accessKeys.Get(ctx, k.ID); got.LastUsedAt != at.UnixMilli() {
		t.Fatalf("LastUsedAt = %d, want %d", got.LastUsedAt, at.UnixMilli())
	}
}

// 【辨别力】从 Watch 流的出口断言:服务层发布的两种事件真的变成了对应的推送消息。
func TestWatchForwardsAccessKeyAndPolicyEvents(t *testing.T) {
	e := newPhase2Env(t)
	ctx := context.Background()
	client, rpcCtx := rawAuthClient(t, e)
	stream, err := client.Watch(rpcCtx)
	if err != nil {
		t.Fatal(err)
	}
	// 只起一个读流的 goroutine;next 读到第一条满足条件的推送为止,中途的其他事件忽略。
	msgs := make(chan *fpv1.WatchResponse, 16)
	go func() {
		defer close(msgs)
		for {
			m, err := stream.Recv()
			if err != nil {
				return
			}
			msgs <- m
		}
	}()
	next := func(what string, match func(*fpv1.WatchResponse) bool) {
		t.Helper()
		timeout := time.After(defaultWaitTimeout)
		for {
			select {
			case m, ok := <-msgs:
				if !ok {
					t.Fatalf("等 %s 时推送流断了", what)
				}
				if match(m) {
					return
				}
			case <-timeout:
				t.Fatalf("等 %s 超时", what)
			}
		}
	}
	next("ready", func(m *fpv1.WatchResponse) bool { return m.GetReady() != nil })

	k, err := e.accessKeys.Create(ctx, service.CreateAccessKeyInput{Remark: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.accessKeys.SetStatus(ctx, k.ID, domain.AccessKeyStatusDisabled); err != nil {
		t.Fatal(err)
	}
	next("AccessKeyChanged", func(m *fpv1.WatchResponse) bool {
		return m.GetAccessKeyChanged().GetAccessKeyId() == k.AccessKeyID
	})

	role, _ := e.authz.CreateRole(ctx, "角色", "角色", nil)
	perm, _ := e.authz.CreatePermission(ctx, e.app.ID, "GET:/x", "x", domain.PermissionKindAPI)
	if err := e.authz.SetRolePermission(ctx, role.ID, perm.ID, domain.EffectAllow); err != nil {
		t.Fatal(err)
	}
	next("PolicyChanged", func(m *fpv1.WatchResponse) bool { return m.GetPolicyChanged() != nil })
}
