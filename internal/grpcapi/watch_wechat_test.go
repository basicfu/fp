package grpcapi

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// 业务方的流收到本应用的 WechatSubscriptionChanged；fp-im 的通配流不收，别的应用也不收。
func TestWatchDeliversWechatSubscriptionChanged(t *testing.T) {
	e := newGRPCEnv(t)
	other := e.newApplication(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	open := func(ctx context.Context) interface {
		Recv() (*fpv1.WatchResponse, error)
	} {
		t.Helper()
		stream, err := e.client.Watch(ctx)
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		if ev, err := stream.Recv(); err != nil || ev.GetReady() == nil {
			t.Fatalf("首帧应当是 ready，got %+v err=%v", ev, err)
		}
		return stream
	}
	mine := open(e.authed(ctx))
	theirs := open(other.authed(ctx))
	im := open(e.imAuthed(ctx, e.appID))

	user := uuid.New()
	if err := e.pub.Publish(ctx, domain.RevokeEvent{
		Kind: domain.EventKindWechatSubscriptionChanged, AppID: e.app.ID,
		UserIDs: []uuid.UUID{user}, Subscribed: true, At: 12345,
	}); err != nil {
		t.Fatal(err)
	}
	// 路标：一条三条流都该收到的事件。没被过滤的话，微信事件会排在它前面先到。
	if err := e.pub.Publish(ctx, domain.RevokeEvent{Kind: domain.EventKindPolicyChanged, At: 1}); err != nil {
		t.Fatal(err)
	}

	ev, err := mine.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	got := ev.GetWechatSubscriptionChanged()
	if got == nil || got.GetUserId() != user.String() || !got.GetSubscribed() || got.GetAtMs() != 12345 {
		t.Fatalf("本应用收到 %+v", ev)
	}
	for name, s := range map[string]interface {
		Recv() (*fpv1.WatchResponse, error)
	}{"别的应用": theirs, "fp-im": im} {
		ev, err := s.Recv()
		if err != nil {
			t.Fatalf("%s Recv: %v", name, err)
		}
		if ev.GetPolicyChanged() == nil {
			t.Fatalf("%s 应先收到路标 PolicyChanged，got %+v", name, ev)
		}
	}
}
