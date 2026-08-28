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

	// streamUp 是推送流的健康状态。它驱动缓存窗口的收紧，
	// 是"流断开时把安全性拉回来"这条策略的唯一输入。
	streamUp atomic.Bool

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

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

	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{opts: opts, conn: conn, rpc: fpv1.NewAuthServiceClient(conn), cancel: cancel}

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
			// 只有收到 ready 才置为健康：此前服务端可能还没订上撤销频道，
			// 那段时间的事件会丢，而 SDK 若已认为健康就不会收紧缓存窗口。
			c.streamUp.Store(true)
			gotReady = true
		case msg.GetRevoke() != nil:
			// 本任务只有连接层，还没有缓存可清。Task 10 会把这里换成
			// 真正的缓存失效，并在 ready 分支补上重连后的整体清空。
			c.opts.Logger.Debug("fpsdk: 收到撤销事件",
				"tokens", len(msg.GetRevoke().GetTokens()),
				"reason", msg.GetRevoke().GetReason())
		case msg.GetPurge() != nil:
			c.opts.Logger.Warn("fpsdk: 收到服务端的缓存清空指令",
				"reason", msg.GetPurge().GetReason())
		default:
			// 未知事件类型（将来的 ConfigChanged / PolicyChanged）。
			// 忽略，不要报错——oneof 的向前兼容就靠这里。
		}
	}
}
