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

// TestConnectConcurrentWritesDoNotPanic 验证 sender 那层锁：路由器从别的协程
// （h.AddConn 触发的 Deliver）往流里写 Event 的同时，服务端为每条收到的请求
// 各自起协程回 Result，两拨写入者必须都经过同一把锁串行化，不能互相踩踏
// 导致 gRPC 底层的并发 Send panic，响应也必须都能按 reqId 配对上。
//
// 这台机器没有 cgo，`go test -race` 跑不了，所以这条测试只能在当前调度下
// 反复制造写入交织的时间窗口，测不出"极小概率才复现"的数据竞争；它能可靠
// 验证的是"去掉锁包装后，在这个数量级的并发下确定性地 panic 或响应错乱"，
// 详见任务报告里的回退验证记录。
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
