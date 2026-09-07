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
	hellos    atomic.Int32                   // 成功写出 hello 的次数：确认某次握手真正完成，比只看 conns（刚 Accept）更准
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
	if c.Write(ctx, websocket.MessageText, []byte(`{"t":"hello","conn":"c1"}`)) == nil {
		f.hellos.Add(1)
	}
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

// closeCurrentConnWithCode 在已建立连接之后，以给定关闭码优雅关闭服务端
// 持有的连接（不像 dropCurrentConn 那样是硬断，而是带上一个具体的关闭
// 码），用于测试"握手已经成功、连接已经在正常使用一段时间之后，才收到
// 某个关闭码"这条真实路径——这与"握手阶段就被拒绝"（TestDialFailsOn4001
// AndDoesNotReconnect 覆盖的那条路径）是两码事：那条路径在 connect() 里
// 直接返回错误，根本进不了 runLoop 的重连循环；这里模拟的是连接已经跑起
// 来、readUntilClosed 正在阻塞读的时候才收到关闭帧。
func (f *fakeIm) closeCurrentConnWithCode(code int) {
	if c := f.cur.Load(); c != nil {
		_ = c.Close(websocket.StatusCode(code), "test")
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

// waitHellos 轮询等待 hellos 计数达到 n，超时则让测试失败。用它而不是
// sleep，是因为"握手完成"本身就有一个可观察的信号（服务端成功写出
// hello），没有理由退化成猜一个足够长的睡眠时间。
func waitHellos(t *testing.T, f *fakeIm, n int32, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for f.hellos.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("等待第 %d 次握手完成超时，实际 hellos=%d", n, f.hellos.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestClientDoesNotReconnectOnKickedAfterEstablishedConnection 覆盖"连接
// 已经建立并正常用了一段时间之后，才收到不可重连的关闭码"这条路径。
//
// 与 TestDialFailsOn4001AndDoesNotReconnect 是两条不同的代码路径：那条
// 测试里服务端在发 hello 回执之前就关闭，client 的 connect() 直接返回
// 错误，Dial 直接失败，根本进不了 runLoop 的重连循环——那条判断守的是
// "握手期间被拒"。这里握手先正常走完（Dial 成功返回），之后才通过已建立
// 的连接收到关闭帧，真正走到 runLoop 里 `noAutoReconnect(code)` 那个
// 判断。4001/4002/4003 三个码在 runLoop 里走的是同一行判断
// （noAutoReconnect 一次性判断三者），这里只挑其中一个（被踢，
// CloseKicked）代表性验证，另外两个不必重复：只要证明"判断本身在起
// 作用"，不需要对每个码各测一遍同一行代码。
func TestClientDoesNotReconnectOnKickedAfterEstablishedConnection(t *testing.T) {
	f, url := newFake(t)
	c, err := Dial(context.Background(), ClientConfig{URL: url, App: "a1", Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	waitHellos(t, f, 1, 2*time.Second) // 确认握手已经真正完成，不是还在半路上

	codes := make(chan int, 4)
	c.OnClose(func(code int) { codes <- code })
	f.closeCurrentConnWithCode(CloseKicked)

	select {
	case got := <-codes:
		if got != CloseKicked {
			t.Fatalf("OnClose 应收到 CloseKicked(%d)，实际 %d", CloseKicked, got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("已建立连接被以 4003 关闭后，OnClose 应被调用")
	}

	// 这条是"确认没有重连"的否定断言，同 TestDialFailsOn4001AndDoesNotReconnect
	// 的注释：只能靠等一小段时间后查计数来证伪，没有事件可等。
	time.Sleep(300 * time.Millisecond)
	if f.conns.Load() != 1 {
		t.Fatalf("被踢（4003）不能自动重连，实际连接了 %d 次", f.conns.Load())
	}
}

// TestIdleTimeoutReconnectsImmediatelyUnlikeUnavailable 覆盖"4005 立即
// 重连、不退避"这条契约：对比同一个 Client 先后收到 4005 和 4004 时，
// 从关闭到下一次握手完成之间的耗时——4005 应当明显更快，因为它跳过了
// 退避等待，4004 至少要等满一个 minBackoff（200ms）。
//
// 用耗时的相对比较和一个宽松的绝对上界，而不是断言某个精确值：本机的
// 调度、localhost 的 TCP 握手开销都会引入抖动，只要 4005 明显快于
// 200ms 这个退避下限、且明显快于 4004 那一次，就足以证明"跳过了退避"，
// 不需要卡在一个可能因为负载而偶尔失败的精确阈值上。
func TestIdleTimeoutReconnectsImmediatelyUnlikeUnavailable(t *testing.T) {
	f, url := newFake(t)
	c, err := Dial(context.Background(), ClientConfig{URL: url, App: "a1", Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	waitHellos(t, f, 1, 2*time.Second)

	idleStart := time.Now()
	f.closeCurrentConnWithCode(CloseIdleTimeout)
	waitHellos(t, f, 2, 5*time.Second)
	idleElapsed := time.Since(idleStart)

	unavailStart := time.Now()
	f.closeCurrentConnWithCode(CloseUnavailable)
	waitHellos(t, f, 3, 5*time.Second)
	unavailElapsed := time.Since(unavailStart)

	// 4005 应该远远快于 minBackoff（200ms）：留足够宽的余量（150ms）
	// 给本机调度/localhost 握手开销，避免在负载较高的机器上误报。
	if idleElapsed > 150*time.Millisecond {
		t.Fatalf("4005 应立即重连、不退避，实际耗时 %v", idleElapsed)
	}
	// 4004 至少要经过一次 minBackoff（200ms）的等待。
	if unavailElapsed < 200*time.Millisecond {
		t.Fatalf("4004 应该退避后再重连，实际耗时 %v 明显短于 200ms 的最小退避", unavailElapsed)
	}
	if idleElapsed >= unavailElapsed {
		t.Fatalf("4005 的重连应明显快于 4004：idle=%v unavailable=%v", idleElapsed, unavailElapsed)
	}
}

// TestClientReportsGivingUpDuringReconnect 是甲四的回归测试：重连尝试
// 阶段撞上不可重连的关闭码（认证失败 / 策略拒绝 / 被踢）时，client 必须
// 让调用方能发现自己已经永久放弃，而不是一声不吭地停掉。
//
// 缺陷版本的行为：重连时收到这三个码直接 return——不调用关闭回调、不置
// 任何状态位、也没有状态查询方法。调用方最后看到的是上一次断开的码
// （节点崩溃时是 -1），之后 client 永远不再重连，Send 只会返回底层库的
// 原始错误。设计文档第四节把"节点崩溃到被判死之间被拒一次"的缓解写成
// "客户端的重连退避会自然跨过这个窗口"，对采用 reject/limit 策略的应用
// 这句话是错的：该窗口内重连拿到 4002，client 就此永久死掉。
func TestClientReportsGivingUpDuringReconnect(t *testing.T) {
	f, url := newFake(t)
	c, err := Dial(context.Background(), ClientConfig{URL: url, App: "a1", Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	waitHellos(t, f, 1, 2*time.Second)

	codes := make(chan int, 4)
	c.OnClose(func(code int) { codes <- code })
	// 下一次握手会被以 4002 拒绝，模拟"节点崩溃、旧连接还没被判死，重连
	// 撞上连接策略"这个窗口。
	f.closeWith.Store(ClosePolicyRejected)
	// 先用一个可重连的码断开，让 client 进入重连循环。
	f.closeCurrentConnWithCode(CloseUnavailable)

	select {
	case got := <-codes:
		if got != CloseUnavailable {
			t.Fatalf("第一次断开应回调 4004，实际 %d", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("连接被以 4004 关闭后 OnClose 应被调用")
	}
	select {
	case got := <-codes:
		if got != ClosePolicyRejected {
			t.Fatalf("重连被 4002 拒绝时应把这个码回调给调用方，实际 %d", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("重连撞上不可重连的关闭码时必须通知调用方：" +
			"不通知的话调用方看到的还是上一次断开的码，而 client 已经永久不再重连")
	}
	waitUntilTrue(t, c.GaveUp, "放弃自动重连之后 GaveUp() 必须为真，调用方才能查出来")
}

// TestClientGaveUpFalseWhileHealthyAndAfterClose 钉住 GaveUp 的语义边界：
// 它只反映"自动重连被永久放弃"，正常连着的时候是假，调用方自己 Close 的
// 也不算放弃（那是调用方自己的决定，不需要 SDK 再报告一次）。
func TestClientGaveUpFalseWhileHealthyAndAfterClose(t *testing.T) {
	f, url := newFake(t)
	c, err := Dial(context.Background(), ClientConfig{URL: url, App: "a1", Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	waitHellos(t, f, 1, 2*time.Second)
	if c.GaveUp() {
		t.Fatal("连接正常时 GaveUp() 应为假")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if c.GaveUp() {
		t.Fatal("调用方自己 Close 不算放弃自动重连，GaveUp() 应仍为假")
	}
}

// TestDialSendsTokenKind 覆盖 Kind: TokenKindBiz 时握手帧真的带上了
// kind: "biz"——网关侧靠这个字段决定走 fp 认证还是业务方回调，字段名/值
// 拼错网关会直接按缺省当成 fp 处理，业务方令牌就会被错误地送去 fp 认证。
func TestDialSendsTokenKind(t *testing.T) {
	f, url := newFake(t)
	c, err := Dial(context.Background(), ClientConfig{URL: url, App: "a1", Token: "tok", Kind: TokenKindBiz})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	auths := f.authsSnapshot()
	if len(auths) != 1 || auths[0]["kind"] != "biz" {
		t.Fatalf("握手帧里的令牌类型不对：%v", auths)
	}
}

// TestDialOmitsKindWhenEmpty 覆盖不设 Kind 时握手帧里根本不带 kind 这个
// 键（而不是带一个空串）：多发一个无意义的键会在抓包/日志里造成噪音，
// 且让"缺省当成 fp"这条契约变得不那么直观。
func TestDialOmitsKindWhenEmpty(t *testing.T) {
	f, url := newFake(t)
	c, err := Dial(context.Background(), ClientConfig{URL: url, App: "a1", Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	auths := f.authsSnapshot()
	if _, present := auths[0]["kind"]; present {
		t.Fatalf("没设类型时不该发 kind 字段，实际 %v", auths[0])
	}
}

// waitUntilTrue 轮询等待条件成立，超时即失败。
func waitUntilTrue(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
