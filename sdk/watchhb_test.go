package fpsdk

import (
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// hbStub 读客户端消息: 每收到一个 ping 计数, 并最多回 pongs 个 pong(-1 = 不限); 记下建流次数。
type hbStub struct {
	stubServer
	pongs   atomic.Int64
	pings   atomic.Int64
	streams atomic.Int64
}

func (s *hbStub) Watch(stream grpc.BidiStreamingServer[fpv1.WatchRequest, fpv1.WatchResponse]) error {
	s.streams.Add(1)
	if err := stream.Send(&fpv1.WatchResponse{Event: &fpv1.WatchResponse_Ready{Ready: &fpv1.WatchReady{}}}); err != nil {
		return err
	}
	for {
		req, err := stream.Recv()
		if err != nil {
			return nil
		}
		if req.GetPing() == nil {
			continue
		}
		s.pings.Add(1)
		if n := s.pongs.Load(); n == 0 {
			continue // 不回: 模拟卡死的流, 或根本不认识 ping 的老服务端
		} else if n > 0 {
			s.pongs.Add(-1)
		}
		if err := stream.Send(&fpv1.WatchResponse{Event: &fpv1.WatchResponse_Pong{Pong: &fpv1.WatchPong{}}}); err != nil {
			return err
		}
	}
}

func newHBClient(t *testing.T, stub *hbStub) {
	t.Helper()
	stub.stubServer = stubServer{validate: okValidate("u1", 1000)}
	addr, stop := startStub(t, "", stub)
	c, err := New(Options{Addr: "grpc://" + addr, AppID: "t", AppSecret: "t", watchHeartbeatInterval: 30 * time.Millisecond})
	if err != nil {
		stop()
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(); stop() })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("等不到: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// 服务端一直回 pong: 持续发心跳, 流不会被重建。
func TestWatchHeartbeatKeepsStreamWhenAnswered(t *testing.T) {
	stub := &hbStub{}
	stub.pongs.Store(-1)
	newHBClient(t, stub)
	waitFor(t, "至少 4 个心跳", func() bool { return stub.pings.Load() >= 4 })
	if n := stub.streams.Load(); n != 1 {
		t.Fatalf("一直有应答, 不该重建流: 建流 %d 次", n)
	}
}

// 回过 pong 之后不再回(流卡死): 超过 watchHeartbeatMisses 个间隔就主动重建流。
func TestWatchHeartbeatRebuildsStuckStream(t *testing.T) {
	stub := &hbStub{}
	stub.pongs.Store(2)
	newHBClient(t, stub)
	waitFor(t, "卡死后重建流", func() bool { return stub.streams.Load() >= 2 })
}

// 老服务端不认识 ping、永远不回: 只管发, 绝不能因此每分钟重建一次流。
func TestWatchHeartbeatNeverKillsOldServer(t *testing.T) {
	stub := &hbStub{} // pongs = 0: 从不回
	newHBClient(t, stub)
	waitFor(t, "至少 8 个心跳", func() bool { return stub.pings.Load() >= 8 })
	if n := stub.streams.Load(); n != 1 {
		t.Fatalf("没回过 pong 就不判死, 不该重建流: 建流 %d 次", n)
	}
}
