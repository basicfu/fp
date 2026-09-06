package fpsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// 配置分区。业务方调 BindType（Task 12）时用来指定要绑定哪个分区；
// Bind 固定绑 ConfigTypeDefault。取值必须与服务端 internal/domain 的
// ConfigType* 逐字相同——这条配对关系与 KeepaliveTime 那条一样，分处
// sdk/ 与 internal/ 两个包，任何一边单独看都只是孤立的字符串常量，
// 靠 internal/integration 的测试守护。
const (
	// ConfigTypeDefault 是后端服务读的分区。密钥类配置必须建在这里。
	ConfigTypeDefault = "DEFAULT"
	// ConfigTypeWeb 会被业务方转发给浏览器的分区。
	ConfigTypeWeb = "WEB"
)

// KeepaliveTime 是客户端向 fp 发送 keepalive ping 的间隔。
//
// 必须**大于** internal/grpcapi 服务端的 KeepaliveMinTime（10 秒），否则
// 服务端会认为客户端 ping 过频，回一个 ENHANCE_YOUR_CALM 的 GOAWAY 把
// 连接掐掉——双方都"配了 keepalive"，结果连接反而被周期性掐断，是这套
// 机制最经典的自伤方式。
//
// 这条配对关系分处两个包（sdk 不得 import internal/，internal/grpcapi
// 也不该反过来依赖 sdk 的实现细节），任何一边单独看都只是一个孤立的
// 时长常量，改错了 go build/vet/全量测试照样全绿。两者的配对由
// internal/integration 里的 TestKeepaliveTimingIsCompatible 守护——那是
// 唯一能同时看到这两个包的地方。
const KeepaliveTime = 30 * time.Second

// Client 是与 fp 的连接。它是并发安全的，一个进程建一个即可。
type Client struct {
	opts Options
	conn *grpc.ClientConn
	rpc  fpv1.AuthServiceClient
	// cfgRPC 是配置中心的 RPC 客户端，供 Bind/BindType 拉取配置用。
	cfgRPC fpv1.ConfigServiceClient

	// auth 与 authz 在 New 里构造一次，之后不再替换，因此无需同步保护。
	auth  *Auth
	authz *Authz

	// streamUp 是推送流的健康状态。它驱动缓存窗口的收紧，
	// 是"流断开时把安全性拉回来"这条策略的唯一输入。
	streamUp atomic.Bool

	// bindingsMu 保护 bindings：Bind/BindType 可能在任意 goroutine 里被
	// 业务方调用，注册与（Task 11 的）推送触发遍历必须互斥。
	bindingsMu sync.Mutex
	// bindings 是全部已注册的配置绑定，供收到 ConfigChanged 推送时逐个
	// 触发重新拉取（Task 11）。Binding[T] 是泛型，没法直接存进切片，
	// 靠 reloadable 这个非泛型接口。
	bindings []reloadable

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// reloadable 是 Client 对各份绑定的全部依赖。Binding[T] 是泛型，
// 没法直接存进一个切片，只能靠这个非泛型接口。
type reloadable interface {
	reloadFromPush()
}

// registerBinding 记录一份绑定，供收到该分区的 ConfigChanged 推送时
// （Task 11）触发它重新拉取。
func (c *Client) registerBinding(r reloadable) {
	c.bindingsMu.Lock()
	defer c.bindingsMu.Unlock()
	c.bindings = append(c.bindings, r)
}

// fetchConfig 拉取 typ 分区当前的全部已配置值。
//
// GetConfig 返回的 Values 是一整个 JSON 对象字符串，且**只含已配置的
// 项**——未配置的（服务端 value 为 JSON null）不会出现在里面，调用方
// 据此判断哪些字段缺失。
func (c *Client) fetchConfig(typ string) (map[string]json.RawMessage, error) {
	res, err := c.cfgRPC.GetConfig(context.Background(), &fpv1.GetConfigRequest{Type: typ})
	if err != nil {
		return nil, translate(err)
	}
	values := make(map[string]json.RawMessage)
	if err := json.Unmarshal([]byte(res.GetValues()), &values); err != nil {
		return nil, fmt.Errorf("fpsdk: 解析配置分区 %s 失败: %w", typ, err)
	}
	return values, nil
}

// Auth 返回认证能力。多次调用返回同一个实例。
func (c *Client) Auth() *Auth { return c.auth }

// Authz 返回鉴权入口。判定完全在本地完成、不走网络。
func (c *Client) Authz() *Authz { return c.authz }

// refreshPolicy 重拉一次策略快照。
//
// 拉失败只记日志、不动本地已有的快照：一次网络抖动不该让整个应用的鉴权
// 从"按上一份策略判定"退化成"全部拒绝"。策略是慢变数据，用旧一点的
// 远好过没有。
func (c *Client) refreshPolicy(ctx context.Context) {
	res, err := c.rpc.GetPolicy(ctx, &fpv1.GetPolicyRequest{})
	if status.Code(err) == codes.Unimplemented {
		// 这个 fp 部署没启用授权模块。安静跳过，不要当成故障反复告警——
		// 每次重连都刷一条 WARN 会把真正的问题淹掉。Allow 会一直返回
		// ErrPolicyUnavailable，业务方按自己的降级策略处理。
		return
	}
	if err != nil {
		c.opts.Logger.Warn("fpsdk: 拉取策略失败，继续沿用本地快照", "err", err)
		return
	}
	c.authz.setPolicy(res.GetPolicy())
	c.opts.Logger.Info("fpsdk: 策略已更新", "version", res.GetPolicy().GetVersion(),
		"roles", len(res.GetPolicy().GetRoles()))
}

// ReportPermissions 把本服务的权限点全量快照上报给 fp。
//
// 通常在启动时调一次，配合 sdk/fpchi 之类的框架适配器：
//
//	a := fpchi.New(client.Authz(), fpchi.StripPrefix("/api/v1"))
//	_ = client.ReportPermissions(ctx, a.Collect(router))
//
// 快照里没有的权限点**不会被 fp 删除**——它们会在控制台上转为"过渡中"
// 并显示已经多久没被上报，由人决定要不要清理。这是为了容忍滚动发布时
// 新旧版本同时在跑、交替上报。
func (c *Client) ReportPermissions(ctx context.Context, points []PermissionPoint) error {
	pts := make([]*fpv1.PermissionPoint, 0, len(points))
	for _, p := range points {
		kind := p.Kind
		if kind == "" {
			kind = PermissionKindAPI
		}
		pts = append(pts, &fpv1.PermissionPoint{
			Key: p.Key, Kind: kind, Parent: p.Parent, Name: p.Name,
		})
	}
	_, err := c.rpc.ReportPermissions(ctx, &fpv1.ReportPermissionsRequest{Points: pts})
	return translate(err)
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
		// Time 必须**大于**服务端的 EnforcementPolicy.MinTime——配对关系与
		// 为什么必须靠 internal/integration 里的测试守护，见 KeepaliveTime
		// 的注释。
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                KeepaliveTime,
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
	c := &Client{
		opts: opts, conn: conn,
		rpc:    fpv1.NewAuthServiceClient(conn),
		cfgRPC: fpv1.NewConfigServiceClient(conn),
		cancel: cancel,
	}
	// 必须在启动 watch goroutine 之前构造好：goroutine 一跑起来就可能调
	// c.auth（收到第一条 ready/revoke/purge 就会），构造顺序反了就是
	// nil 解引用——而且只在恰好有事件到达时才崩，本地测试多半复现不出来。
	c.auth = &Auth{c: c, cache: cch}
	c.authz = &Authz{}

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

// healthyConnDuration 是判定"这次连接足够健康、可以复位退避"所需的最短
// 存活时长。
//
// 仅仅"收到过 ready"不足以说明这次连接是健康的：fp 若出现"连上→吐一次
// ready→立刻断开"式抖动（比如刚起来就被压垮、或反复被探活探针打断），
// 一样会先发出 ready 再断开。只看 gotReady 复位的话，SDK 会以约
// 1/minBackoff（200ms，约每秒 5 次）的频率持续冲击一个本就不稳的服务端，
// 而不是随着连续失败让退避增长——这正是退避机制本来要防的事。加上这个
// 存活时长门槛，把"曾经收到过 ready"收紧成"曾经收到过 ready、且这次
// 连接撑过了这个门槛"，才有资格复位。
const healthyConnDuration = 5 * time.Second

// runWatch 维持推送流，断开后按退避重连，直到 ctx 取消。
func (c *Client) runWatch(ctx context.Context) {
	const (
		minBackoff = 200 * time.Millisecond
		maxBackoff = 30 * time.Second
	)
	backoff := minBackoff

	for ctx.Err() == nil {
		start := time.Now()
		gotReady, err := c.watchOnce(ctx)
		c.streamUp.Store(false)

		if ctx.Err() != nil {
			return
		}

		// 退避复位：这次连接不仅收到过 ready，还存活超过了
		// healthyConnDuration（见其注释），说明 fp 真的是健康的，这次
		// 断开大概率是短暂抖动（网络毛刺、fp 的 MaxConnectionAge 主动
		// 回收连接）而不是长时间故障，重连节奏应该从头开始，不能沿用
		// 断开前累积的退避——否则一次长时间的 fp 故障之后，后续任何一次
		// 短暂抖动都要等满 maxBackoff 才重连。没达到门槛的（连 ready 都
		// 没等到，或者收到 ready 后转瞬即断）都继续按原节奏增长。
		healthy := gotReady && time.Since(start) >= healthyConnDuration
		if healthy {
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
		if !healthy {
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
			// 重连后必须清空缓存：断开期间发生的撤销一条都没收到，缓存里
			// 的任何条目都可能是已被撤销的会话。首次连接也走这条路径，
			// 此时缓存本来就是空的，无害——**除非**调用方在 New() 返回
			// 之后、这条首条 ready 到达之前就已经发起了请求并写入了
			// 缓存；这种情况下无条件 purge 的代价只是那一次请求被迫再
			// 多打一次回源（下一次请求会立刻重新命中缓存），是一次性、
			// 自愈的性能成本。
			//
			// 这个代价必须承受，不能靠跳过首次 purge 来省：跳过会打开
			// 一个更贵的窗口——服务端订阅撤销频道之前收到的撤销事件本来
			// 就会漏掉（这正是本分支要清空缓存的原因），如果 fp 恰好在
			// "SDK 已建立连接、尚未收到首条 ready"这段时间里撤销了一个
			// 刚被首次请求缓存下来的 token，跳过首次 purge 会让这条
			// 已被撤销的缓存条目按其未收紧的原始 TTL 一直被信任，直到
			// 自然过期或者一次真正的重连——不确定性远大于"一次可避免的
			// 回源"。两害相权，无条件 purge 的代价更小、更好界定。
			//
			// 顺序很重要：先 purge、再把 streamUp 置为健康。反过来的话，
			// 两次调用之间有一条极窄但真实存在的竞态窗口——并发的
			// Validate() 一旦观察到 streamUp==true 就不再收紧查询窗口
			// （maxTTL 归零），如果此时 purge 还没跑完，它可能读到一条
			// 断连检测延迟期间（连接实际已断但 streamUp 尚未翻 false
			// 那段时间，最长一个 keepalive Timeout）写入的满 TTL 存量
			// 条目，把它当新鲜数据放行。先 purge 后置位，保证任何观察到
			// streamUp==true 的调用，看到的都已经是清空之后的缓存——
			// 这一顺序同时也是 Go 内存模型下的一次同步点：任何观察到
			// streamUp==true 的 goroutine，都保证能看到这次 purge
			// 已经完成（同一 goroutine 内 purge 在 Store 之前发生）。
			c.auth.onPurge()
			c.streamUp.Store(true)
			// 只有收到 ready 才置为健康：此前服务端可能还没订上撤销频道，
			// 那段时间的事件会丢，而 SDK 若已认为健康就不会收紧缓存窗口。
			gotReady = true
			// 流就绪后拉一次策略：新实例启动、以及每次重连之后，本地都要有
			// 一份可用的快照，否则 Allow 会一直返回"没能判定"。
			c.refreshPolicy(ctx)
		case msg.GetRevoke() != nil:
			c.auth.onRevoke(msg.GetRevoke())
		case msg.GetPurge() != nil:
			c.opts.Logger.Warn("fpsdk: 按服务端要求清空校验缓存",
				"reason", msg.GetPurge().GetReason())
			c.auth.onPurge()
			// 订阅重连后可能漏了策略变更，一并重拉。
			c.refreshPolicy(ctx)
		case msg.GetPolicyChanged() != nil:
			c.refreshPolicy(ctx)
		case msg.GetUserRoleChanged() != nil:
			// 只丢这个用户的缓存，不影响他的登录态——角色变更是**刷新**
			// 而不是撤销。下次校验时 fp 会带回新角色。
			c.auth.cache.dropUser(msg.GetUserRoleChanged().GetUserId())
		default:
			// 未知事件类型（将来的 ConfigChanged 等）。
			// 忽略，不要报错——oneof 的向前兼容就靠这里。
		}
	}
}
