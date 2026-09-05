package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// configChannel 是配置变更的 Redis 广播频道。
// fp 的每个实例都订阅它，再通过各自持有的 gRPC Watch 流推给 SDK。
const configChannel = "fp:config"

// ConfigSignal 是订阅流上的一条信号。
type ConfigSignal struct {
	// Gap 为 true 表示事件流出现了缺口：订阅刚刚重建，期间发布的事件已
	// 永久丢失，且无法知道丢了哪些。此时 AppID / Type / Seq 全部无意义，
	// 消费方应当让**所有** SDK 重拉配置。
	//
	// 这是悲观判断不是确知：重建时未必真的丢了东西。但 fp 无从分辨，
	// 只能按最坏情况处理。与 RevokeSignalGap 是同一件事的两种后果——
	// 撤销那边要求丢弃全部缓存，配置这边只要求重拉。
	Gap bool

	AppID uuid.UUID `json:"appId"`
	// Type 是分区，取值见 domain.ConfigType*。
	Type string `json:"type"`
	// Seq 是新版本号，仅用于日志与排障。SDK 收到信号后拉的是"当前版本"
	// 而不是"第 Seq 版"——这让丢失一条信号的后果被下一条信号自动修复。
	Seq int64 `json:"seq"`
}

// ConfigPublisher 广播配置变更。
type ConfigPublisher struct {
	rdb *redis.Client
}

// NewConfigPublisher 构造 ConfigPublisher。
func NewConfigPublisher(rdb *redis.Client) *ConfigPublisher {
	return &ConfigPublisher{rdb: rdb}
}

// Publish 广播一次配置变更。
//
// 没有订阅者时不算错误：值已经落库，那是权威事实；推送只是把生效延迟从
// "下次重启"压到近乎实时的加速手段。
func (p *ConfigPublisher) Publish(ctx context.Context, appID uuid.UUID, typ string, seq int64) error {
	raw, err := json.Marshal(ConfigSignal{AppID: appID, Type: typ, Seq: seq})
	if err != nil {
		return fmt.Errorf("store: 序列化配置变更事件: %w", err)
	}
	if err := p.rdb.Publish(ctx, configChannel, raw).Err(); err != nil {
		return fmt.Errorf("store: 广播配置变更事件: %w", err)
	}
	return nil
}

// Subscribe 订阅配置变更。返回的 channel 在 ctx 取消或调用 close 时关闭。
//
// ctx 取消必须显式转成 sub.Close()：go-redis 的 PubSub reader goroutine
// 跑在 context.TODO() 上，它的输出 channel 只在 PubSub.Close() 时才关。光靠下面
// select 里的 <-ctx.Done() 是不够的——out 有 64 的缓冲，两个 case 常常同时就绪，
// Go 会随机选一个；而在完全没有消息流入时，goroutine 会一直阻塞在
// range sub.ChannelWithSubscriptions() 上，out 永不关闭，Redis 那条订阅连接也
// 一直挂着。
//
// 关键在于：那个等 ctx.Done() 的看门狗 goroutine 必须**也能被 closeFn 叫醒**。
// 直接监听调用方传进来的 ctx 的话，调用方若传 context.Background() 再靠 closeFn
// 清理（对一个返回了 close 函数的 API 来说是完全合理的用法），看门狗就永远等不到
// Done，一路阻塞到进程退出。所以这里派生一个可取消的子 ctx，closeFn 里先 cancel
// 再 Close：两种清理途径（取消父 ctx / 调 closeFn）都能让看门狗退出。
//
// 派生子 ctx 不影响订阅本身：redis.Client.Subscribe 只在最初那次 SUBSCRIBE
// 往返里用 ctx，之后的消息读取跑在 PubSub 自己的 goroutine 上。
//
// 用 ChannelWithSubscriptions 而不是 Channel：连接抖动时 go-redis 会**静默重连
// 并重发 SUBSCRIBE**——既不报错也不关 channel。Channel() 只投递 *Message，
// 重连这件事对调用方完全不可见，那个窗口丢的配置变更事件永久消失且不留任何
// 痕迹。ChannelWithSubscriptions 额外投递 *Subscription，go-redis 的
// resubscribe() 在每次重连时都会重发 SUBSCRIBE，Redis 的确认回包就被解析成
// 一条 *Subscription{Kind:"subscribe"}。
//
// 下面的分发循环把**每一条**这样的确认都当成重连、翻译成 Gap，不做"第一条
// 例外"的特判——因为真正的初次订阅确认已经被上面那次同步的 sub.Receive(ctx)
// 读走并消耗掉了，不会再出现在 ChannelWithSubscriptions() 里。
//
// 注意 ChannelWithSubscriptions 与 Channel 互斥：go-redis 内部对同一个 PubSub
// 先调用其中一个、后调用另一个会 panic，所以全仓库不能再有别处对同一个
// sub 调用 Channel()。
func (p *ConfigPublisher) Subscribe(ctx context.Context) (<-chan ConfigSignal, func(), error) {
	sub := p.rdb.Subscribe(ctx, configChannel)
	// Receive 会阻塞到订阅确认返回，确保这之后发布的消息不会丢。
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return nil, nil, fmt.Errorf("store: 订阅配置频道: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)

	// ctx 取消时主动关掉底层订阅，这才是让 reader goroutine 退出的唯一途径：
	// go-redis 的 reader 跑在 context.TODO() 上，它的输出 channel 只在
	// PubSub.Close() 时才关。光靠下面 select 里的 <-ctx.Done() 不够——
	// 完全没有消息流入时，goroutine 会一直阻塞在 range 上。
	go func() {
		<-ctx.Done()
		_ = sub.Close()
	}()

	out := make(chan ConfigSignal, 64)
	go func() {
		defer close(out)

		// resubscribeCount 数的是本循环观察到的 SUBSCRIBE 确认回包，也就是
		// go-redis 重连的次数。这里能看到的每一条都必然来自重连——真正
		// "初次订阅"的那条确认已经被上面 sub.Receive(ctx) 同步读走了。
		resubscribeCount := 0

		for msg := range sub.ChannelWithSubscriptions() {
			var sig ConfigSignal
			switch m := msg.(type) {
			case *redis.Subscription:
				if m.Kind != "subscribe" {
					continue
				}
				resubscribeCount++
				slog.Warn("store: Redis 订阅已重建，期间的配置变更事件已丢失",
					"resubscribeCount", resubscribeCount)
				sig = ConfigSignal{Gap: true}
			case *redis.Message:
				if err := json.Unmarshal([]byte(m.Payload), &sig); err != nil {
					// 跳过这一条，不中断订阅：一条坏消息不该让整个实例
					// 从此收不到任何配置变更。
					slog.Error("store: 解析配置变更事件失败", "err", err)
					continue
				}
			default:
				continue
			}

			select {
			case out <- sig:
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, func() { cancel(); _ = sub.Close() }, nil
}
