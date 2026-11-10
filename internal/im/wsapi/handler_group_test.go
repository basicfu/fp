package wsapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/hub"
	"github.com/basicfu/fp/internal/im/hub/hubtest"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/redisx"
	"github.com/basicfu/fp/internal/im/registry"
	"github.com/basicfu/fp/internal/testsupport"
	"github.com/coder/websocket"
)

// liveList 是可配置存活节点的 Liveness，本节点恒为 im-a。
type liveList []string

func (liveList) NodeID() string        { return "im-a" }
func (l liveList) LiveNodes() []string { return l }

// newRedisEnv 与 newEnv 相同，但握手走真实的 registry.Conns：分组策略
// 测的是"脚本按哪个策略裁决"，用假 Handshaker 就只是在测假实现自己。
func newRedisEnv(t *testing.T, policy model.Policy, ids map[string]auth.Identity) (*env, *registry.Conns) {
	t.Helper()
	rdb := testsupport.NewTestRedis(t)
	run, err := redisx.NewRunner(rdb, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(run.Close)
	conns := registry.NewConns(rdb, run, 30*time.Minute)

	apps := hubtest.Apps{"a1": {AppID: "a1", ConnPolicy: policy}}
	h, _, _, _ := hubtest.NewHubWithApps(apps)
	stream := &hubtest.Stream{}
	h.AddStream(context.Background(), "a1", stream)
	fa := &fakeAuth{byToken: map[string]model.Subject{}, ids: ids}
	handler := New(Deps{Hub: h, Conns: conns, Live: liveList{"im-a", "im-b"}, Auth: fa, Apps: apps, Guests: NewGuestLimiter(),
		Cfg: Config{AuthTimeout: 2 * time.Second, IdleTimeout: 10 * time.Second, SendQueue: 8}})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &env{srv: srv, h: h, stream: stream, apps: apps, auth: fa}, conns
}

func (e *env) connect(t *testing.T, token string) *websocket.Conn {
	t.Helper()
	c := e.dial(t)
	t.Cleanup(func() { c.CloseNow() })
	send(t, c, map[string]any{"t": model.FrameAuth, "app": "a1", "token": token, "kind": "biz"})
	return c
}

// alive 用一次心跳往返确认连接没被关。
func alive(t *testing.T, c *websocket.Conn) bool {
	t.Helper()
	if err := c.Write(context.Background(), websocket.MessageText, []byte(`{"t":4}`)); err != nil {
		return false
	}
	f, err := read(t, c)
	return err == nil && f.T == model.FramePong
}

// TestGroupPolicyFromCallback 守住分组策略按"这一次握手"生效：同一个 uid
// 在 web 组按 limit 2、在 app 组按 replace，两组是不同主体、互不影响；
// 应用级策略是 reject，任何一组错用了它都会在第二条连接上露馅。
func TestGroupPolicyFromCallback(t *testing.T) {
	e, _ := newRedisEnv(t, model.PolicyReject, map[string]auth.Identity{
		"web": {Subject: model.BizGroup("web", "1001"), Policy: model.PolicyLimit, Limit: 2},
		"app": {Subject: model.BizGroup("app", "1001"), Policy: model.PolicyReplace},
	})

	w1 := e.connect(t, "web")
	expectSuccess(t, w1)
	w2 := e.connect(t, "web")
	expectSuccess(t, w2)
	w3 := e.connect(t, "web")
	expectReject(t, w3, model.CodeConnLimited, model.ClosePolicyRejected)

	a1 := e.connect(t, "app")
	expectSuccess(t, a1)
	a2 := e.connect(t, "app")
	expectSuccess(t, a2)
	if _, err := read(t, a1); websocket.CloseStatus(err) != model.CloseKicked {
		t.Fatalf("app 组按 replace，第一条应被 4003 顶掉，实际 %v", err)
	}

	for i, c := range []*websocket.Conn{w1, w2, a2} {
		if !alive(t, c) {
			t.Fatalf("第 %d 条连接不该受另一组的影响", i)
		}
	}
}

// TestGroupWithoutPolicyUsesAppPolicy 守住回调不给策略时沿用应用级策略。
func TestGroupWithoutPolicyUsesAppPolicy(t *testing.T) {
	e, _ := newRedisEnv(t, model.PolicyReject, map[string]auth.Identity{
		"r": {Subject: model.BizGroup("r", "n1")},
	})
	c1 := e.connect(t, "r")
	expectSuccess(t, c1)
	c2 := e.connect(t, "r")
	expectReject(t, c2, model.CodeConnLimited, model.ClosePolicyRejected)
	if !alive(t, c1) {
		t.Fatal("reject 策略下旧连接应保持在线")
	}
}

// TestGroupCapCheckedBeforeHandshake 守住分组数上限在握手脚本之前判：
// 放到脚本之后，replace 已经把别的节点上的旧连接顶掉了，再拒新连接，
// 两条都没了。
func TestGroupCapCheckedBeforeHandshake(t *testing.T) {
	ctx := context.Background()
	e, conns := newRedisEnv(t, model.PolicyReplace, map[string]auth.Identity{
		"over": {Subject: model.BizGroup("over", "u"), Policy: model.PolicyReplace},
		"g0":   {Subject: model.BizGroup("g0", "v")},
	})
	for i := 0; i < hub.MaxGroupsPerApp; i++ {
		e.h.AddConn(ctx, "a1", model.BizGroup(fmt.Sprintf("g%d", i), "u"), &hubtest.Conn{ConnID: fmt.Sprint("c", i)}, model.ConnMeta{Node: "im-a"}, "")
	}
	// 同一主体在另一个节点上已有连接：本节点还没有这个分组
	live := []string{"im-a", "im-b"}
	subject := model.BizGroup("over", "u").String()
	if _, err := conns.Handshake(ctx, "a1", subject, "old", model.ConnMeta{Node: "im-b"}, model.PolicyNone, 0, live); err != nil {
		t.Fatal(err)
	}

	c := e.connect(t, "over")
	expectReject(t, c, model.CodeAuthUnavailable, model.CloseUnavailable)
	got, err := conns.Lookup(ctx, "a1", subject, live)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["old"]; !ok || len(got) != 1 {
		t.Fatalf("被拒的握手不能顶掉别的节点上的旧连接，连接表实际 %v", got)
	}

	ok := e.connect(t, "g0")
	expectSuccess(t, ok)
}

// TestDefaultGroupIgnoresIdentityPolicy 是 bizauth 之外的第二道保护：默认分组
// 只用应用级策略。哪个认证器出 bug 给默认分组带了策略（这里给 replace），
// 也不能把应用级 reject 下已在线的连接顶掉。
func TestDefaultGroupIgnoresIdentityPolicy(t *testing.T) {
	e, _ := newRedisEnv(t, model.PolicyReject, map[string]auth.Identity{
		"d": {Subject: model.Biz("1001"), Policy: model.PolicyReplace},
	})
	c1 := e.connect(t, "d")
	expectSuccess(t, c1)
	c2 := e.connect(t, "d")
	expectReject(t, c2, model.CodeConnLimited, model.ClosePolicyRejected)
	if !alive(t, c1) {
		t.Fatal("默认分组按应用级 reject，旧连接应保持在线")
	}
}
