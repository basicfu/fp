package imgrpc

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/hub"
	"github.com/basicfu/fp/internal/im/hub/hubtest"
	"github.com/basicfu/fp/internal/im/model"
	fpimv1 "github.com/basicfu/fp/sdk/gen/fp/im/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// newClient 起一个跑在 bufconn 上的 Server，返回 client、底层 Hub（测试要用它
// 从"路由器"一侧模拟并发写：h.AddConn 会触发 Deliver 往同一条流写 Event）、
// 以及假注册表 Reg。
func newClient(t *testing.T) (fpimv1.ImServiceClient, *hub.Hub, *hubtest.Reg) {
	t.Helper()
	h, reg, _, _, apps := hubtest.NewHub()
	srv := New(Deps{Hub: h, Apps: apps})
	lis := bufconn.Listen(1 << 20)
	go srv.Serve(lis)
	t.Cleanup(func() { srv.Stop(context.Background()) })
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close() })
	return fpimv1.NewImServiceClient(cc), h, reg
}

func authed(ctx context.Context, id, secret string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, MDAppID, id, MDAppSecret, secret)
}

func TestConnectRejectsBadCredentials(t *testing.T) {
	c, _, _ := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := c.Connect(authed(ctx, "a1", "wrong"))
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("错误凭据应 Unauthenticated，实际 %v", err)
	}
}

func TestConnectReadyThenPushAndSessions(t *testing.T) {
	c, _, reg := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := c.Connect(authed(ctx, "a1", "s"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := stream.Recv()
	if err != nil || first.GetReady() == nil || first.GetReady().NodeId != "im-a" {
		t.Fatalf("第一帧应是 Ready{im-a}，实际 %+v %v", first, err)
	}
	reg.Table["u:1"] = map[string]model.ConnMeta{"c1": {Node: "im-b", OS: "mac"}}
	_ = stream.Send(&fpimv1.ConnectRequest{ReqId: "r1", Body: &fpimv1.ConnectRequest_Push{Push: &fpimv1.PushRequest{Subject: "u:1", Payload: []byte(`1`)}}})
	_ = stream.Send(&fpimv1.ConnectRequest{ReqId: "r2", Body: &fpimv1.ConnectRequest_Sessions{Sessions: &fpimv1.SessionsRequest{Subject: "u:1"}}})
	_ = stream.Send(&fpimv1.ConnectRequest{ReqId: "r3", Body: &fpimv1.ConnectRequest_Push{Push: &fpimv1.PushRequest{Subject: "bogus", Payload: []byte(`1`)}}})
	got := map[string]*fpimv1.Result{}
	for len(got) < 3 {
		resp, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if r := resp.GetResult(); r != nil {
			got[r.ReqId] = r
		}
	}
	if got["r1"].Pushes[0].Status != fpimv1.PushStatus_PUSH_STATUS_SENT || got["r1"].Pushes[0].Nodes != 1 {
		t.Fatalf("r1 应 Sent{1}，实际 %+v", got["r1"])
	}
	if len(got["r2"].Sessions) != 1 || got["r2"].Sessions[0].NodeId != "im-b" || got["r2"].Sessions[0].Os != "mac" {
		t.Fatalf("r2 Sessions 不对：%+v", got["r2"])
	}
	if got["r3"].Error == "" {
		t.Fatal("非法 subject 应以 Result.Error 返回而不是断流")
	}
}

// TestConnectConcurrentWritesDoNotPanic 验证的是功能正确性和"不崩溃"：路由器
// 从别的协程（h.AddConn 触发的 Deliver）往流里写 Event 的同时，服务端为每条
// 收到的请求各自起协程回 Result，响应必须都能按 reqId 配对上，进程不能 panic。
//
// 如实说明这条测试当前能验证到什么程度、不能验证到什么程度：这台机器没有
// cgo，`go test -race` 跑不了；本机实测把 sender.Send 的锁去掉之后，连续跑
// 10 次、把并发量放大到 200×200，一次都没有变红（详见任务报告 4.4 节的
// 回退验证记录）。也就是说这条测试**不能**证明"给 sender 加锁"这个改动是
// 必要的——加锁的必要性来自静态论证：gRPC 文档明确说同一条流不允许并发
// Send，hub.Stream 接口的注释也写了"实现要自己保证 Send 并发安全"。这条
// 测试目前只能确认：（a）路由器与请求处理协程并发写入同一条流时，功能不
// 倒退（响应仍然配对得上）；（b）在当前调度下没有观察到 panic。要真正验证
// 去掉锁会出问题，依赖竞态检测，留待有 cgo 的环境补验。
func TestConnectConcurrentWritesDoNotPanic(t *testing.T) {
	c, h, _ := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := c.Connect(authed(ctx, "a1", "s"))
	if err != nil {
		t.Fatal(err)
	}
	if first, err := stream.Recv(); err != nil || first.GetReady() == nil {
		t.Fatalf("第一帧应是 Ready，实际 %+v %v", first, err)
	}

	const nEvents = 30
	const nReqs = 30

	// 路由器侧：并发调用 AddConn，每次都会经 emit → Deliver → 本地流的 Send，
	// 与下面服务端处理请求的协程共用同一条 snd。
	var wg sync.WaitGroup
	for i := 0; i < nEvents; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conn := &hubtest.Conn{ConnID: fmt.Sprintf("ev-%d", i)}
			h.AddConn(context.Background(), "a1", model.User(fmt.Sprintf("evu%d", i)), conn, model.ConnMeta{OS: "linux"}, "ua")
		}(i)
	}

	// client 侧：不并发调用 stream.Send（gRPC 客户端流本身也不允许并发发送），
	// 而是紧凑地顺序发出多条请求——服务端的 Connect 循环会把每条请求各自
	// 派发到一个协程去处理，处理协程与上面 AddConn 触发的写入天然交织。
	for i := 0; i < nReqs; i++ {
		req := &fpimv1.ConnectRequest{ReqId: fmt.Sprintf("q%d", i), Body: &fpimv1.ConnectRequest_Sessions{Sessions: &fpimv1.SessionsRequest{Subject: "u:1"}}}
		if err := stream.Send(req); err != nil {
			t.Fatalf("Send(q%d): %v", i, err)
		}
	}

	eventsDone := make(chan struct{})
	go func() { wg.Wait(); close(eventsDone) }()
	select {
	case <-eventsDone:
	case <-time.After(5 * time.Second):
		t.Fatal("等待并发 AddConn 完成超时")
	}

	got := map[string]bool{}
	deadline := time.Now().Add(5 * time.Second)
	for len(got) < nReqs && time.Now().Before(deadline) {
		resp, err := stream.Recv()
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if r := resp.GetResult(); r != nil {
			got[r.ReqId] = true
		}
		// Event/Inbound 帧混在同一条流里，忽略即可：这条测试只关心
		// Result 有没有全部配对上、进程有没有 panic。
	}
	for i := 0; i < nReqs; i++ {
		id := fmt.Sprintf("q%d", i)
		if !got[id] {
			t.Fatalf("请求 %s 没有收到响应（可能被并发写入互相踩掉了）", id)
		}
	}
}

// TestConnectReadySentBeforeStreamIsRoutable 验证 Ready 帧与"这条流对路由器
// 可见"这两件事的顺序：Ready 必须先送达 client，之后这条流才应该能被别的
// 协程（其它连接触发的事件、其它请求处理协程）经由 hub 找到并写入。
//
// 复现的是简报点出的真实场景：业务 server 重启重连时，同一个 app 下大量
// 在线连接仍在持续产生事件——用一批一直在自旋调用 h.AddConn 的"路由器噪音"
// 协程模拟这种背景流量，在新连接握手期间片刻不停地尝试往它可能落到的
// 本地流上写 Event。
//
// 只要实现是"先发 Ready 再注册"，这个顺序就有静态保证、不依赖调度：Ready
// 那次 snd.Send 调用在同一个协程里先于 AddStream 完成，而其它协程只有在
// AddStream 返回之后才可能在 h.streams[app] 里找到这条流、进而竞争 sender
// 的锁——此时 Ready 早已经发完、锁也已经释放，其它协程的写入不可能抢到
// 前面。反过来，一旦实现退化成"先注册再发 Ready"，AddStream 返回到本协程
// 真正调用 Send(Ready) 之间就出现一个窗口，只要有协程在这个窗口里抢到锁，
// client 收到的第一帧就会是 Event 而不是 Ready。
func TestConnectReadySentBeforeStreamIsRoutable(t *testing.T) {
	c, h, _ := newClient(t)

	// 一开始试过给每个协程限定一个固定的尝试次数上限：实测发现完全不管用
	// ——"还没注册流"这条路径里 Deliver 判定候选节点为空、静默丢弃，快到
	// 几十万次/秒，一批固定次数的尝试会在 AddStream 真正把流注册进路由器
	// 之前就已经全部跑完退出，等流可见的那一刻早已没有协程还在自旋，完全
	// 撞不上窗口（哪怕人为在 AddStream 和 Send(Ready) 之间插一个 5ms 的
	// sleep 也没用）。改成按墙钟时间限定：deadline 是唯一的硬上限，不管
	// stop 信号有没有及时生效，测试都不会因为背景协程停不下来而挂住；
	// 只要还没到 deadline 且没收到 stop，就一直循环，确保背景噪音能持续
	// 覆盖从连接建立到读到第一帧为止的整个窗口，不会自己提前退场。
	const nRacers = 300
	deadline := time.Now().Add(5 * time.Second)
	stop := make(chan struct{})
	var racers sync.WaitGroup
	for i := 0; i < nRacers; i++ {
		racers.Add(1)
		go func(i int) {
			defer racers.Done()
			for n := 0; time.Now().Before(deadline); n++ {
				select {
				case <-stop:
					return
				default:
				}
				conn := &hubtest.Conn{ConnID: fmt.Sprintf("racer-%d-%d", i, n)}
				h.AddConn(context.Background(), "a1", model.User(fmt.Sprintf("racer%d-%d", i, n)), conn, model.ConnMeta{OS: "linux"}, "ua")
			}
		}(i)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	stream, err := c.Connect(authed(ctx, "a1", "s"))
	if err != nil {
		cancel()
		close(stop)
		racers.Wait()
		t.Fatal(err)
	}
	first, recvErr := stream.Recv()

	// 断言完就立刻叫停背景噪音、取消这条 RPC 的 ctx：还卡在 stream.Send 里
	// 等发送窗口的噪音协程，靠 ctx 取消让底层调用尽快出错返回，不能指望它们
	// 自己看 stop 信号退出（select 只在两次 AddConn 之间的间隙才会被检查到）。
	close(stop)
	cancel()
	waitDone := make(chan struct{})
	go func() { racers.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-time.After(10 * time.Second):
		t.Fatal("等待背景噪音协程收尾超时")
	}

	if recvErr != nil {
		t.Fatalf("Recv 第一帧失败: %v", recvErr)
	}
	if first.GetReady() == nil {
		t.Fatalf("第一帧应是 Ready，实际收到 %+v——说明路由器在 Ready 发出之前就已经能往这条流写东西了", first)
	}
	if first.GetReady().NodeId != "im-a" {
		t.Fatalf("Ready.NodeId 应为 im-a，实际 %q", first.GetReady().NodeId)
	}
}
