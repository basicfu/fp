package fpsdk

import (
	"sync"
	"testing"
	"time"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

func TestOnWechatSubscriptionChanged(t *testing.T) {
	var (
		mu  sync.Mutex
		got []WechatSubscriptionEvent
	)
	env := newStubEnv(t, nil, func(o *Options) {
		o.OnWechatSubscriptionChanged = func(ev WechatSubscriptionEvent) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, ev)
		}
	})
	push := func(user string, sub bool, at int64) {
		env.stub.events <- &fpv1.WatchResponse{Event: &fpv1.WatchResponse_WechatSubscriptionChanged{
			WechatSubscriptionChanged: &fpv1.WechatSubscriptionChanged{UserId: user, Subscribed: sub, AtMs: at},
		}}
	}
	push("u-1", true, 1700000000000)
	push("u-1", false, 1700000001000)
	env.waitUntil(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 2 }, "两条事件都应回调")

	mu.Lock()
	defer mu.Unlock()
	if got[0].UserID != "u-1" || !got[0].Subscribed || !got[0].At.Equal(time.UnixMilli(1700000000000)) || got[1].Subscribed {
		t.Fatalf("got = %+v", got)
	}
}

// 没注册回调的业务方收到这类事件什么都不做，流照常工作。
func TestWechatSubscriptionChangedWithoutCallback(t *testing.T) {
	env := newStubEnv(t, nil)
	env.stub.events <- &fpv1.WatchResponse{Event: &fpv1.WatchResponse_WechatSubscriptionChanged{
		WechatSubscriptionChanged: &fpv1.WechatSubscriptionChanged{UserId: "u-1", Subscribed: true},
	}}
	env.pushPurge(t, "路标")
	env.waitUntil(t, func() bool { return env.client.streamUp.Load() }, "流应保持健康")
}
