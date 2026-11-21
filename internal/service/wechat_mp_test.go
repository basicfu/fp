package service_test

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/notify"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

const mpToken = "mp-token"

// fakeMP 是假的服务号接口：记下生成二维码时的场景值和发出去的模板消息。
type fakeMP struct {
	mu       sync.Mutex
	scenes   []string
	sent     []map[string]any
	unionIDs map[string]string // openid → unionid
	gone     map[string]bool   // 这些 openid 发模板消息回 43004
}

func (f *fakeMP) lastScene(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.scenes) == 0 {
		t.Fatal("还没生成过二维码")
	}
	return f.scenes[len(f.scenes)-1]
}

func newFakeMP(t *testing.T) (*fakeMP, *httptest.Server) {
	t.Helper()
	f := &fakeMP{unionIDs: map[string]string{}, gone: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/cgi-bin/stable_token":
			_, _ = io.WriteString(w, `{"access_token":"at","expires_in":7200}`)
		case "/cgi-bin/qrcode/create":
			scene := body["action_info"].(map[string]any)["scene"].(map[string]any)["scene_str"].(string)
			f.scenes = append(f.scenes, scene)
			_, _ = io.WriteString(w, `{"ticket":"tk","expire_seconds":600,"url":"http://weixin.qq.com/q/x"}`)
		case "/cgi-bin/user/info":
			openid := r.URL.Query().Get("openid")
			_ = json.NewEncoder(w).Encode(map[string]any{"subscribe": 1, "openid": openid, "unionid": f.unionIDs[openid]})
		case "/cgi-bin/message/template/send":
			if f.gone[body["touser"].(string)] {
				_, _ = io.WriteString(w, `{"errcode":43004,"errmsg":"require subscribe"}`)
				return
			}
			f.sent = append(f.sent, body)
			_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

type mpEnv struct {
	mp     *fakeMP
	wechat *service.WechatMPService
	notify *service.NotifyService
	users  *service.UserService
	prov   uuid.UUID
	pub    *recordingPublisher
	apps   *service.ApplicationService
}

func newMPEnv(t *testing.T) *mpEnv {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)
	mp, srv := newFakeMP(t)
	reg := notify.NewRegistry()
	if err := reg.Register(notify.WechatMPSpec(srv.URL)); err != nil {
		t.Fatal(err)
	}
	pub := &recordingPublisher{}
	wechat := service.NewWechatMPService(pool, rdb, service.WithWechatMPAPIBase(srv.URL), service.WithWechatMPPublisher(pub))
	ns := service.NewNotifyService(pool, rdb, reg, service.WithNotifyRecipients(domain.NotifyChannelWechatMP, wechat.ResolveRecipient))
	p, err := ns.CreateProvider(context.Background(), service.CreateNotifyProviderInput{
		Type: notify.TypeWechatMP, Enabled: true,
		Config: map[string]any{"appId": "wxmp", "appSecret": "sec", "token": mpToken, "followReply": "欢迎关注"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &mpEnv{mp: mp, wechat: wechat, notify: ns, users: service.NewUserService(pool), prov: p.ID,
		pub: pub, apps: service.NewApplicationService(pool, connector.NewRegistry())}
}

func (e *mpEnv) newUser(t *testing.T, phone, unionID string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	u, _, _, err := e.users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{Type: domain.IdentityTypePhone, Subject: phone})
	if err != nil {
		t.Fatal(err)
	}
	if unionID != "" {
		if _, err := e.users.AttachIdentity(ctx, u.ID, service.EnsureIdentityInput{
			Type: domain.IdentityTypeWechatWeb, Subject: "web-" + unionID, UnionKey: unionID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return u.ID
}

// push 模拟一次明文模式的事件推送，返回回给微信的内容。
func (e *mpEnv) push(t *testing.T, openid, event, key string, at int64) string {
	t.Helper()
	ts, nonce := "1700000000", "n1"
	parts := []string{mpToken, ts, nonce}
	sort.Strings(parts)
	sum := sha1.Sum([]byte(strings.Join(parts, "")))
	q := url.Values{"timestamp": {ts}, "nonce": {nonce}, "signature": {hex.EncodeToString(sum[:])}, "openid": {openid}}
	body := fmt.Sprintf(`<xml><ToUserName><![CDATA[gh_1]]></ToUserName><FromUserName><![CDATA[%s]]></FromUserName>`+
		`<CreateTime>%d</CreateTime><MsgType><![CDATA[event]]></MsgType><Event><![CDATA[%s]]></Event>`+
		`<EventKey><![CDATA[%s]]></EventKey></xml>`, openid, at, event, key)
	res := e.wechat.HandlePush(context.Background(), q, []byte(body))
	if res.BadRequest {
		t.Fatal("推送被判为非法")
	}
	return string(res.Body)
}

func (e *mpEnv) subscription(t *testing.T, userID uuid.UUID) *service.WechatSubscription {
	t.Helper()
	s, err := e.wechat.Subscription(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *mpEnv) template(t *testing.T, code string) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.notify.CreateTemplate(ctx, service.CreateNotifyTemplateInput{
		Code: code, Channel: domain.NotifyChannelWechatMP, Mode: domain.NotifyModeVendor, Enabled: true,
		Content: domain.NotifyContent{Content: "设备：{{thing1.DATA}}\n时间：{{time2.DATA}}", Variables: []string{"thing1", "time2"}, Link: "https://kkdesk.example.com/devices"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.notify.SetTemplateProvider(ctx, code, e.prov, service.SetNotifyLinkInput{ProviderTemplateID: "TPL_1", Enabled: true}); err != nil {
		t.Fatal(err)
	}
}

func TestWechatSubscribeByScanningQR(t *testing.T) {
	e := newMPEnv(t)
	ctx := context.Background()
	user := e.newUser(t, "13800000001", "")

	qr, err := e.wechat.CreateSubscribeQR(ctx, "app-1", user)
	if err != nil {
		t.Fatal(err)
	}
	if qr.ImageURL == "" || qr.ExpiresIn != 10*time.Minute {
		t.Fatalf("qr = %+v", qr)
	}
	if s := e.subscription(t, user); s.Bound {
		t.Fatalf("扫码前不该已开启: %+v", s)
	}

	// 没关注的人扫码：先关注，推 subscribe，场景值带 qrscene_ 前缀。
	reply := e.push(t, "o-1", "subscribe", "qrscene_"+e.mp.lastScene(t), 100)
	if !strings.Contains(reply, notify.DefaultWechatMPBindReply) {
		t.Fatalf("reply = %s", reply)
	}
	if s := e.subscription(t, user); !s.Bound || !s.Subscribed {
		t.Fatalf("扫码后应已开启: %+v", s)
	}
	// 微信超时重推同一个事件：照常成功，不能变成"已被使用"。
	if reply := e.push(t, "o-1", "subscribe", "qrscene_"+e.mp.lastScene(t), 100); !strings.Contains(reply, notify.DefaultWechatMPBindReply) {
		t.Fatalf("重推的回复 = %s", reply)
	}
}

func TestWechatSubscribeQRSingleUse(t *testing.T) {
	e := newMPEnv(t)
	user := e.newUser(t, "13800000001", "")
	if _, err := e.wechat.CreateSubscribeQR(context.Background(), "app-1", user); err != nil {
		t.Fatal(err)
	}
	scene := e.mp.lastScene(t)
	e.push(t, "o-1", "SCAN", scene, 100)
	if reply := e.push(t, "o-intruder", "SCAN", scene, 101); !strings.Contains(reply, "已被其他微信使用") {
		t.Fatalf("reply = %s", reply)
	}
	openid, err := e.wechat.ResolveRecipient(context.Background(), user.String())
	if err != nil || openid != "o-1" {
		t.Fatalf("openid = %q, err = %v", openid, err)
	}
}

func TestWechatSubscribeExpiredScene(t *testing.T) {
	e := newMPEnv(t)
	if reply := e.push(t, "o-1", "SCAN", "fpsub:no-such-token", 100); !strings.Contains(reply, "已过期") {
		t.Fatalf("reply = %s", reply)
	}
}

func TestWechatUnsubscribeStopsDeliveryAndOrderIsRespected(t *testing.T) {
	e := newMPEnv(t)
	ctx := context.Background()
	user := e.newUser(t, "13800000001", "")
	if _, err := e.wechat.CreateSubscribeQR(ctx, "app-1", user); err != nil {
		t.Fatal(err)
	}
	e.push(t, "o-1", "SCAN", e.mp.lastScene(t), 100)
	e.push(t, "o-1", "unsubscribe", "", 200)
	// 早于取关的关注事件迟到了，不能把状态改回已关注。
	e.push(t, "o-1", "subscribe", "", 150)

	if s := e.subscription(t, user); !s.Bound || s.Subscribed {
		t.Fatalf("取关后应是已绑定但收不到: %+v", s)
	}
	_, err := e.wechat.ResolveRecipient(ctx, user.String())
	wantNotifyCode(t, err, domain.CodeNotifyRecipientUnreachable)

	// 重新关注就恢复，不用重新扫码。
	e.push(t, "o-1", "subscribe", "", 300)
	if s := e.subscription(t, user); !s.Subscribed {
		t.Fatalf("重新关注后应恢复: %+v", s)
	}
}

// 用微信登录过的人自己搜索关注服务号：按 unionid 自动开启，不用再扫码。
func TestWechatFollowAutoBindsByUnionID(t *testing.T) {
	e := newMPEnv(t)
	user := e.newUser(t, "13800000001", "u-1")
	e.mp.unionIDs["o-1"] = "u-1"

	if reply := e.push(t, "o-1", "subscribe", "", 100); !strings.Contains(reply, "欢迎关注") {
		t.Fatalf("reply = %s", reply)
	}
	if s := e.subscription(t, user); !s.Bound || !s.Subscribed {
		t.Fatalf("应按 unionid 自动开启: %+v", s)
	}
}

// 关掉通知后不会被自动关联悄悄改回来；只有用户自己重新关注服务号，才按 unionid 重新开启。
func TestWechatUnbindStaysUntilRefollow(t *testing.T) {
	e := newMPEnv(t)
	ctx := context.Background()
	user := e.newUser(t, "13800000001", "u-1")
	e.mp.unionIDs["o-1"] = "u-1"
	e.push(t, "o-1", "subscribe", "", 100)
	if err := e.wechat.Unbind(ctx, user); err != nil {
		t.Fatal(err)
	}
	if s := e.subscription(t, user); s.Bound {
		t.Fatalf("关掉后不该还绑着: %+v", s)
	}
	// 关注状态的其他事件（再扫一次别的码之外）不会重新关联。
	e.push(t, "o-1", "SCAN", "", 150)
	if s := e.subscription(t, user); s.Bound {
		t.Fatalf("没有重新关注就不该重新开启: %+v", s)
	}
	e.push(t, "o-1", "unsubscribe", "", 200)
	e.push(t, "o-1", "subscribe", "", 300)
	if s := e.subscription(t, user); !s.Bound {
		t.Fatalf("取关再关注应按 unionid 重新关联: %+v", s)
	}
}

func TestWechatNotifySendResolvesUserToOpenID(t *testing.T) {
	e := newMPEnv(t)
	ctx := context.Background()
	user := e.newUser(t, "13800000001", "")
	e.template(t, "device_connected")
	if _, err := e.wechat.CreateSubscribeQR(ctx, "app-1", user); err != nil {
		t.Fatal(err)
	}
	e.push(t, "o-1", "SCAN", e.mp.lastScene(t), 100)

	long := strings.Repeat("长", 30)
	err := e.notify.Send(ctx, service.NotifySendInput{
		AppID: "app-1", Code: "device_connected", To: user.String(),
		Params: map[string]string{"thing1": long, "time2": "2026-11-20 10:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.mp.sent) != 1 {
		t.Fatalf("sent = %+v", e.mp.sent)
	}
	msg := e.mp.sent[0]
	if msg["touser"] != "o-1" || msg["template_id"] != "TPL_1" || msg["url"] != "https://kkdesk.example.com/devices" {
		t.Fatalf("msg = %+v", msg)
	}
	thing := msg["data"].(map[string]any)["thing1"].(map[string]any)["value"].(string)
	if n := len([]rune(thing)); n != 20 || !strings.HasSuffix(thing, "…") {
		t.Fatalf("thing1 应截到 20 个字符: %q", thing)
	}

	// 发送记录里记 fp 用户 ID，不记 openid。
	logs, _, err := e.notify.ListLogs(ctx, service.NotifyLogFilter{Code: "device_connected"})
	if err != nil || len(logs) != 1 || logs[0].Target != user.String() {
		t.Fatalf("logs = %+v, err = %v", logs, err)
	}
}

func TestWechatNotifySendUnreachableRecipients(t *testing.T) {
	e := newMPEnv(t)
	ctx := context.Background()
	e.template(t, "device_connected")
	params := map[string]string{"thing1": "x", "time2": "y"}

	user := e.newUser(t, "13800000001", "")
	err := e.notify.Send(ctx, service.NotifySendInput{Code: "device_connected", To: user.String(), Params: params})
	wantNotifyCode(t, err, domain.CodeNotifyRecipientUnreachable)

	err = e.notify.Send(ctx, service.NotifySendInput{Code: "device_connected", To: "13800000001", Params: params})
	wantNotifyCode(t, err, domain.CodeNotifyRecipientInvalid)
}

func TestWechatMPProviderIsSingleton(t *testing.T) {
	e := newMPEnv(t)
	_, err := e.notify.CreateProvider(context.Background(), service.CreateNotifyProviderInput{
		Type: notify.TypeWechatMP, Enabled: true, Config: map[string]any{"appId": "wx2", "appSecret": "s", "token": "t"},
	})
	wantNotifyCode(t, err, domain.CodeNotifyProviderInvalid)
}

func TestWechatSubscribeQRUnavailableWhenProviderDisabled(t *testing.T) {
	e := newMPEnv(t)
	ctx := context.Background()
	off := false
	if _, err := e.notify.UpdateProvider(ctx, e.prov, service.UpdateNotifyProviderInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	_, err := e.wechat.CreateSubscribeQR(ctx, "app-1", e.newUser(t, "13800000001", ""))
	wantNotifyCode(t, err, domain.CodeWechatMPUnavailable)
}

func TestWechatSubscribeQRUnknownUser(t *testing.T) {
	e := newMPEnv(t)
	_, err := e.wechat.CreateSubscribeQR(context.Background(), "app-1", uuid.New())
	wantNotifyCode(t, err, domain.CodeUserNotFound)
}

func TestWechatPushBadSignature(t *testing.T) {
	e := newMPEnv(t)
	res := e.wechat.HandlePush(context.Background(), url.Values{"signature": {"x"}, "timestamp": {"1"}, "nonce": {"n"}}, []byte("<xml></xml>"))
	if !res.BadRequest {
		t.Fatal("签名不对应回 400")
	}
}

func wantNotifyCode(t *testing.T, err error, code string) {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != code {
		t.Fatalf("err = %v, want 码 %s", err, code)
	}
}
