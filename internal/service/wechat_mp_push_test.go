package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
)

// takeWechatEvents 取走迄今发布的微信通知事件，返回 应用 → 该应用收到的最后一条状态。
func (e *mpEnv) takeWechatEvents(t *testing.T, user uuid.UUID) map[uuid.UUID]bool {
	t.Helper()
	e.pub.mu.Lock()
	defer e.pub.mu.Unlock()
	out := map[uuid.UUID]bool{}
	for _, ev := range e.pub.events {
		if ev.Kind != domain.EventKindWechatSubscriptionChanged {
			continue
		}
		if len(ev.UserIDs) != 1 || ev.UserIDs[0] != user || ev.AppID == uuid.Nil || ev.At == 0 {
			t.Fatalf("事件形状不对: %+v", ev)
		}
		out[ev.AppID] = ev.Subscribed
	}
	e.pub.events = nil
	return out
}

func (e *mpEnv) newApp(t *testing.T, code string) *domain.Application {
	t.Helper()
	app, _, err := e.apps.Create(context.Background(), code, code)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func wantPushed(t *testing.T, what string, got map[uuid.UUID]bool, want map[uuid.UUID]bool) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: 推给了 %v, want %v", what, got, want)
	}
	for app, sub := range want {
		if s, ok := got[app]; !ok || s != sub {
			t.Fatalf("%s: 推给了 %v, want %v", what, got, want)
		}
	}
}

// 扫码开启、取关、重新关注、关掉通知：每一步都推给用户登录过的应用和发起扫码的应用。
func TestWechatSubscriptionChangesArePushed(t *testing.T) {
	e := newMPEnv(t)
	ctx := context.Background()
	user := e.newUser(t, "13800000001", "")
	loggedIn, creator := e.newApp(t, "logged-in"), e.newApp(t, "creator")
	if err := e.users.EnsureRegistration(ctx, user, loggedIn.ID, false); err != nil {
		t.Fatal(err)
	}

	if _, err := e.wechat.CreateSubscribeQR(ctx, creator.AppID, user); err != nil {
		t.Fatal(err)
	}
	e.push(t, "o-1", "subscribe", "qrscene_"+e.mp.lastScene(t), 100)
	wantPushed(t, "扫码开启", e.takeWechatEvents(t, user), map[uuid.UUID]bool{loggedIn.ID: true, creator.ID: true})

	e.push(t, "o-1", "unsubscribe", "", 200)
	wantPushed(t, "取关", e.takeWechatEvents(t, user), map[uuid.UUID]bool{loggedIn.ID: false, creator.ID: false})

	e.push(t, "o-1", "subscribe", "", 300)
	wantPushed(t, "重新关注", e.takeWechatEvents(t, user), map[uuid.UUID]bool{loggedIn.ID: true, creator.ID: true})

	if err := e.wechat.Unbind(ctx, user); err != nil {
		t.Fatal(err)
	}
	// 绑定行删掉了，发起扫码的应用照样要收到。
	wantPushed(t, "关掉通知", e.takeWechatEvents(t, user), map[uuid.UUID]bool{loggedIn.ID: false, creator.ID: false})

	// 绑定已经没了：再有关注事件，跟这个用户无关。
	e.push(t, "o-1", "unsubscribe", "", 400)
	wantPushed(t, "解绑后的取关", e.takeWechatEvents(t, user), map[uuid.UUID]bool{})
}

func TestWechatAutoBindIsPushed(t *testing.T) {
	e := newMPEnv(t)
	ctx := context.Background()
	user := e.newUser(t, "13800000001", "u-1")
	app := e.newApp(t, "kk")
	if err := e.users.EnsureRegistration(ctx, user, app.ID, true); err != nil {
		t.Fatal(err)
	}
	e.mp.unionIDs["o-1"] = "u-1"

	e.push(t, "o-1", "subscribe", "", 100)
	wantPushed(t, "按 unionid 自动开启", e.takeWechatEvents(t, user), map[uuid.UUID]bool{app.ID: true})

	// 没有 unionid 能对上的关注者：谁都不推。
	e.push(t, "o-stranger", "subscribe", "", 100)
	wantPushed(t, "陌生人关注", e.takeWechatEvents(t, user), map[uuid.UUID]bool{})
}
