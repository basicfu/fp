package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

func TestConfigPublishSubscribe(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	pub := store.NewConfigPublisher(rdb)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, closeSub, err := pub.Subscribe(ctx)
	if err != nil {
		t.Fatalf("订阅失败: %v", err)
	}
	defer closeSub()

	appID := uuid.New()
	if err := pub.Publish(ctx, appID, domain.ConfigTypeWeb, 7); err != nil {
		t.Fatalf("广播失败: %v", err)
	}

	select {
	case sig := <-ch:
		if sig.Gap {
			t.Fatal("这是一条正常事件，不该带 Gap")
		}
		if sig.AppID != appID {
			t.Fatalf("AppID = %s，期望 %s", sig.AppID, appID)
		}
		if sig.Type != domain.ConfigTypeWeb {
			t.Fatalf("Type = %q，期望 %q", sig.Type, domain.ConfigTypeWeb)
		}
		if sig.Seq != 7 {
			t.Fatalf("Seq = %d，期望 7", sig.Seq)
		}
	case <-ctx.Done():
		t.Fatal("等待事件超时")
	}
}

// 没有订阅者不算错误：推送只是把生效延迟从"下次重启"压到近乎实时的加速
// 手段，值已经落库了，那才是权威事实。与 RevokePublisher 同一逻辑。
func TestConfigPublishNoSubscriberIsNotAnError(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	pub := store.NewConfigPublisher(rdb)

	if err := pub.Publish(context.Background(), uuid.New(), domain.ConfigTypeDefault, 1); err != nil {
		t.Fatalf("无订阅者时广播不该报错: %v", err)
	}
}

// TestConfigSubscribeSurfacesResubscribeAsGap 守住"丢事件必须可观测"。
//
// go-redis 会在连接抖动时静默重连并重发 SUBSCRIBE，既不报错也不关 channel。
// 没有这个信号的话，丢事件这件事在整个系统里不留任何痕迹——日志里没有、
// 监控里没有、SDK 看到的流状态也一切正常，配置就永远停在旧版本上。
//
// 制造重连的方式与 revoke_test.go 的 TestSubscribeSurfacesResubscribeAsGap
// 相同：用 CLIENT KILL 掐掉订阅连接（拿 CLIENT LIST TYPE pubsub 找到它）。
// 断开后必须在合理时间内收到一条 Gap == true 的信号。两个辅助函数
// pubsubClientIDs / waitForNewPubsubClientID 定义在 revoke_test.go，
// 同属 store_test 包，这里直接复用。
func TestConfigSubscribeSurfacesResubscribeAsGap(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	pub := store.NewConfigPublisher(rdb)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 先拍一张"订阅前"的 pubsub 连接快照，subscribe 之后用差集找出新出现的
	// 那一条——而不是假设列表里只有一条。
	before := pubsubClientIDs(t, rdb)

	signals, closeFn, err := pub.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer closeFn()

	id := waitForNewPubsubClientID(t, rdb, before)

	// 用 ID 过滤的新式 CLIENT KILL：即使因为时序问题 0 条匹配也不报错，
	// 但这里显式检查杀掉的连接数，确保我们真的打中了目标。
	killed, err := rdb.ClientKillByFilter(ctx, "ID", id).Result()
	if err != nil {
		t.Fatalf("CLIENT KILL ID %s: %v", id, err)
	}
	if killed == 0 {
		t.Fatalf("CLIENT KILL ID %s 没有杀掉任何连接", id)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case sig := <-signals:
			if sig.Gap {
				return // 收到缺口信号，符合预期
			}
			// 理论上此时不该收到别的信号；忽略并继续等待，避免测试因为
			// 无关消息的到达顺序而误判。
		case <-deadline:
			t.Fatal("5 秒内没有收到 Gap 信号——订阅重建没有被检测到，" +
				"丢事件这件事在整个系统里将不留任何痕迹")
		}
	}
}

// Gap 只能由本地的重订阅判定产生，绝不能跨网络传过来。
// 少了 json:"-" 的话，任何有 PUBLISH 权限的人发一条 {"Gap":true} 就能
// 伪造出与真实断连无法区分的信号，触发全量配置重拉风暴。
func TestConfigGapNeverCrossesTheWire(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	pub := store.NewConfigPublisher(rdb)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// ① Publish 出去的 JSON 里不能有 Gap 字段。
	raw, err := json.Marshal(store.ConfigSignal{AppID: uuid.New(), Type: domain.ConfigTypeWeb, Seq: 1})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if bytes.Contains(bytes.ToLower(raw), []byte(`"gap"`)) {
		t.Fatalf("Gap 被序列化上了 wire: %s", raw)
	}

	// ② 伪造一条带 Gap 的普通消息，订阅方必须把它当成普通事件（Gap=false）。
	ch, closeSub, err := pub.Subscribe(ctx)
	if err != nil {
		t.Fatalf("订阅失败: %v", err)
	}
	defer closeSub()

	appID := uuid.New()
	forged := fmt.Sprintf(`{"Gap":true,"gap":true,"appId":%q,"type":%q,"seq":999}`,
		appID.String(), domain.ConfigTypeWeb)
	if err := rdb.Publish(ctx, "fp:config", forged).Err(); err != nil {
		t.Fatalf("发伪造消息失败: %v", err)
	}

	select {
	case sig := <-ch:
		if sig.Gap {
			t.Fatal("伪造的 {\"Gap\":true} 被当成了真的缺口信号——Gap 必须是本地判定，不能来自 wire")
		}
		if sig.AppID != appID || sig.Seq != 999 {
			t.Fatalf("普通字段没解对: %+v", sig)
		}
	case <-ctx.Done():
		t.Fatal("等待事件超时")
	}
}
