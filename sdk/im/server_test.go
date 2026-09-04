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

// TestServerPushInsideOnMessageDoesNotDeadlock 守住修复批次 2 问题一：
// OnMessage 回调里直接同步调用 Push（不开 goroutine）必须能正常拿到
// 结果，而不是把 Server 内部读循环自己堵死。
//
// 这是 examples/im-demo 能不能写成"最直白的同步写法"的验收标准本身：
// 如果这条测试红了，说明 Inbound 又被塞回了读循环同步处理，回调里的
// Push 会因为等不到 Result（Result 也只能靠这同一个循环读到）卡满
// RequestTimeout。断言用 500ms 的上界（远小于下面配的 3s
// RequestTimeout）区分"立刻拿到结果"与"卡死等超时"两种情形，不用
// sleep 等条件成立。
//
// 回退验证：把 runOnce 里 Inbound 分支从 s.enqueue(...) 改回直接调用
// s.handleInbound(ctx, b.Inbound)（修复前的写法），本测试按预期变红，
// 报错为"回调里同步调用 Push 应该在 500ms 内返回"；改回 enqueue 后
// 复测转绿。过程记在 fix-batch-2-report.md。
func TestServerPushInsideOnMessageDoesNotDeadlock(t *testing.T) {
	stub, addr, stop := startStub(t)
	defer stop()
	s, err := NewServer(ServerConfig{Addr: addr, AppID: "a1", AppSecret: "sec", Insecure: true, RequestTimeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	resultCh := make(chan PushResult, 1)
	errCh := make(chan error, 1)
	s.OnMessage(func(ctx context.Context, in Inbound) error {
		// 故意不开 goroutine：这正是 examples/im-demo 想写、也应该能写的
		// "最直白"版本。如果这里会自死锁，示例就必须继续绕开协程写法，
		// 说明修复没到位。
		res, pushErr := s.Push(ctx, in.Subject, in.Payload)
		if pushErr != nil {
			errCh <- pushErr
			return pushErr
		}
		resultCh <- res
		return nil
	})
	waitUntil(t, s.StreamHealthy, "流应就绪")
	stub.push(&fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Inbound{Inbound: &fpimv1.Inbound{Subject: "u:1", ConnId: "c", Payload: []byte(`1`)}}})

	select {
	case res := <-resultCh:
		if res.Status != Sent {
			t.Fatalf("回调里同步 Push 应该成功送达，实际状态 %v", res.Status)
		}
	case err := <-errCh:
		t.Fatalf("回调里同步调用 Push 不应该报错，实际 %v", err)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("回调里同步调用 Push 应该在 500ms 内返回，实际没有——" +
			"疑似 Inbound 又被塞回了读循环同步处理，读循环被自己的回调堵住（自死锁），" +
			"要一直等到 RequestTimeout 才会因超时返回")
	}
}

// TestServerPreservesEventBeforeMessageOrder 守住修复批次 2 问题一的第二条
// 约束：Inbound 与 Event 必须共用同一条队列。
//
// 构造"先事件、后消息"的序列（与 hub 的真实保证一致：连接建立事件严格
// 早于该连接的第一条消息），并且让 OnEvent 回调故意卡住（等测试放行）。
// 只要 Inbound 与 Event 真的共用同一条队列、由同一个单消费者按序处理，
// 消息就绝不可能在事件回调返回之前被观测到——它俩共用一个"queue 位置"，
// 消息排在事件后面，必须等前面处理完。用一个 200ms 的 channel+超时窗口
// 确认这一点，而不是假设"消息永远不会先到"（有限时间里没法断言永远，
// 但两条毫无阻塞的纯内存 channel 操作用不了 200ms，200ms 足够暴露反例，
// 不会因为调度延迟产生误报）。
//
// 回退验证：把 runOnce 里 Event 分支临时改成 `go s.handleEvent(ctx,
// b.Event)`（消息仍走队列，但事件绕开队列、自己开一个不受队列约束的
// goroutine——这是"事件不走同一条队列"最贴近真实的一种误修法）。用这个
// 变体复跑，本测试如期变红：消息回调在 200ms 内被调用，先于事件回调
// 返回。改回共享队列后复测转绿。过程记在 fix-batch-2-report.md。
//
// 注意：这条测试**只**能抓住"事件不走队列"这个方向的违规。如果改成
// "事件完全同步内联在读循环里处理"（不开 goroutine、也不入队），由于
// 读循环是唯一的读取者，事件回调必然在读到下一帧（这里是消息）之前就
// 跑完，这个方向的测试构造下不可能观测到"消息先于事件"——这不代表
// 完全同步内联没有问题，只代表这条测试的构造方向抓不住它。另一个方向
// （消息先到、事件后到，例如 client 发完最后一条消息随即断开）由镜像
// 测试 TestServerPreservesMessageBeforeEventOrder 覆盖：完全同步内联在
// 那个方向下会让事件抢在还没处理完的消息前面被观测到，能被抓住。两条
// 测试合起来才完整覆盖"事件与消息谁先到、谁就该先被观测到"这条双向
// 契约。
func TestServerPreservesEventBeforeMessageOrder(t *testing.T) {
	stub, addr, stop := startStub(t)
	defer stop()
	s, _ := NewServer(ServerConfig{Addr: addr, AppID: "a1", AppSecret: "sec", Insecure: true})
	defer s.Close()

	var mu sync.Mutex
	var order []string
	release := make(chan struct{})
	// releaseNow 用 sync.Once 包一层、且用 defer 兜底：如果下面的
	// select 因为断言失败走 t.Fatal（会 runtime.Goexit，跳过它后面的
	// 正常收尾代码），必须保证 release 最终还是会被关掉——否则 OnEvent
	// 回调永远卡在 <-release 里，而它是被 consumeLoop 调用的，Close()
	// 的 wg.Wait() 会等这个消费协程退出，永远等不到，把整个测试拖入
	// 死锁而不是干净地报一个 FAIL（回退验证时曾经真的因为漏了这个兜底
	// 卡死过，教训写在这里）。defer 在这条语句之后注册，比更早注册的
	// `defer s.Close()` 后出栈，保证释放顺序总是"先放行回调、再关闭"。
	var releaseOnce sync.Once
	releaseNow := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseNow()
	msgDone := make(chan struct{})
	s.OnEvent(func(_ context.Context, ev Event) {
		<-release // 卡住，模拟"还没处理完"的事件回调
		mu.Lock()
		order = append(order, "event")
		mu.Unlock()
	})
	s.OnMessage(func(_ context.Context, in Inbound) error {
		mu.Lock()
		order = append(order, "message")
		mu.Unlock()
		close(msgDone)
		return nil
	})
	waitUntil(t, s.StreamHealthy, "流应就绪")

	stub.push(&fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Event{Event: &fpimv1.Event{Kind: fpimv1.EventKind_EVENT_KIND_CONNECTED, Subject: "u:1", ConnId: "c", AtMs: 1}}})
	stub.push(&fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Inbound{Inbound: &fpimv1.Inbound{Subject: "u:1", ConnId: "c", Payload: []byte(`1`)}}})

	select {
	case <-msgDone:
		t.Fatal("消息不应该在事件回调返回之前被观测到——Inbound 与 Event 必须共用" +
			"同一条队列、由同一个单消费者按入队顺序处理，消息不能绕过还没处理完的" +
			"事件抢先被处理")
	case <-time.After(200 * time.Millisecond):
		// 符合预期：消息还卡在事件后面，尚未被处理。
	}

	releaseNow() // 放行事件回调
	waitUntil(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(order) == 2
	}, "事件与消息都应该被处理")

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	if got[0] != "event" || got[1] != "message" {
		t.Fatalf("事件应先于消息被观测到，实际顺序 %v", got)
	}
}

// TestServerPreservesMessageBeforeEventOrder 是
// TestServerPreservesEventBeforeMessageOrder 的镜像：反过来构造"先消息、
// 后事件"的序列，断言事件不能抢在还没处理完的消息前面被观测到。
//
// 这个方向对应一个很常见的真实场景：client 发完最后一条消息随即断开
// 连接，于是入站消息后面紧跟着这条连接的断开事件。如果事件抢先被观测
// 到，业务方按"收到断开事件就清理会话状态"的常见写法，会在会话状态
// 已经被清理之后才收到那条消息，相当于把消息丢进了一个已经不存在的
// 上下文——这正是"事件与消息共用同一条队列"这条约束真正要防的生产
// 事故之一，不是只为了让测试好看。
//
// 这条测试与上面那条互补、缺一不可：上面那条测试的构造方向（先事件后
// 消息）抓不住"事件完全同步内联处理"这一类违规（读循环是唯一读取者，
// 事件必然先于下一帧被处理完，无从制造反例）；这条测试反过来，能够
// 让"事件完全同步内联处理"现出原形——消息卡在队列里等消费协程处理，
// 而同步内联的事件不需要排队，会在读循环里立刻被处理并被业务侧观测到。
//
// 回退验证：把 runOnce 里 Event 分支临时改成 `s.handleEvent(ctx,
// b.Event)`（完全同步内联，不开 goroutine、也不入队——上面那条测试的
// 回退验证特意说明这个变体抓不住它，这里反过来验证它能被这条测试抓
// 住），本测试如期变红：事件回调在 200ms 内被调用完成，抢在还在卡住
// 的消息回调前面。同时确认上面那条测试在这个变体下仍然是绿的（两条
// 测试互补：各自只覆盖一个方向，合起来才完整覆盖双向的顺序契约）。
// 改回共享队列后两条复测都转绿。过程记在 fix-batch-2-report.md。
func TestServerPreservesMessageBeforeEventOrder(t *testing.T) {
	stub, addr, stop := startStub(t)
	defer stop()
	s, _ := NewServer(ServerConfig{Addr: addr, AppID: "a1", AppSecret: "sec", Insecure: true})
	defer s.Close()

	var mu sync.Mutex
	var order []string
	release := make(chan struct{})
	// releaseNow 的 sync.Once+defer 兜底，理由与
	// TestServerPreservesEventBeforeMessageOrder 里同名变量完全一样：
	// 这里卡住的是 OnMessage 回调，它由 consumeLoop 调用，Close() 的
	// wg.Wait() 会等这个协程退出——如果下面的 select 因为断言失败走
	// t.Fatal（Goexit，跳过后面显式的 close(release)），必须靠 defer
	// 兜底把它放行，否则整个测试会卡死在 defer s.Close() 上，而不是
	// 干净地报一个 FAIL。这个坑在验证这条测试本身时真的踩过一次。
	var releaseOnce sync.Once
	releaseNow := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseNow()
	eventDone := make(chan struct{})
	s.OnMessage(func(_ context.Context, in Inbound) error {
		<-release // 卡住，模拟"还没处理完"的消息回调
		mu.Lock()
		order = append(order, "message")
		mu.Unlock()
		return nil
	})
	s.OnEvent(func(_ context.Context, ev Event) {
		mu.Lock()
		order = append(order, "event")
		mu.Unlock()
		close(eventDone)
	})
	waitUntil(t, s.StreamHealthy, "流应就绪")

	// 真实场景：client 发完最后一条消息随即断开，消息先到、断开事件
	// 紧随其后。
	stub.push(&fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Inbound{Inbound: &fpimv1.Inbound{Subject: "u:1", ConnId: "c", Payload: []byte(`1`)}}})
	stub.push(&fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Event{Event: &fpimv1.Event{Kind: fpimv1.EventKind_EVENT_KIND_DISCONNECTED, Subject: "u:1", ConnId: "c", Reason: "closed", AtMs: 2}}})

	select {
	case <-eventDone:
		t.Fatal("事件不应该在消息回调返回之前被观测到——Inbound 与 Event 必须共用" +
			"同一条队列、由同一个单消费者按入队顺序处理，事件不能绕过还没处理完的" +
			"消息抢先被处理（真实场景：client 发完最后一条消息随即断开，业务侧若" +
			"提前看到断开事件清理了会话状态，随后到的消息就会被丢进一个已经清理" +
			"掉的上下文）")
	case <-time.After(200 * time.Millisecond):
		// 符合预期：事件还卡在消息后面，尚未被处理。
	}

	releaseNow() // 放行消息回调
	waitUntil(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(order) == 2
	}, "消息与事件都应该被处理")

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	if got[0] != "message" || got[1] != "event" {
		t.Fatalf("消息应先于事件被观测到，实际顺序 %v", got)
	}
}

// TestServerDropsFramesWhenQueueFull 守住修复批次 2 问题一的溢出约束：
// 入队必须非阻塞，队列满了要丢弃并计数、而不是静默卡住或阻塞读循环。
//
// 用一个永远不放行的回调把消费协程卡死在第一条消息上，再抢着推
// frameQueueCap（256）之外的一批消息：这些消息在队列已满之后仍然要
// 能被读循环正常收下（stub.push 走的是同步 st.Send，如果 enqueue 会
// 阻塞，读循环会卡在这次入队上，读不出后面的帧，stub.push 本身也可能
// 因为 gRPC 流控窗口写满而跟着卡住——所以能顺利推完这一批，本身就是
// "入队非阻塞"这条约束成立的证据），多出的部分应该被丢弃并计入
// DroppedFrames。
//
// 回退验证：把 enqueue 里的 `select { case s.frames <- f: default: ... }`
// 改回无条件阻塞发送 `s.frames <- f`，本测试按预期变红——
// waitUntil 等 DroppedFrames()>0 会一直等到 5 秒超时失败（丢弃计数永远
// 停在 0，因为阻塞发送不会走到 default 分支），而不是这里断言的"很快
// 就能观测到丢弃"。改回 select+default 后复测转绿。过程记在
// fix-batch-2-report.md。
func TestServerDropsFramesWhenQueueFull(t *testing.T) {
	stub, addr, stop := startStub(t)
	defer stop()
	s, _ := NewServer(ServerConfig{Addr: addr, AppID: "a1", AppSecret: "sec", Insecure: true})
	defer s.Close()

	started := make(chan struct{})
	var once sync.Once
	block := make(chan struct{}) // 故意不主动关闭：让消费协程死死卡在第一条消息上
	// blockOnce+defer 兜底：中间任何一个 waitUntil/t.Fatalf 提前失败
	// （Goexit，跳过下面显式的 close(block)），都必须保证 block 最终
	// 被放行——否则 consumeLoop 永远卡在这个回调里，defer s.Close() 的
	// wg.Wait() 等不到它退出，会把测试拖入死锁而不是干净地报 FAIL
	// （这个坑在验证同一批的另外两条顺序测试时真实踩过一次，这里回头
	// 一并加固，理由与那两条测试里的 releaseNow 完全一样）。
	var blockOnce sync.Once
	unblock := func() { blockOnce.Do(func() { close(block) }) }
	defer unblock()
	s.OnMessage(func(_ context.Context, in Inbound) error {
		once.Do(func() { close(started) })
		<-block
		return nil
	})
	waitUntil(t, s.StreamHealthy, "流应就绪")

	// 第一条：占住消费协程。
	stub.push(&fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Inbound{Inbound: &fpimv1.Inbound{Subject: "u:1", ConnId: "c", Payload: []byte(`0`)}}})
	waitUntil(t, func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	}, "消费协程应已开始处理第一条消息（卡在回调里）")

	// 消费协程卡住不动，队列此刻是空的（第一条已经被取走，只是回调没
	// 返回）。再推 frameQueueCap+一批，前 frameQueueCap 条能填满队列，
	// 剩下的应该被丢弃。
	const extra = frameQueueCap + 40
	for i := 0; i < extra; i++ {
		stub.push(&fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Inbound{Inbound: &fpimv1.Inbound{Subject: "u:1", ConnId: "c", Payload: []byte(`1`)}}})
	}
	wantDropped := int64(extra - frameQueueCap)
	waitUntil(t, func() bool { return s.DroppedFrames() >= wantDropped }, "队列打满后应该有帧被丢弃并计数")
	if got := s.DroppedFrames(); got != wantDropped {
		t.Fatalf("丢弃计数不对：want %d, got %d", wantDropped, got)
	}
	unblock() // 收尾：放行卡住的回调，避免消费协程带着阻塞状态进 Close
}
