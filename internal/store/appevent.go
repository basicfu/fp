package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// appEventChannel 是「某应用有新业务事件」的唤醒频道，载荷是应用内部 ID。
//
// 只是让投递方不必等下一次轮询的加速信号：丢了、重连漏了都无妨，投递方另有定时轮询兜底，
// 所以不像撤销频道那样需要识别订阅缺口。
const appEventChannel = "fp:evt:new"

// AppEventSignal 收发业务事件的唤醒信号。
type AppEventSignal struct {
	rdb *redis.Client
}

// NewAppEventSignal 构造 AppEventSignal。
func NewAppEventSignal(rdb *redis.Client) *AppEventSignal {
	return &AppEventSignal{rdb: rdb}
}

// Publish 通知各实例某应用有新事件。
func (s *AppEventSignal) Publish(ctx context.Context, appID uuid.UUID) error {
	if err := s.rdb.Publish(ctx, appEventChannel, appID.String()).Err(); err != nil {
		return fmt.Errorf("store: 广播业务事件唤醒: %w", err)
	}
	return nil
}

// Subscribe 订阅唤醒信号。返回的 channel 在 ctx 取消或调用 close 后关闭；缓冲满时丢弃（见 appEventChannel）。
func (s *AppEventSignal) Subscribe(ctx context.Context) (<-chan uuid.UUID, func(), error) {
	sub := s.rdb.Subscribe(ctx, appEventChannel)
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return nil, nil, fmt.Errorf("store: 订阅业务事件唤醒: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		<-ctx.Done()
		_ = sub.Close()
	}()
	out := make(chan uuid.UUID, 64)
	go func() {
		defer close(out)
		for msg := range sub.Channel() {
			id, err := uuid.Parse(msg.Payload)
			if err != nil {
				continue
			}
			select {
			case out <- id:
			default:
			}
		}
	}()
	return out, func() { cancel(); _ = sub.Close() }, nil
}

// Lease 是 Redis 上的一把带持有者的租约：SET NX PX 抢，持有者凭 owner 续期与释放。
type Lease struct {
	rdb *redis.Client
}

// NewLease 构造 Lease。
func NewLease(rdb *redis.Client) *Lease {
	return &Lease{rdb: rdb}
}

// 续期与释放都必须先核对持有者：租约过期被别人抢走之后，旧持有者的续期或释放会覆盖掉新持有者。
var (
	leaseRenewScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0`)
	leaseReleaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0`)
)

// Acquire 尝试抢租约，抢到返回 true。
func (l *Lease) Acquire(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	ok, err := l.rdb.SetNX(ctx, key, owner, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("store: 抢租约: %w", err)
	}
	return ok, nil
}

// Renew 续期，租约已不归 owner 时返回 false。
func (l *Lease) Renew(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	n, err := leaseRenewScript.Run(ctx, l.rdb, []string{key}, owner, ttl.Milliseconds()).Int()
	if err != nil {
		return false, fmt.Errorf("store: 续租约: %w", err)
	}
	return n == 1, nil
}

// Release 释放租约，不归 owner 时什么都不做。
func (l *Lease) Release(ctx context.Context, key, owner string) error {
	if err := leaseReleaseScript.Run(ctx, l.rdb, []string{key}, owner).Err(); err != nil {
		return fmt.Errorf("store: 释放租约: %w", err)
	}
	return nil
}
