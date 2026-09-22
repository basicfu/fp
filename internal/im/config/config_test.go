package config

import (
	"strings"
	"testing"
	"time"
)

// setRequiredEnv 把三个必填环境变量都设成能通过 FromEnv 的值，返回时
// 由 t.Setenv 自动在测试结束后还原。
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FP_IM_REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("FP_IM_FPSDK_ADDR", "fp.internal:9090")
	t.Setenv("FP_IM_FPSDK_SECRET", "im-secret")
}

func TestFromEnvReadsRequiredFieldsAndFillsDefaults(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.Redis.URL != "redis://localhost:6379/0" {
		t.Errorf("Redis.URL = %q", cfg.Redis.URL)
	}
	if cfg.FPSDK.Addr != "fp.internal:9090" {
		t.Errorf("FPSDK.Addr = %q", cfg.FPSDK.Addr)
	}
	if cfg.FPSDK.Secret != "im-secret" {
		t.Errorf("FPSDK.Secret = %q", cfg.FPSDK.Secret)
	}

	// 其余字段全部是写死的默认值，与 defaultConfig() 一致。
	if cfg.Env != "dev" || cfg.Log.Level != "info" {
		t.Errorf("env/log.level = %q/%q, want dev/info", cfg.Env, cfg.Log.Level)
	}
	if cfg.HTTP.Addr != ":8081" || cfg.HTTP.TrustProxy {
		t.Errorf("http = %+v, want addr :8081 且 trust_proxy false", cfg.HTTP)
	}
	if cfg.GRPC.Addr != ":9091" {
		t.Errorf("grpc.addr = %q, want :9091", cfg.GRPC.Addr)
	}
	if cfg.Node.Heartbeat.Std() != 3*time.Second || cfg.Node.DeadAfter.Std() != 10*time.Second {
		t.Errorf("node = %+v", cfg.Node)
	}
	if cfg.Conn.FieldTTL.Std() != 30*time.Minute || cfg.Conn.FieldRenew.Std() != 10*time.Minute ||
		cfg.Conn.IdleTimeout.Std() != 60*time.Second || cfg.Conn.AuthTimeout.Std() != 5*time.Second ||
		cfg.Conn.SendQueue != 256 {
		t.Errorf("conn = %+v", cfg.Conn)
	}
	if cfg.Pipeline.FlushInterval != 0 || cfg.Pipeline.FlushSize != 1 {
		t.Errorf("pipeline = %+v", cfg.Pipeline)
	}
}

func TestFromEnvRequiresRedisURL(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("FP_IM_REDIS_URL", "")

	_, err := FromEnv()
	if err == nil {
		t.Fatal("缺 FP_IM_REDIS_URL 必须报错")
	}
	if !strings.Contains(err.Error(), "FP_IM_REDIS_URL") {
		t.Errorf("错误信息 %q 里应当出现变量名 FP_IM_REDIS_URL", err)
	}
}

func TestFromEnvRequiresFPSDKAddr(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("FP_IM_FPSDK_ADDR", "")

	_, err := FromEnv()
	if err == nil {
		t.Fatal("缺 FP_IM_FPSDK_ADDR 必须报错")
	}
	if !strings.Contains(err.Error(), "FP_IM_FPSDK_ADDR") {
		t.Errorf("错误信息 %q 里应当出现变量名 FP_IM_FPSDK_ADDR", err)
	}
}

func TestFromEnvRequiresFPSDKSecret(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("FP_IM_FPSDK_SECRET", "")

	_, err := FromEnv()
	if err == nil {
		t.Fatal("缺 FP_IM_FPSDK_SECRET 必须报错")
	}
	if !strings.Contains(err.Error(), "FP_IM_FPSDK_SECRET") {
		t.Errorf("错误信息 %q 里应当出现变量名 FP_IM_FPSDK_SECRET", err)
	}
}

// TestDefaultConfigPassesValidate 钉住默认值本身自洽——写死的默认值要是
// 连自己的校验都过不了，FromEnv 在任何人手上都永远启动不了。
func TestDefaultConfigPassesValidate(t *testing.T) {
	if err := defaultConfig().validate(); err != nil {
		t.Fatalf("defaultConfig() 应当自己通过 validate()：%v", err)
	}
}

// TestDurationZeroIsValidFlushInterval：0s 是合法的 flush_interval——
// pipeline.flush_size 为 1（默认值）时"不合批"就是它。
func TestZeroFlushIntervalValidWhenFlushSizeIsOne(t *testing.T) {
	c := defaultConfig()
	c.Pipeline = Pipeline{FlushInterval: 0, FlushSize: 1}
	if err := c.validate(); err != nil {
		t.Fatalf("flush_size=1 时 flush_interval=0 应当合法：%v", err)
	}
}

func TestValidateRejectsNonPositiveSendQueue(t *testing.T) {
	c := defaultConfig()
	c.Conn.SendQueue = 0
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "conn.send_queue") {
		t.Fatalf("conn.send_queue 必须 >= 1，实际 %v", err)
	}
}

func TestValidateRejectsNonPositiveFlushSize(t *testing.T) {
	c := defaultConfig()
	c.Pipeline.FlushSize = 0
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "pipeline.flush_size") {
		t.Fatalf("pipeline.flush_size 必须 >= 1，实际 %v", err)
	}
}

func TestValidateRejectsFlushSizeAboveOneWithoutInterval(t *testing.T) {
	c := defaultConfig()
	c.Pipeline = Pipeline{FlushSize: 64}
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "pipeline.flush_interval") {
		t.Fatalf("flush_size>1 且无 flush_interval 必须报错，实际 %v", err)
	}
}

func TestValidateRejectsDeadAfterNotGreaterThanHeartbeat(t *testing.T) {
	c := defaultConfig()
	c.Node = Node{Heartbeat: Duration(10 * time.Second), DeadAfter: Duration(10 * time.Second)}
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "node.dead_after") {
		t.Fatalf("dead_after 必须大于 heartbeat，相等也要报错，实际 %v", err)
	}

	c.Node.DeadAfter = Duration(9 * time.Second)
	if err := c.validate(); err == nil {
		t.Fatal("dead_after 小于 heartbeat 更加不合理，必须报错")
	}
}

func TestValidateRejectsFieldRenewNotBelowHalfFieldTTL(t *testing.T) {
	c := defaultConfig()
	c.Conn.FieldRenew = Duration(20 * time.Minute) // 默认 FieldTTL 30m，20m*2 >= 30m
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "conn.field_renew") {
		t.Fatalf("field_renew 必须小于 field_ttl 的一半，实际 %v", err)
	}
}

// TestValidateRejectsIdleTimeoutBelowClientHeartbeat 是给空闲超时加下界的
// 那一半。另一半（下界这个数字确实等于 client SDK 心跳间隔的两倍）在
// internal/integration 的 TestClientPingAndIdleTimeoutPairing 里，那里同时
// 看得见 sdk/im 与本包；本包看不见 sdk/im，只能守住"下界被真的执行了"。
func TestValidateRejectsIdleTimeoutBelowClientHeartbeat(t *testing.T) {
	c := defaultConfig()
	c.Conn.IdleTimeout = Duration(20 * time.Second)
	if err := c.validate(); err == nil {
		t.Fatal("空闲超时低于 client 心跳间隔的两倍必须报错：" +
			"配成 20s 会让全网每个 client 每 20 秒被踢一次并立即重连（4005 的契约就是不退避），形成稳定的重连风暴")
	}

	// 恰好等于下界要放行：下界是"允许的最小值"，不是"必须严格大于"。
	c.Conn.IdleTimeout = Duration(MinIdleTimeout)
	if err := c.validate(); err != nil {
		t.Fatalf("空闲超时恰好等于下界应放行，实际报错：%v", err)
	}
}

// TestValidateRejectsFieldTTLBelowOneSecond：conn.field_ttl 会被
// registry.Conns.Handshake 换算成 int64(seconds) 传给 Redis 的 HEXPIRE，
// 小于一秒的值会被截断成 0，而 HEXPIRE 传 0 的语义是"立刻让这个 field 过期"
// ——把它配成几百毫秒，效果不是"缩短存活时长"，而是每条连接刚握手登记就被
// Redis 判定过期删除，client 无法查到自己是谁在线。必须在校验阶段就拒绝。
func TestValidateRejectsFieldTTLBelowOneSecond(t *testing.T) {
	c := defaultConfig()
	c.Conn.FieldRenew = Duration(100 * time.Millisecond)
	for _, ttl := range []time.Duration{500 * time.Millisecond, 999 * time.Millisecond} {
		c.Conn.FieldTTL = Duration(ttl)
		if err := c.validate(); err == nil {
			t.Fatalf("conn.field_ttl = %s 小于 1s，换算成整秒会被截断为 0，必须报错", ttl)
		}
	}
	c.Conn.FieldTTL = Duration(time.Second)
	if err := c.validate(); err != nil {
		t.Fatalf("conn.field_ttl 恰好等于 1s 应该通过：%v", err)
	}
}
