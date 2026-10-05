package fpsdk

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// notifyStub 是一个可编排的 NotifyService 桩：fn 决定第 call 次（从 1 起）调用的结果。
type notifyStub struct {
	fpv1.UnimplementedNotifyServiceServer
	mu    sync.Mutex
	calls []*fpv1.SendRequest
	fn    func(call int, req *fpv1.SendRequest) error
}

func (s *notifyStub) Send(_ context.Context, req *fpv1.SendRequest) (*fpv1.SendResponse, error) {
	s.mu.Lock()
	s.calls = append(s.calls, req)
	call := len(s.calls)
	s.mu.Unlock()
	if err := s.fn(call, req); err != nil {
		return nil, err
	}
	return &fpv1.SendResponse{}, nil
}

func (s *notifyStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// newNotifyForTest 起一个只挂 NotifyService 桩的真实 gRPC 服务端，返回连着它的 Notify。
// 不走 New()：New 会起 Watch 流等一堆与本测试无关的后台 goroutine。
func newNotifyForTest(t *testing.T, fn func(call int, req *fpv1.SendRequest) error) (*Notify, *notifyStub) {
	t.Helper()
	notifyBackoff = time.Millisecond
	t.Cleanup(func() { notifyBackoff = 200 * time.Millisecond })

	stub := &notifyStub{fn: fn}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听: %v", err)
	}
	srv := grpc.NewServer()
	fpv1.RegisterNotifyServiceServer(srv, stub)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &Notify{c: &Client{notifyRPC: fpv1.NewNotifyServiceClient(conn)}}, stub
}

func detailed(code codes.Code, errCode, msg string) error {
	st, err := status.New(code, msg).WithDetails(&fpv1.ErrorDetail{Code: errCode, Msg: msg})
	if err != nil {
		panic(err)
	}
	return st.Err()
}

func TestNotifySendRetriesTransientFailuresWithTheSameKey(t *testing.T) {
	n, stub := newNotifyForTest(t, func(call int, _ *fpv1.SendRequest) error {
		if call < 3 {
			return status.Error(codes.Unavailable, "fp 不可达")
		}
		return nil
	})
	if err := n.Send(context.Background(), "login_sms", "13800138000", map[string]string{"code": "1"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if stub.callCount() != 3 {
		t.Fatalf("calls = %d, want 3（首次 + 两次重试）", stub.callCount())
	}
	key := stub.calls[0].GetIdempotencyKey()
	if key == "" {
		t.Fatal("SDK 应默认生成幂等键")
	}
	for i, c := range stub.calls {
		if c.GetIdempotencyKey() != key {
			t.Fatalf("第 %d 次调用换了幂等键——重试会让服务端当成新请求而重复发送", i+1)
		}
		if c.GetCode() != "login_sms" || c.GetTo() != "13800138000" || c.GetParams()["code"] != "1" {
			t.Fatalf("第 %d 次调用的请求内容变了: %+v", i+1, c)
		}
	}
}

func TestNotifySendGivesUpAfterThreeAttempts(t *testing.T) {
	n, stub := newNotifyForTest(t, func(int, *fpv1.SendRequest) error { return status.Error(codes.Unavailable, "down") })
	err := n.Send(context.Background(), "c", "", nil)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if stub.callCount() != 3 {
		t.Fatalf("calls = %d, want 3", stub.callCount())
	}
}

// 业务错误重试多少次结果都一样：不重试，并且保留服务端给的错误码。
// NotFound / PermissionDenied 在这里不能被归进 ErrUnauthorized（那会让调用方以为是凭据问题）。
func TestNotifySendDoesNotRetryBusinessErrors(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		code    string
		wantIs  error
		notIsOf error
	}{
		{"模板不存在", detailed(codes.NotFound, "NOTIFY_TEMPLATE_NOT_FOUND", "通知模板不存在"), "NOTIFY_TEMPLATE_NOT_FOUND", nil, ErrUnauthorized},
		{"模板已停用", detailed(codes.PermissionDenied, "NOTIFY_TEMPLATE_DISABLED", "已停用"), "NOTIFY_TEMPLATE_DISABLED", nil, ErrUnauthorized},
		{"变量不匹配", detailed(codes.InvalidArgument, "NOTIFY_PARAMS_INVALID", "变量不匹配"), "NOTIFY_PARAMS_INVALID", ErrInvalidArgument, nil},
		{"凭据无效", detailed(codes.Unauthenticated, "APP_CREDENTIAL_INVALID", "无效"), "APP_CREDENTIAL_INVALID", ErrUnauthorized, nil},
	}
	for _, c := range cases {
		n, stub := newNotifyForTest(t, func(int, *fpv1.SendRequest) error { return c.err })
		err := n.Send(context.Background(), "c", "", nil)
		var fe *Error
		if !errors.As(err, &fe) || fe.Code != c.code {
			t.Errorf("%s: err = %v, want 结构化错误 %s", c.name, err, c.code)
		}
		if c.wantIs != nil && !errors.Is(err, c.wantIs) {
			t.Errorf("%s: errors.Is(%v) 应成立", c.name, c.wantIs)
		}
		if c.notIsOf != nil && errors.Is(err, c.notIsOf) {
			t.Errorf("%s: 不该被归进 %v", c.name, c.notIsOf)
		}
		if stub.callCount() != 1 {
			t.Errorf("%s: calls = %d, want 1（业务错误不重试）", c.name, stub.callCount())
		}
	}
}

// 服务端说"相同幂等键的通知正在发送中"：上一次请求还没结束，稍后重试会得到"已完成"。
func TestNotifySendRetriesWhileInProgress(t *testing.T) {
	n, stub := newNotifyForTest(t, func(call int, _ *fpv1.SendRequest) error {
		if call == 1 {
			return detailed(codes.AlreadyExists, notifyInProgressCode, "正在发送中")
		}
		return nil
	})
	if err := n.Send(context.Background(), "c", "", nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if stub.callCount() != 2 || stub.calls[0].GetIdempotencyKey() != stub.calls[1].GetIdempotencyKey() {
		t.Fatalf("calls = %d，两次应使用同一个幂等键", stub.callCount())
	}
}

func TestNotifySendKeyOptionsAndGeneration(t *testing.T) {
	n, stub := newNotifyForTest(t, func(int, *fpv1.SendRequest) error { return nil })
	ctx := context.Background()
	_ = n.Send(ctx, "c", "", nil, WithIdempotencyKey("evt-1"))
	_ = n.Send(ctx, "c", "", nil)
	_ = n.Send(ctx, "c", "", nil)
	if got := stub.calls[0].GetIdempotencyKey(); got != "evt-1" {
		t.Fatalf("指定的幂等键应原样使用, got %q", got)
	}
	k1, k2 := stub.calls[1].GetIdempotencyKey(), stub.calls[2].GetIdempotencyKey()
	if k1 == "" || k2 == "" || k1 == k2 {
		t.Fatalf("自动生成的键必须非空且每次调用不同: %q %q", k1, k2)
	}
}

// fakeNotifyRPC 直接实现 fpv1.NotifyServiceClient，不走网络。
//
// 不用上面的桩服务端：真实 gRPC 客户端遇到已结束的 ctx 会自己返回 Canceled，而 Canceled 本来就不重试，
// 重试循环自己认不认 ctx 在那条路上永远测不出来。这里的假客户端无视 ctx、照样返回可重试的 Unavailable。
type fakeNotifyRPC struct {
	mu    sync.Mutex
	calls int
	fn    func() error
}

func (f *fakeNotifyRPC) Send(context.Context, *fpv1.SendRequest, ...grpc.CallOption) (*fpv1.SendResponse, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if err := f.fn(); err != nil {
		return nil, err
	}
	return &fpv1.SendResponse{}, nil
}

func (f *fakeNotifyRPC) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// requireStopsAfterOneAttempt 用一个每次都返回 Unavailable 的假 RPC 调一次 Send（onCall 在每次 RPC 里先执行），
// 要求它在 1 秒内带着错误返回，且只发起过一次 RPC。调用前 notifyBackoff 必须已被调成远大于 1 秒。
func requireStopsAfterOneAttempt(t *testing.T, ctx context.Context, onCall func()) {
	t.Helper()
	rpc := &fakeNotifyRPC{fn: func() error {
		if onCall != nil {
			onCall()
		}
		return status.Error(codes.Unavailable, "down")
	}}
	n := &Notify{c: &Client{notifyRPC: rpc}}

	done := make(chan error, 1)
	go func() { done <- n.Send(ctx, "c", "", nil) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("应返回错误")
		}
	case <-time.After(time.Second):
		t.Fatal("Send 超过 1 秒没返回：ctx 已结束，却还在退避里干等")
	}
	if got := rpc.callCount(); got != 1 {
		t.Fatalf("calls = %d, want 1（ctx 已结束，不该再重试）", got)
	}
}

// 调用方自己的 ctx 已经结束（取消或到期）：别再重试，更别在退避里干等。
// 退避被调成 1 小时，任何真去睡它的实现都会撞上 1 秒的断言。
func TestNotifySendStopsRetryingWhenContextIsDone(t *testing.T) {
	old := notifyBackoff
	notifyBackoff = time.Hour
	t.Cleanup(func() { notifyBackoff = old })

	t.Run("第一次尝试期间被取消", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		requireStopsAfterOneAttempt(t, ctx, cancel)
	})
	t.Run("截止时间在调用前就已过", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Minute))
		defer cancel()
		requireStopsAfterOneAttempt(t, ctx, nil)
	})
	// 前两个用例在进退避之前就被 ctx.Err() 拦下了，只有这个会真的走到等待那一步。
	t.Run("退避等待期间被取消", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		time.AfterFunc(50*time.Millisecond, cancel)
		requireStopsAfterOneAttempt(t, ctx, nil)
	})
}
