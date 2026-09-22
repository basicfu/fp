// Package config 装配 fp-im 的启动配置。没有配置文件：FP_IM_REDIS_URL、
// FP_IM_FPSDK_ADDR、FP_IM_FPSDK_SECRET 三个环境变量必填，其余全部是写死
// 在 defaultConfig 里的默认值——不打算配置化的东西就不留一个"以后可能会
// 改"的口子。想改默认值，改代码、发新版本。
package config

import (
	"fmt"
	"os"
	"time"
)

// Duration 是内部使用的时长类型，纯粹为了让默认值声明处（defaultConfig）
// 读起来是"3 秒"而不是一串纳秒数。不再有 UnmarshalYAML：没有配置文件了，
// 这个类型不需要再知道怎么从 YAML 节点解出自己。
type Duration time.Duration

func (d Duration) Std() time.Duration { return time.Duration(d) }
func (d Duration) String() string     { return time.Duration(d).String() }

type Config struct {
	Env      string
	Log      Log
	HTTP     HTTP // client 的 WebSocket 接入
	GRPC     Listen
	Redis    Endpoint
	FPSDK    FPSDK
	Node     Node
	Conn     Conn
	Pipeline Pipeline
}

type Log struct {
	Level string
}

type Listen struct {
	Addr string
}

type HTTP struct {
	Addr string
	// TrustProxy 决定访客限流的 IP 取不取 X-Forwarded-For 的最右一跳。
	// 前面确实有可信反代时才该是 true——开在裸奔的服务上等于让客户端自己
	// 声明 IP。没有环境变量能改它，需要打开时改 defaultConfig 里的值。
	TrustProxy bool
}

type Endpoint struct {
	URL string
}

// FPSDK 是 fp SDK 的连接参数。Addr 是 fp 的 **gRPC** 地址，不是 HTTP。
type FPSDK struct {
	Addr string
	// Secret 是 IM 凭据，在 fp 控制台生成，全部 fp-im 实例共用同一份。
	Secret string
}

type Node struct {
	Heartbeat Duration
	DeadAfter Duration
}

type Conn struct {
	FieldTTL    Duration
	FieldRenew  Duration
	IdleTimeout Duration
	AuthTimeout Duration
	SendQueue   int
}

type Pipeline struct {
	FlushInterval Duration
	FlushSize     int
}

// MinIdleTimeout 是 conn.idle_timeout 的下界：client SDK 心跳间隔的两倍。
//
// 25 秒这个数字是硬编码抄过来的，对应 sdk/im 的 PingInterval——client SDK
// 在一条已建立的连接上每 25 秒发一次心跳帧。这里不能 import 那个常量：
// internal/ 不该反过来依赖 sdk 的实现细节，而 sdk/ 也不得 import
// internal/（见 sdk/arch_test.go），两边只能各写一份。真正把这两个数字
// 钉在一起的是 internal/integration 里的
// TestClientPingAndIdleTimeoutPairing——只有那里同时看得见两个包。
//
// 为什么要有下界：空闲超时一旦小于等于心跳间隔，全网每个 client 都会被
// 周期性地空闲超时踢下线，而 4005 的契约恰恰是"立即重连、不退避"，于是
// 形成一场稳定的重连风暴——配成 20 秒就够了。取两倍而不是刚好一倍，是
// 为了留出至少一个心跳周期的余量：网络抖动、client 忙、时钟漂移都可能让
// 某一次心跳晚到，只留一倍余量的话这些正常抖动就会变成断线。
//
// 类型保持 time.Duration（而不是本包的 Duration）：
// internal/integration/im_parity_test.go 拿它直接跟 fpim.PingInterval 比。
const MinIdleTimeout = 2 * 25 * time.Second

// defaultConfig 返回全部写死的默认值，FromEnv 只在这份默认值之上填三个
// 必填的环境变量。这三个数字（node/conn/pipeline 那一堆）就是以前
// config-im.example.yaml 里的默认值——直接抄过来，行为不变。
func defaultConfig() *Config {
	return &Config{
		Env:  "dev",
		Log:  Log{Level: "info"},
		HTTP: HTTP{Addr: ":8081"},
		GRPC: Listen{Addr: ":9091"},
		Node: Node{
			Heartbeat: Duration(3 * time.Second),
			DeadAfter: Duration(10 * time.Second),
		},
		Conn: Conn{
			FieldTTL:    Duration(30 * time.Minute),
			FieldRenew:  Duration(10 * time.Minute),
			IdleTimeout: Duration(60 * time.Second),
			AuthTimeout: Duration(5 * time.Second),
			SendQueue:   256,
		},
		Pipeline: Pipeline{FlushSize: 1},
	}
}

// requireEnv 读取 name 指向的环境变量，为空时返回一个点明是哪个变量
// 缺失的错误。不复用 internal/config.RequireEnv：那个包是 fp 自己的启动
// 配置，两个二进制的配置刻意互不引用、互不依赖，见本文件顶部的包注释。
func requireEnv(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("config: 环境变量 %s 未设置", name)
	}
	return v, nil
}

// FromEnv 读三个必填的环境变量，叠加在 defaultConfig 之上，校验通过后
// 返回。redis.url 之外的必填项已经没有了——fpsdk 的两项曾经"谁用谁校验"
// （由 fpauth.New 挡），现在提前到这里，因为它们本来就是这份配置仅有的
// 三个外部输入之一，不该有的缺了却要等装配到 fpauth 那一步才报错。
func FromEnv() (*Config, error) {
	redisURL, err := requireEnv("FP_IM_REDIS_URL")
	if err != nil {
		return nil, err
	}
	fpsdkAddr, err := requireEnv("FP_IM_FPSDK_ADDR")
	if err != nil {
		return nil, err
	}
	fpsdkSecret, err := requireEnv("FP_IM_FPSDK_SECRET")
	if err != nil {
		return nil, err
	}

	c := defaultConfig()
	c.Redis.URL = redisURL
	c.FPSDK.Addr = fpsdkAddr
	c.FPSDK.Secret = fpsdkSecret

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// validate 钉住几条跨字段的不变式。这些值现在全部来自写死的默认值，
// 正常情况下永远通过；留着不是防运行时输入出错，而是防以后有人改
// defaultConfig 时手滑改出一个自相矛盾的组合——测试直接构造 Config
// 调用它，不需要真的经过环境变量。
func (c *Config) validate() error {
	// 计数类的下界：send_queue: 0 会一路走到 hub 里变成一个零容量发送
	// 队列。
	if c.Conn.SendQueue < 1 {
		return fmt.Errorf("config: conn.send_queue 必须是正整数，当前为 %d", c.Conn.SendQueue)
	}
	if c.Pipeline.FlushSize < 1 {
		return fmt.Errorf("config: pipeline.flush_size 必须是正整数，当前为 %d", c.Pipeline.FlushSize)
	}
	if c.Pipeline.FlushSize > 1 && c.Pipeline.FlushInterval <= 0 {
		return fmt.Errorf("config: pipeline.flush_size > 1 时必须设置 pipeline.flush_interval")
	}
	if c.Node.DeadAfter <= c.Node.Heartbeat {
		return fmt.Errorf("config: node.dead_after 必须大于 node.heartbeat")
	}
	if c.Conn.FieldRenew*2 >= c.Conn.FieldTTL {
		return fmt.Errorf("config: conn.field_renew 必须小于 conn.field_ttl 的一半")
	}
	if c.Conn.IdleTimeout.Std() < MinIdleTimeout {
		return fmt.Errorf("config: conn.idle_timeout 必须 >= %s（client SDK 每 25 秒发一次心跳，"+
			"空闲超时低于这个量级会让全网 client 被周期性踢下线，而 4005 的契约是立即重连不退避，形成重连风暴），当前为 %s",
			MinIdleTimeout, c.Conn.IdleTimeout)
	}
	// registry.Conns.Handshake 把 field_ttl 换算成 int64(seconds) 传给 Redis
	// 的 HEXPIRE。小于一秒的值转换成整秒会被截断为 0，而 HEXPIRE 的字段 TTL
	// 传 0 的语义是"让这个字段立刻过期"——不是"几乎不过期"，效果是每条连接
	// 刚握手登记就被 Redis 删除，client 查不到自己是谁在线，现象极难定位到
	// 是这里的配置问题。所以这里直接拒绝，而不是留给运行期悄悄截断。
	if c.Conn.FieldTTL.Std() < time.Second {
		return fmt.Errorf("config: conn.field_ttl 必须 >= 1s"+
			"（会被换算成整秒传给 Redis HEXPIRE，小于一秒会截断为 0，语义是立刻删除），当前为 %s",
			c.Conn.FieldTTL)
	}
	return nil
}
