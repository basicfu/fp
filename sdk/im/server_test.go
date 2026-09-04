package fpim

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fpimv1 "github.com/basicfu/fp/sdk/gen/fp/im/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// stubIm 是最小的 ImService：校验 metadata、发 Ready、把 Push 原样回 Sent、可主动推 Inbound/Event。
//
// swallow 为 true 时，收到请求后只计数、不回 Result——用于制造"请求已经
// 发出、正在等回应"的在途状态，配合 TestServerInFlightPushFailsFastOnDisconnect
// 验证断线时在途请求会被立刻唤醒，而不是真的等到 RequestTimeout。
type stubIm struct {
	fpimv1.UnimplementedImServiceServer
	mu       sync.Mutex
	streams  []fpimv1.ImService_ConnectServer
	seenMD   metadata.MD
	swallow  atomic.Bool
	received atomic.Int64
}

func (s *stubIm) Connect(st fpimv1.ImService_ConnectServer) error {
	md, _ := metadata.FromIncomingContext(st.Context())
	s.mu.Lock()
	s.seenMD = md
	s.streams = append(s.streams, st)
	s.mu.Unlock()
	if err := st.Send(&fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Ready{Ready: &fpimv1.Ready{NodeId: "stub"}}}); err != nil {
		return err
	}
	for {
		req, err := st.Recv()
		if err != nil {
			return nil
		}
		s.received.Add(1)
		if s.swallow.Load() {
			continue // 故意不回 Result，让这个请求停在"在途"状态
		}
		res := &fpimv1.Result{ReqId: req.ReqId}
		switch b := req.Body.(type) {
		case *fpimv1.ConnectRequest_Push:
			res.Pushes = []*fpimv1.PushResult{{Subject: b.Push.Subject, Status: fpimv1.PushStatus_PUSH_STATUS_SENT, Nodes: 1}}
		case *fpimv1.ConnectRequest_PushMany:
			for _, sub := range b.PushMany.Subjects {
				res.Pushes = append(res.Pushes, &fpimv1.PushResult{Subject: sub, Status: fpimv1.PushStatus_PUSH_STATUS_SENT, Nodes: 1})
			}
		case *fpimv1.ConnectRequest_Sessions:
			res.Sessions = []*fpimv1.Session{{ConnId: "c1", NodeId: "stub", Os: "ios", Mobile: true, ConnectedAtMs: 9}}
		}
		_ = st.Send(&fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Result{Result: res}})
	}
}

func (s *stubIm) push(resp *fpimv1.ConnectResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.streams {
		_ = st.Send(resp)
	}
}

func startStub(t *testing.T) (*stubIm, string, func()) {
	t.Helper()
	stub := &stubIm{}
	var lis net.Listener
	var err error
	for i := 0; i < 20; i++ { // Windows 释放端口慢
		lis, err = net.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	fpimv1.RegisterImServiceServer(srv, stub)
	go srv.Serve(lis)
	return stub, lis.Addr().String(), srv.Stop
}

func waitUntil(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestServerPushSessionsAndCredentials(t *testing.T) {
	stub, addr, stop := startStub(t)
	defer stop()
	s, err := NewServer(ServerConfig{Addr: addr, AppID: "a1", AppSecret: "sec", Insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	waitUntil(t, s.StreamHealthy, "流应就绪")
	if got := stub.seenMD.Get("fp-app-id"); len(got) != 1 || got[0] != "a1" {
		t.Fatalf("metadata 应带 fp-app-id，实际 %v", stub.seenMD)
	}
	res, err := s.Push(context.Background(), User("1"), []byte(`{"a":1}`))
	if err != nil || res.Status != Sent || res.Nodes != 1 {
		t.Fatalf("Push 结果不对：%+v %v", res, err)
	}
	if _, err := s.Push(context.Background(), User("1"), []byte(`not json`)); !errors.Is(err, ErrBadPayload) {
		t.Fatalf("非 JSON payload 应 ErrBadPayload，实际 %v", err)
	}
	sess, err := s.Sessions(context.Background(), User("1"))
	if err != nil || len(sess) != 1 || sess[0].ConnID != "c1" || sess[0].OS != "ios" || !sess[0].Mobile || sess[0].ConnectedAt != 9 {
		t.Fatalf("Sessions 不对：%+v %v", sess, err)
	}
}

func TestServerHandlersReceiveInboundAndEvents(t *testing.T) {
	stub, addr, stop := startStub(t)
	defer stop()
	s, _ := NewServer(ServerConfig{Addr: addr, AppID: "a1", AppSecret: "sec", Insecure: true})
	defer s.Close()
	var mu sync.Mutex
	var inbound []Inbound
	var events []Event
	s.OnMessage(func(_ context.Context, in Inbound) error {
		mu.Lock()
		inbound = append(inbound, in)
		mu.Unlock()
		return nil
	})
	s.OnEvent(func(_ context.Context, ev Event) { mu.Lock(); events = append(events, ev); mu.Unlock() })
	waitUntil(t, s.StreamHealthy, "流应就绪")
	stub.push(&fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Inbound{Inbound: &fpimv1.Inbound{Subject: "u:7", ConnId: "c", Payload: []byte(`1`)}}})
	stub.push(&fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Event{Event: &fpimv1.Event{Kind: fpimv1.EventKind_EVENT_KIND_CONNECTED, Subject: "g:6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f", ConnId: "c", Os: "mac", Ua: "x", AtMs: 3}}})
	waitUntil(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(inbound) == 1 && len(events) == 1 }, "应收到 1 条 Inbound 与 1 个事件")
	if inbound[0].Subject != User("7") || string(inbound[0].Payload) != "1" {
		t.Fatalf("Inbound 不对：%+v", inbound[0])
	}
	if events[0].Kind != EventConnected || events[0].Subject.Kind != KindGuest || events[0].OS != "mac" || events[0].At != 3 {
		t.Fatalf("Event 不对：%+v", events[0])
	}
}

func TestServerPushFailsFastWhenStreamDown(t *testing.T) {
	_, addr, stop := startStub(t)
	s, _ := NewServer(ServerConfig{Addr: addr, AppID: "a1", AppSecret: "sec", Insecure: true})
	defer s.Close()
	waitUntil(t, s.StreamHealthy, "流应就绪")
	stop()
	waitUntil(t, func() bool { return !s.StreamHealthy() }, "桩停掉后流应变为不健康")
	if _, err := s.Push(context.Background(), User("1"), []byte(`1`)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("流断开时 Push 应立即 ErrUnavailable，实际 %v", err)
	}
}

// TestServerInFlightPushFailsFastOnDisconnect 守住"流断开时，已经发出去、
// 正在等回应的在途请求要立刻失败"，而不是傻等到 RequestTimeout。
//
// 与 TestServerPushFailsFastWhenStreamDown 的区别：那个测试是在流已经
// 观测为不健康之后才发起 Push，走的是 call() 里"stream==nil 直接拒绝"
// 这条路径，根本碰不到 pending 表的清理逻辑——把 dropStream() 清理
// pending 的代码删掉，那个测试照样通过。这里用 swallow 让桩服务端收到
// 请求后按兵不动，请求真正进入 pending 表、Push 调用方阻塞在等回应上，
// 再停服务端，只有 dropStream 主动关闭 pending 里的 channel 才能把它
// 唤醒。
//
// 断言用 channel + select 加超时上界（1 秒），不用 sleep：RequestTimeout
// 配的是 10 秒，真出现回归（清理被删掉），Push 要等满 10 秒才会因
// ctx.Done() 超时返回，而不是挂到测试结束前才失败——1 秒的上界足以把
// "立刻失败"与"等到超时"两种情形可靠区分开。
func TestServerInFlightPushFailsFastOnDisconnect(t *testing.T) {
	stub, addr, stop := startStub(t)
	s, err := NewServer(ServerConfig{Addr: addr, AppID: "a1", AppSecret: "sec", Insecure: true, RequestTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	waitUntil(t, s.StreamHealthy, "流应就绪")
	stub.swallow.Store(true)

	resultCh := make(chan error, 1)
	go func() {
		_, err := s.Push(context.Background(), User("1"), []byte(`1`))
		resultCh <- err
	}()
	// 等桩服务端真的收到了这条请求，确保它已经进入 pending 表、
	// 处于"在途"状态，而不是还没发出去。
	waitUntil(t, func() bool { return stub.received.Load() >= 1 }, "桩服务端应已收到在途请求")

	stop() // 制造断开：只有 dropStream 清理 pending 才能唤醒上面阻塞的 Push。

	select {
	case err := <-resultCh:
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("在途请求应因流断开返回 ErrUnavailable，实际 %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("流断开后 1 秒内在途 Push 仍未返回——疑似没有清理 pending 请求，" +
			"要一直等到 RequestTimeout(10s) 才会失败")
	}
}
