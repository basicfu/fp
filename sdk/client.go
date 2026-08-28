package fpsdk

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// Client 是与 fp 的连接。它是并发安全的，一个进程建一个即可。
type Client struct {
	opts Options
	conn *grpc.ClientConn
	rpc  fpv1.AuthServiceClient

	// auth 在 New 里构造一次，之后不再替换，因此无需同步保护。
	auth *Auth

	// streamUp 是推送流的健康状态。它驱动缓存窗口的收紧，
	// 是"流断开时把安全性拉回来"这条策略的唯一输入。
	streamUp atomic.Bool

	// everReady 记录是否已经收到过至少一次 ready。用于区分"首次连接"
	// 与"重连"：只有重连才需要清空缓存（断开期间的撤销可能漏收）。
	// 见 watchOnce 里 Ready 分支的说明。
	everReady atomic.Bool

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Auth 返回认证能力。多次调用返回同一个实例。
func (c *Client) Auth() *Auth { return c.auth }

// New 建立与 fp 的连接并启动推送流。
func New(opts Options) (*Client, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	opts.applyDefaults()

	transport := credentials.NewTLS(opts.TLSConfig)
	if opts.Insecure {
		opts.Logger.Warn("fpsdk: 使用明文连接，appSecret 将以明文传输——生产环境绝不要这样")
		transport = insecure.NewCredentials()
	}

	conn, err := grpc.NewClient(opts.Addr,
		grpc.WithTransportCredentials(transport),
		grpc.WithPerRPCCredentials(newAppCredentials(opts)),

		// keepalive 是"连接永不空闲"的第二道保险（第一道是 Watch 长流本身）。
		// Time 必须**大于**服务端的 EnforcementPolicy.MinTime（fp 设的是 10 秒），
		// 否则服务端会认为客户端 ping 过频，回 ENHANCE_YOUR_CALM 的 GOAWAY
		// 把连接掐掉——双方都"配了 keepalive"却导致连接被周期性掐断，
		// 是这套机制最经典的自伤方式。
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	if err != nil {
		return nil, err
	}

	cch, err := newCache(opts.CacheSize, time.Now)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{opts: opts, conn: conn, rpc: fpv1.NewAuthServiceClient(conn), cancel: cancel}
	// 必须在启动 watch goroutine 之前构造好：goroutine 一跑起来就可能调
	// c.auth（收到第一条 ready/revoke/purge 就会），构造顺序反了就是
	// nil 解引用——而且只在恰好有事件到达时才崩，本地测试多半复现不出来。
	c.auth = &Auth{c: c, cache: cch}

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.runWatch(ctx)
	}()
	return c, nil
}

// StreamHealthy 报告撤销推送流当前是否可用。
func (c *Client) StreamHealthy() bool { return c.streamUp.Load() }

// Close 关闭连接并停止后台循环。
func (c *Client) Close() error {
	c.cancel()
	err := c.conn.Close()
	c.wg.Wait()
	return err
}

// runWatch 维持推送流，断开后按退避重连，直到 ctx 取消。
func (c *Client) runWatch(ctx context.Context) {
	const (
		minBackoff = 200 * time.Millisecond
		maxBackoff = 30 * time.Second
	)
	backoff := minBackoff

	for ctx.Err() == nil {
		gotReady, err := c.watchOnce(ctx)
		c.streamUp.Store(false)

		if ctx.Err() != nil {
			return
		}

		// 退避复位：这次连接曾经收到过 ready，说明 fp 是健康的，这次断开
		// 大概率是短暂抖动（网络毛刺、fp 的 MaxConnectionAge 主动回收
		// 连接）而不是长时间故障，重连节奏应该从头开始，不能沿用断开前
		// 累积的退避——否则一次长时间的 fp 故障之后，后续任何一次短暂
		// 抖动都要等满 maxBackoff 才重连。只有连 ready 都没等到（fp 本身
		// 就没起来）才继续按原节奏增长。
		if gotReady {
			backoff = minBackoff
		}

		if err != nil {
			c.opts.Logger.Warn("fpsdk: 撤销推送流断开，准备重连", "err", err, "backoff", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if !gotReady {
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// watchOnce 建一次流并读到断开为止。
//
// gotReady 报告本次连接是否曾经收到过 ready，供 runWatch 判断断开后能否
// 把退避重置回最小值：连 ready 都没等到就断开，说明这次重连尝试本身就
// 失败了，退避必须继续按原节奏增长，不能被"看似建了流"误导。
func (c *Client) watchOnce(ctx context.Context) (gotReady bool, err error) {
	stream, err := c.rpc.Watch(ctx)
	if err != nil {
		return false, err
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			return gotReady, err
		}
		switch {
		case msg.GetReady() != nil:
			// 只有"重连"（曾经收到过 ready，这次是再收到一次）才需要清空
			// 缓存：断开期间发生的撤销一条都没收到，缓存里在断开前写入的
			// 任何条目都可能是已被撤销的会话。
			//
			// 首次连接不 purge：New() 在启动本 goroutine 之前才构造
			// c.auth（含空缓存），但 New() 会在本 goroutine 收到首条
			// ready 之前就返回给调用方——调用方的第一个请求与这里的首条
			// ready 谁先发生并无顺序保证。若首次也无条件 purge，一旦
			// 调用方的请求先把结果写入缓存，这里会把它冲掉，造成一次
			// 本可避免的额外回源（这不是理论风险：靠反复运行给定测试
			// TestValidateCachesAndAvoidsRefetch 复现过，约几十分之一的
			// 概率失败）。首次连接时缓存天然是空的，跳过 purge 无损。
			//
			// 顺序很重要（重连分支内）：先 purge、再把 streamUp 置为
			// 健康。反过来的话，两次调用之间有一条极窄但真实存在的
			// 竞态窗口——并发的 Validate() 一旦观察到 streamUp==true
			// 就不再收紧查询窗口（maxTTL 归零），如果此时 purge 还没
			// 跑完，它可能读到一条断连检测延迟期间（连接实际已断但
			// streamUp 尚未翻 false 那段时间，最长一个 keepalive
			// Timeout）写入的满 TTL 存量条目，把它当新鲜数据放行。
			// 先 purge 后置位，保证任何观察到 streamUp==true 的调用，
			// 看到的都已经是清空之后的缓存。
			if c.everReady.Swap(true) {
				c.auth.onPurge()
			}
			c.streamUp.Store(true)
			// 只有收到 ready 才置为健康：此前服务端可能还没订上撤销频道，
			// 那段时间的事件会丢，而 SDK 若已认为健康就不会收紧缓存窗口。
			gotReady = true
		case msg.GetRevoke() != nil:
			c.auth.onRevoke(msg.GetRevoke())
		case msg.GetPurge() != nil:
			c.opts.Logger.Warn("fpsdk: 按服务端要求清空校验缓存",
				"reason", msg.GetPurge().GetReason())
			c.auth.onPurge()
		default:
			// 未知事件类型（将来的 ConfigChanged / PolicyChanged）。
			// 忽略，不要报错——oneof 的向前兼容就靠这里。
		}
	}
}
