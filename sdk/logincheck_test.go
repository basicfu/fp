package fpsdk

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// loginCheckStub 是会发登录校验、收应答的 Watch 桩。
type loginCheckStub struct {
	stubServer
	mu      sync.Mutex
	caps    []string
	hello   chan struct{}
	push    chan *fpv1.LoginCheck
	replies chan *fpv1.LoginCheckReply
}

func (s *loginCheckStub) Watch(stream grpc.BidiStreamingServer[fpv1.WatchRequest, fpv1.WatchResponse]) error {
	if err := stream.Send(&fpv1.WatchResponse{Event: &fpv1.WatchResponse_Ready{Ready: &fpv1.WatchReady{}}}); err != nil {
		return err
	}
	go func() {
		for {
			req, err := stream.Recv()
			if err != nil {
				return
			}
			if h := req.GetHello(); h != nil {
				s.mu.Lock()
				s.caps = append(s.caps, h.GetCaps()...)
				s.mu.Unlock()
				s.hello <- struct{}{}
			}
			if r := req.GetReply(); r != nil {
				s.replies <- r
			}
		}
	}()
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case m := <-s.push:
			if err := stream.Send(&fpv1.WatchResponse{Event: &fpv1.WatchResponse_LoginCheck{LoginCheck: m}}); err != nil {
				return err
			}
		}
	}
}

func newLoginCheckEnv(t *testing.T, opts func(*Options)) *loginCheckStub {
	t.Helper()
	stub := &loginCheckStub{
		stubServer: stubServer{validate: okValidate("u1", 1000)},
		hello:      make(chan struct{}, 4),
		push:       make(chan *fpv1.LoginCheck, 4),
		replies:    make(chan *fpv1.LoginCheckReply, 4),
	}
	addr, stop := startStub(t, "", stub)
	o := Options{Addr: "grpc://" + addr, AppID: "t", AppSecret: "t"}
	opts(&o)
	client, err := New(o)
	if err != nil {
		stop()
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		stop()
	})
	return stub
}

func (s *loginCheckStub) reply(t *testing.T) *fpv1.LoginCheckReply {
	t.Helper()
	select {
	case r := <-s.replies:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("没收到登录校验应答")
		return nil
	}
}

func TestLoginCheckCallbackAnswersOnStream(t *testing.T) {
	var got LoginCheckRequest
	stub := newLoginCheckEnv(t, func(o *Options) {
		o.OnAppEvent = func(context.Context, AppEvent) error { return nil }
		o.OnLoginCheck = func(ctx context.Context, req LoginCheckRequest) (LoginCheckResult, error) {
			got = req
			if _, ok := ctx.Deadline(); !ok {
				t.Error("回调的 ctx 没带期限")
			}
			if req.IP == "9.9.9.9" {
				return LoginCheckResult{Code: "version_too_low", Msg: "请升级"}, nil
			}
			return LoginCheckResult{Allow: true, ClientRef: "dev-1"}, nil
		}
	})
	<-stub.hello
	stub.mu.Lock()
	caps := append([]string(nil), stub.caps...)
	stub.mu.Unlock()
	if !slices.Contains(caps, "login_check") || !slices.Contains(caps, "app_events") {
		t.Fatalf("hello caps = %v", caps)
	}

	stub.push <- &fpv1.LoginCheck{CallId: "c1", DeviceHeader: "hdr", Ip: "1.1.1.1"}
	r := stub.reply(t)
	if r.GetCallId() != "c1" || !r.GetAllow() || r.GetClientRef() != "dev-1" || got.DeviceHeader != "hdr" || got.IP != "1.1.1.1" {
		t.Fatalf("应答 %+v，回调收到 %+v", r, got)
	}
	stub.push <- &fpv1.LoginCheck{CallId: "c2", Ip: "9.9.9.9"}
	r = stub.reply(t)
	if r.GetCallId() != "c2" || r.GetAllow() || r.GetCode() != "version_too_low" || r.GetMsg() != "请升级" {
		t.Fatalf("拒绝应答 %+v", r)
	}
}

func TestLoginCheckCallbackErrorRepliesUnavailable(t *testing.T) {
	stub := newLoginCheckEnv(t, func(o *Options) {
		o.OnLoginCheck = func(context.Context, LoginCheckRequest) (LoginCheckResult, error) {
			return LoginCheckResult{Allow: true, ClientRef: "ignored"}, errors.New("库挂了")
		}
	})
	<-stub.hello
	stub.push <- &fpv1.LoginCheck{CallId: "c1"}
	r := stub.reply(t)
	if r.GetAllow() || r.GetCode() != CodeLoginCheckUnavailable || r.GetClientRef() != "" {
		t.Fatalf("回调出错时应答 %+v", r)
	}
}

func TestNoLoginCheckCallbackDoesNotDeclare(t *testing.T) {
	stub := newLoginCheckEnv(t, func(o *Options) {
		o.OnAppEvent = func(context.Context, AppEvent) error { return nil }
	})
	<-stub.hello
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if slices.Contains(stub.caps, "login_check") {
		t.Fatalf("没设 OnLoginCheck 却声明了 %v", stub.caps)
	}
}
