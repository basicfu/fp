// Package config 从 YAML 文件加载 fp-im 的启动配置。风格与 internal/config
// 一致：字段树与 config-im.yaml 1:1 同构，每个字段显式写 yaml tag，解码开
// KnownFields(true)，必填项缺失直接报错。
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultPath 是 -c 未指定时读的配置文件。
const DefaultPath = "config-im.yaml"

// Duration 是配置里写成 "3s" 这种人类可读时长的配置项。
//
// 不直接用 time.Duration：yaml.v3 会把它当成一个 int64 纳秒数，配置文件里
// 写 3000000000 既难读又容易错一个数量级。与 internal/im/model.Duration 是
// 同一件事的 YAML 版本；两者不合并，因为那个服务的是 apps 文件（JSON），
// 而 internal/im/config 不该为了复用一个十行的类型去 import internal/im/model。
type Duration time.Duration

func (d Duration) Std() time.Duration { return time.Duration(d) }
func (d Duration) String() string     { return time.Duration(d).String() }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	// 明确拒绝裸数字：写 2 是 2 秒还是 2 纳秒，没人说得清，与其猜一个
	// 不如让配置加载直接失败。yaml.v3 解一个 !!int 节点进 string 会报错，
	// 这里正是要那个错误。
	if err := n.Decode(&s); err != nil {
		return fmt.Errorf("config: 时长必须是带单位的字符串，例如 \"3s\"：%w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("config: 无法解析时长 %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

type Config struct {
	Env      string   `yaml:"env"` // dev / prod，大小写不敏感
	Log      Log      `yaml:"log"`
	HTTP     HTTP     `yaml:"http"` // client 的 WebSocket 接入
	GRPC     Listen   `yaml:"grpc"` // 业务 server 接入
	Redis    Endpoint `yaml:"redis"`
	FPSDK    FPSDK    `yaml:"fpsdk"`
	Node     Node     `yaml:"node"`
	Conn     Conn     `yaml:"conn"`
	Pipeline Pipeline `yaml:"pipeline"`
}

type Log struct {
	Level string `yaml:"level"`
}

type Listen struct {
	Addr string `yaml:"addr"`
}

type HTTP struct {
	Addr string `yaml:"addr"`
	// TrustProxy 决定访客限流的 IP 取不取 X-Forwarded-For 的最右一跳。
	// 前面确实有可信反代时才开——开在裸奔的服务上等于让客户端自己声明 IP。
	TrustProxy bool `yaml:"trust_proxy"`
}

type Endpoint struct {
	URL string `yaml:"url"`
}

// FPSDK 是 fp SDK 的连接参数。Addr 是 fp 的 **gRPC** 地址，不是 HTTP。
//
// 这里没有 insecure 开关：传输安全由 Config.Insecure() 从 env 推导。一个
// 默认值为 true 的 insecure 旋钮，最可能的失效方式就是有人把它连同整份 dev
// 配置抄到生产上，而 appSecret 随每个 RPC 的 metadata 明文发送，抓到包就
// 等于拿到一个能签发任意用户会话的凭据。
//
// Addr 为空**不由本包校验**——config 不知道谁会用这个地址，"谁用谁校验"。
// 空值仍然会在启动装配阶段炸掉，那是 fpauth.New 的职责（见那里的
// TestNewRequiresFPAddr）。
type FPSDK struct {
	Addr string `yaml:"addr"`
	// Secret 是 IM 凭据，在 fp 控制台生成，全部 fp-im 实例共用同一份。
	//
	// 与 Addr 一样**不由本包校验**——config 不知道谁会用它，"谁用谁校验"。
	// 两项为空都会在装配阶段被 fpauth.New 挡住，报错时机仍是启动时。
	Secret string `yaml:"secret"`
}

type Node struct {
	Heartbeat Duration `yaml:"heartbeat"`
	DeadAfter Duration `yaml:"dead_after"`
}

type Conn struct {
	FieldTTL    Duration `yaml:"field_ttl"`
	FieldRenew  Duration `yaml:"field_renew"`
	IdleTimeout Duration `yaml:"idle_timeout"`
	AuthTimeout Duration `yaml:"auth_timeout"`
	SendQueue   int      `yaml:"send_queue"`
}

type Pipeline struct {
	FlushInterval Duration `yaml:"flush_interval"`
	FlushSize     int      `yaml:"flush_size"`
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

// IsProd 报告当前是否为生产环境。
func (c *Config) IsProd() bool { return strings.EqualFold(c.Env, "prod") }

// Insecure 报告 fp-im 连 fp 的 gRPC 是否走明文。
//
// **前提**：fp 的 gRPC 服务端目前没有传 grpc.Creds，只服务明文，所以
// env: prod 下这条 TLS 必须由 fp 前面的反代 / 网关终结。见 docs/im.md。
func (c *Config) Insecure() bool { return !c.IsProd() }

// Load 读 path 指向的 YAML 文件，填默认值并校验。
func Load(path string) (*Config, error) {
	// 先填默认值再解码：yaml.v3 只写文档里出现过的字段，没出现的原样保留。
	c := &Config{
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
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: 打开配置文件 %s: %w", path, err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config: 解析 %s: %w", path, err)
	}

	var missing []string
	// fpsdk.addr 刻意不在这个清单里，理由见 FPSDK 的注释。
	for _, kv := range []struct{ path, v string }{
		{"redis.url", c.Redis.URL},
	} {
		if kv.v == "" {
			missing = append(missing, kv.path)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("config: %s 缺少必填项 %s", path, strings.Join(missing, ", "))
	}

	// 计数类的下界：旧版靠 num() 助手统一挡，换成 YAML 之后 yaml.v3 只管把
	// 整数解出来，下界得自己判——send_queue: 0 会一路走到 hub 里变成一个
	// 零容量发送队列。
	if c.Conn.SendQueue < 1 {
		return nil, fmt.Errorf("config: %s 里 conn.send_queue 必须是正整数，当前为 %d", path, c.Conn.SendQueue)
	}
	if c.Pipeline.FlushSize < 1 {
		return nil, fmt.Errorf("config: %s 里 pipeline.flush_size 必须是正整数，当前为 %d", path, c.Pipeline.FlushSize)
	}
	if c.Pipeline.FlushSize > 1 && c.Pipeline.FlushInterval <= 0 {
		return nil, fmt.Errorf("config: %s 里 pipeline.flush_size > 1 时必须设置 pipeline.flush_interval", path)
	}
	if c.Node.DeadAfter <= c.Node.Heartbeat {
		return nil, fmt.Errorf("config: %s 里 node.dead_after 必须大于 node.heartbeat", path)
	}
	if c.Conn.FieldRenew*2 >= c.Conn.FieldTTL {
		return nil, fmt.Errorf("config: %s 里 conn.field_renew 必须小于 conn.field_ttl 的一半", path)
	}
	if c.Conn.IdleTimeout.Std() < MinIdleTimeout {
		return nil, fmt.Errorf("config: %s 里 conn.idle_timeout 必须 >= %s（client SDK 每 25 秒发一次心跳，"+
			"空闲超时低于这个量级会让全网 client 被周期性踢下线，而 4005 的契约是立即重连不退避，形成重连风暴），当前为 %s",
			path, MinIdleTimeout, c.Conn.IdleTimeout)
	}
	// registry.Conns.Handshake 把 field_ttl 换算成 int64(seconds) 传给 Redis
	// 的 HEXPIRE。小于一秒的值转换成整秒会被截断为 0，而 HEXPIRE 的字段 TTL
	// 传 0 的语义是"让这个字段立刻过期"——不是"几乎不过期"，效果是每条连接
	// 刚握手登记就被 Redis 删除，client 查不到自己是谁在线，现象极难定位到
	// 是这里的配置问题。所以这里直接拒绝，而不是留给运行期悄悄截断。
	if c.Conn.FieldTTL.Std() < time.Second {
		return nil, fmt.Errorf("config: %s 里 conn.field_ttl 必须 >= 1s"+
			"（会被换算成整秒传给 Redis HEXPIRE，小于一秒会截断为 0，语义是立刻删除），当前为 %s",
			path, c.Conn.FieldTTL)
	}
	return c, nil
}
