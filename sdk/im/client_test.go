package fpim

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeIm 是最小的 ws 端点：读 auth 帧、回 hello、把 msg 原样回推、可按指令关闭。
type fakeIm struct {
	closeWith atomic.Int32 // 非零：握手后立刻以该码关闭
	mu        sync.Mutex
	auths     []map[string]any
	conns     atomic.Int32
	cur       atomic.Pointer[websocket.Conn] // 当前存活的服务端连接，供测试模拟网络切断
}

func (f *fakeIm) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	f.conns.Add(1)
	f.cur.Store(c)
	ctx := r.Context()
	_, b, err := c.Read(ctx)
	if err != nil {
		return
	}
	var af map[string]any
	_ = json.Unmarshal(b, &af)
	f.mu.Lock()
	f.auths = append(f.auths, af)
	f.mu.Unlock()
	if code := f.closeWith.Load(); code != 0 {
		_ = c.Close(websocket.StatusCode(code), "test")
		return
	}
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"t":"hello","conn":"c1"}`))
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			return
		}
		var fr struct {
			T string          `json:"t"`
			P json.RawMessage `json:"p"`
		}
		_ = json.Unmarshal(b, &fr)
		if fr.T == "msg" {
			_ = c.Write(ctx, websocket.MessageText, []byte(`{"t":"msg","p":`+string(fr.P)+`}`))
		}
		if fr.T == "ping" {
			_ = c.Write(ctx, websocket.MessageText, []byte(`{"t":"pong"}`))
		}
	}
}

// dropCurrentConn 模拟网络连接被意外切断：直接砍断服务端持有的底层连接，
// 不发送任何关闭帧。
//
// 不用 httptest.Server.CloseClientConnections：websocket.Accept 靠
// http.Hijacker 拿到裸 net.Conn 之后，标准库 net/http/httptest 会在
// ConnState 回调里把这条连接标记为 StateHijacked，并把它从
// httptest.Server 自己的跟踪表里摘掉（net/http/httptest/server.go 的
// ConnState 处理，StateHijacked 分支会 delete(s.conns, c)）——
// CloseClientConnections 遍历的正是这张表，对已升级为 websocket 的连接
// 是彻底的空操作。用一个独立的最小复现程序验证过：httptest 场景下调用
// CloseClientConnections 之后，client 侧的 ws.Read 仍然永久阻塞，不会
// 返回任何错误。所以这里换成直接操作服务端持有的 *websocket.Conn，
// CloseNow 会立即断开底层 TCP 而不走优雅关闭握手，效果上等价于一次
// 真实的网络中断。
func (f *fakeIm) dropCurrentConn() {
	if c := f.cur.Load(); c != nil {
		_ = c.CloseNow()
	}
}

func (f *fakeIm) authsSnapshot() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]any, len(f.auths))
	copy(out, f.auths)
	return out
}

func newFake(t *testing.T) (*fakeIm, string) {
	f := &fakeIm{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestDialSendsAuthFrameAndEchoes(t *testing.T) {
	f, url := newFake(t)
	got := make(chan []byte, 1)
	c, err := Dial(context.Background(), ClientConfig{URL: url, App: "a1", Token: "tok", OS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.OnMessage(func(p []byte) { got <- p })
	auths := f.authsSnapshot()
	if len(auths) != 1 || auths[0]["t"] != "auth" || auths[0]["app"] != "a1" || auths[0]["token"] != "tok" || auths[0]["os"] != "linux" {
		t.Fatalf("握手帧不对：%v", auths)
	}
	if err := c.Send(context.Background(), []byte(`{"hi":1}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-got:
		if string(p) != `{"hi":1}` {
			t.Fatalf("回显不对：%s", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("5 秒内没收到回显")
	}
	if err := c.Send(context.Background(), []byte(`nope`)); !errors.Is(err, ErrBadPayload) {
		t.Fatalf("非 JSON 应 ErrBadPayload，实际 %v", err)
	}
}

func TestDialFailsOn4001AndDoesNotReconnect(t *testing.T) {
	f, url := newFake(t)
	f.closeWith.Store(CloseAuthFailed)
	_, err := Dial(context.Background(), ClientConfig{URL: url, App: "a1", Token: "bad"})
	if err == nil {
		t.Fatal("4001 应让 Dial 失败")
	}
	// 这条是"确认没有重连"的否定断言：只能靠等一小段时间后查计数来证伪，
	// 没有事件可等——不代表真的等满这段时间就能证明"永远不会重连"，只是
	// 观察窗口内没有发生。这与要求里"轮询/channel 代替 sleep"的原则不矛盾：
	// 那条原则是为了避免用 sleep 等一个本该用 channel 通知的正向条件，而这里
	// 要等的东西恰恰是"没有发生"，没有 channel 可等。
	time.Sleep(300 * time.Millisecond)
	if f.conns.Load() != 1 {
		t.Fatalf("认证失败不能自动重连，实际连接了 %d 次", f.conns.Load())
	}
}

func TestClientReconnectsAfterServerDropAndReportsClose(t *testing.T) {
	f, url := newFake(t)
	c, err := Dial(context.Background(), ClientConfig{URL: url, App: "a1", Guest: "6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	codes := make(chan int, 4)
	c.OnClose(func(code int) { codes <- code })
	f.dropCurrentConn()
	select {
	case <-codes:
	case <-time.After(5 * time.Second):
		t.Fatal("连接被切断后 OnClose 应被调用")
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.conns.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("切断后应自动重连")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestSendAfterCloseReturnsErrUnavailable 覆盖"关闭之后再发送"这条并发
// 契约：不能 panic、不能挂起，必须返回一个明确的、调用方能判断的错误。
func TestSendAfterCloseReturnsErrUnavailable(t *testing.T) {
	_, url := newFake(t)
	c, err := Dial(context.Background(), ClientConfig{URL: url, App: "a1", Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Send(context.Background(), []byte(`{}`)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Close 之后 Send 应返回 ErrUnavailable，实际 %v", err)
	}
}
