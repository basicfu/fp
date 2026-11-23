package fpsdk

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// appEventStub 是会读客户端消息的 Watch 桩：记下 hello 与确认，测试经 push 往流里塞业务事件。
type appEventStub struct {
	stubServer
	mu    sync.Mutex
	caps  []string
	acks  []string
	ready chan struct{}
	push  chan *fpv1.AppEvent
}

func (s *appEventStub) Watch(stream grpc.BidiStreamingServer[fpv1.WatchRequest, fpv1.WatchResponse]) error {
	if err := stream.Send(&fpv1.WatchResponse{Event: &fpv1.WatchResponse_Ready{Ready: &fpv1.WatchReady{}}}); err != nil {
		return err
	}
	go func() {
		for {
			req, err := stream.Recv()
			if err != nil {
				return
			}
			s.mu.Lock()
			if h := req.GetHello(); h != nil {
				s.caps = append(s.caps, h.GetCaps()...)
				select {
				case s.ready <- struct{}{}:
				default:
				}
			}
			if a := req.GetAck(); a != nil {
				s.acks = append(s.acks, a.GetIds()...)
			}
			s.mu.Unlock()
		}
	}()
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case ev := <-s.push:
			if err := stream.Send(&fpv1.WatchResponse{Event: &fpv1.WatchResponse_AppEvent{AppEvent: ev}}); err != nil {
				return err
			}
		}
	}
}

func (s *appEventStub) snapshot() (caps, acks []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.caps...), append([]string(nil), s.acks...)
}

func newAppEventEnv(t *testing.T, cb func(context.Context, AppEvent) error) *appEventStub {
	t.Helper()
	stub := &appEventStub{
		stubServer: stubServer{validate: okValidate("u1", 1000)},
		ready:      make(chan struct{}, 1),
		push:       make(chan *fpv1.AppEvent, 16),
	}
	addr, stop := startStub(t, "", stub)
	client, err := New(Options{Addr: "grpc://" + addr, AppID: "t", AppSecret: "t", OnAppEvent: cb})
	if err != nil {
		stop()
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		stop()
	})
	select {
	case <-stub.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("SDK 没发 hello")
	}
	return stub
}

func TestAppEventCallbackNilAcksAndErrorDoesNot(t *testing.T) {
	got := make(chan AppEvent, 4)
	stub := newAppEventEnv(t, func(_ context.Context, ev AppEvent) error {
		got <- ev
		if ev.Kind == "logged_out" {
			return errors.New("处理失败")
		}
		return nil
	})
	caps, _ := stub.snapshot()
	if len(caps) != 1 || caps[0] != "app_events" {
		t.Fatalf("hello caps = %v", caps)
	}

	at := time.UnixMilli(1_700_000_000_000)
	stub.push <- &fpv1.AppEvent{Id: "e1", Seq: 1, Kind: "login_succeeded", UserId: "u1",
		Payload: []byte(`{"session_id":"s1","client_ref":"42"}`), CreatedAtMs: at.UnixMilli()}
	stub.push <- &fpv1.AppEvent{Id: "e2", Seq: 2, Kind: "logged_out", UserId: "u1", Payload: []byte(`{}`)}

	ev := <-got
	if ev.ID != "e1" || ev.Seq != 1 || ev.UserID != "u1" || string(ev.Payload) != `{"session_id":"s1","client_ref":"42"}` || !ev.CreatedAt.Equal(at) {
		t.Fatalf("回调收到的事件不对: %+v", ev)
	}
	<-got

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, acks := stub.snapshot(); len(acks) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if _, acks := stub.snapshot(); len(acks) != 1 || acks[0] != "e1" {
		t.Fatalf("确认 = %v，期望只确认 e1", acks)
	}
}

func TestAppEventOldServerStillWorks(t *testing.T) {
	// 老服务端从不读客户端消息、也不发 app_event：SDK 发了 hello 也要照常就绪、照常处理撤销。
	env := newStubEnv(t, okValidate("u1", 60_000), func(o *Options) {
		o.OnAppEvent = func(context.Context, AppEvent) error { return nil }
	})
	env.waitUntil(t, env.client.StreamHealthy, "老服务端下推送流没有就绪")
	if _, err := env.auth.Validate(context.Background(), "tok"); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	env.pushRevoke(t, &fpv1.RevokeEvent{Tokens: []string{"tok"}})
	env.waitUntil(t, func() bool {
		_, state := env.auth.cache.get("tok", 0, 0)
		return state != cacheFresh
	}, "老服务端下撤销没生效")
}

func TestNoAppEventCallbackSendsNoHello(t *testing.T) {
	stub := &appEventStub{
		stubServer: stubServer{validate: okValidate("u1", 1000)},
		ready:      make(chan struct{}, 1),
		push:       make(chan *fpv1.AppEvent, 1),
	}
	addr, stop := startStub(t, "", stub)
	defer stop()
	client, err := New(Options{Addr: "grpc://" + addr, AppID: "t", AppSecret: "t"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	waitUntilTimeout(t, 5*time.Second, client.StreamHealthy, "流没有就绪")
	time.Sleep(200 * time.Millisecond)
	if caps, _ := stub.snapshot(); len(caps) != 0 {
		t.Fatalf("没设 OnAppEvent 却声明了 %v", caps)
	}
}
