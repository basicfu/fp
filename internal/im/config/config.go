// Package config 读 fp-im 的环境变量。风格与 internal/config 一致：FP_IM_ 前缀，必填项缺失直接报错。
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Env        string
	HTTPAddr   string
	GRPCAddr   string
	RedisURL   string
	FPAddr     string
	FPInsecure bool
	AppsFile   string
	LogLevel   string
	TrustProxy bool
	Node       struct{ Heartbeat, DeadAfter time.Duration }
	Conn       struct {
		FieldTTL, FieldRenew, IdleTimeout, AuthTimeout time.Duration
		SendQueue                                      int
	}
	Pipeline struct {
		FlushInterval time.Duration
		FlushSize     int
	}
}

// MinIdleTimeout 是 FP_IM_CONN_IDLE_TIMEOUT 的下界：client SDK 心跳间隔
// 的两倍。
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
const MinIdleTimeout = 2 * 25 * time.Second

func Load() (*Config, error) {
	c := &Config{
		Env:        envOr("FP_IM_ENV", "DEV"),
		HTTPAddr:   envOr("FP_IM_HTTP_ADDR", ":8081"),
		GRPCAddr:   envOr("FP_IM_GRPC_ADDR", ":9091"),
		RedisURL:   os.Getenv("FP_IM_REDIS_URL"),
		FPAddr:     os.Getenv("FP_IM_FP_ADDR"),
		FPInsecure: envOr("FP_IM_FP_INSECURE", "true") == "true",
		AppsFile:   os.Getenv("FP_IM_APPS_FILE"),
		LogLevel:   envOr("FP_IM_LOG_LEVEL", "info"),
		TrustProxy: envOr("FP_IM_TRUST_PROXY", "false") == "true",
	}
	var missing []string
	for _, kv := range []struct{ k, v string }{{"FP_IM_REDIS_URL", c.RedisURL}, {"FP_IM_FP_ADDR", c.FPAddr}, {"FP_IM_APPS_FILE", c.AppsFile}} {
		if kv.v == "" {
			missing = append(missing, kv.k)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("config: 缺少必填环境变量 %s", strings.Join(missing, ", "))
	}
	var err error
	if c.Node.Heartbeat, err = dur("FP_IM_NODE_HEARTBEAT", 3*time.Second); err != nil {
		return nil, err
	}
	if c.Node.DeadAfter, err = dur("FP_IM_NODE_DEAD_AFTER", 10*time.Second); err != nil {
		return nil, err
	}
	if c.Conn.FieldTTL, err = dur("FP_IM_CONN_FIELD_TTL", 30*time.Minute); err != nil {
		return nil, err
	}
	if c.Conn.FieldRenew, err = dur("FP_IM_CONN_FIELD_RENEW", 10*time.Minute); err != nil {
		return nil, err
	}
	if c.Conn.IdleTimeout, err = dur("FP_IM_CONN_IDLE_TIMEOUT", 60*time.Second); err != nil {
		return nil, err
	}
	if c.Conn.AuthTimeout, err = dur("FP_IM_CONN_AUTH_TIMEOUT", 5*time.Second); err != nil {
		return nil, err
	}
	if c.Conn.SendQueue, err = num("FP_IM_CONN_SEND_QUEUE", 256); err != nil {
		return nil, err
	}
	if c.Pipeline.FlushInterval, err = dur("FP_IM_PIPELINE_FLUSH_INTERVAL", 0); err != nil {
		return nil, err
	}
	if c.Pipeline.FlushSize, err = num("FP_IM_PIPELINE_FLUSH_SIZE", 1); err != nil {
		return nil, err
	}
	if c.Pipeline.FlushSize > 1 && c.Pipeline.FlushInterval <= 0 {
		return nil, errors.New("config: FP_IM_PIPELINE_FLUSH_SIZE > 1 时必须设置 FP_IM_PIPELINE_FLUSH_INTERVAL")
	}
	if c.Node.DeadAfter <= c.Node.Heartbeat {
		return nil, errors.New("config: FP_IM_NODE_DEAD_AFTER 必须大于 FP_IM_NODE_HEARTBEAT")
	}
	if c.Conn.FieldRenew*2 >= c.Conn.FieldTTL {
		return nil, errors.New("config: FP_IM_CONN_FIELD_RENEW 必须小于 FP_IM_CONN_FIELD_TTL 的一半")
	}
	// 债务 4（复审遗留）：registry.Conns.Handshake 把 FieldTTL 换算成
	// int64(c.fieldTTL.Seconds()) 传给 Redis 的 HEXPIRE。小于一秒的值转换
	// 成整秒会被截断为 0，而 HEXPIRE 的字段 TTL 传 0 的语义是"让这个字段
	// 立刻过期"——不是"几乎不过期"，效果是每条连接刚握手登记就被 Redis
	// 删除，client 查不到自己是谁在线，现象极难定位到是这里的配置问题。
	// 所以这里直接拒绝小于 1 秒的配置，而不是留给运行期悄悄截断。
	if c.Conn.IdleTimeout < MinIdleTimeout {
		return nil, fmt.Errorf("config: FP_IM_CONN_IDLE_TIMEOUT 必须 >= %s（client SDK 每 25 秒发一次心跳，"+
			"空闲超时低于这个量级会让全网 client 被周期性踢下线，而 4005 的契约是立即重连不退避，形成重连风暴），当前为 %s",
			MinIdleTimeout, c.Conn.IdleTimeout)
	}
	if c.Conn.FieldTTL < time.Second {
		return nil, fmt.Errorf("config: FP_IM_CONN_FIELD_TTL 必须 >= 1s（会被换算成整秒传给 Redis HEXPIRE，小于一秒会截断为 0，语义是立刻删除），当前为 %s", c.Conn.FieldTTL)
	}
	return c, nil
}

func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func dur(k string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(k)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s 不是合法时长: %w", k, err)
	}
	return d, nil
}

func num(k string, fallback int) (int, error) {
	v := os.Getenv(k)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("config: %s 必须是正整数", k)
	}
	return n, nil
}
