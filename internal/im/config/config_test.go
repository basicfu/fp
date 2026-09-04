package config

import (
	"testing"
	"time"
)

// setRequiredEnv 把 Load 的三个必填环境变量设成占位值，供不关心这三项本身
// 的测试复用。写法与 internal/config/config_test.go 的 setRequiredEnv 一致。
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FP_IM_REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("FP_IM_FP_ADDR", "localhost:9090")
	t.Setenv("FP_IM_APPS_FILE", "apps.json")
}

func TestLoadDefaultsAndRequired(t *testing.T) {
	setRequiredEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":8081" || cfg.GRPCAddr != ":9091" || cfg.Node.Heartbeat != 3*time.Second || cfg.Node.DeadAfter != 10*time.Second ||
		cfg.Conn.FieldTTL != 30*time.Minute || cfg.Conn.FieldRenew != 10*time.Minute || cfg.Conn.IdleTimeout != 60*time.Second ||
		cfg.Conn.AuthTimeout != 5*time.Second || cfg.Conn.SendQueue != 256 || cfg.Pipeline.FlushInterval != 0 || cfg.Pipeline.FlushSize != 1 {
		t.Fatalf("默认值与 spec 第十二节不符：%+v", cfg)
	}
	t.Setenv("FP_IM_REDIS_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("缺少必填变量必须报错，不能静默用默认值")
	}
}

func TestLoadRequiresFPAddrAndAppsFile(t *testing.T) {
	// 三个必填项各自单独缺失都要报错，不能只测 REDIS_URL 一项就断言
	// "必填校验生效"——那样漏掉另外两项各自的校验分支都覆盖不到。
	setRequiredEnv(t)
	t.Setenv("FP_IM_FP_ADDR", "")
	if _, err := Load(); err == nil {
		t.Fatal("缺少 FP_IM_FP_ADDR 必须报错")
	}
	setRequiredEnv(t)
	t.Setenv("FP_IM_APPS_FILE", "")
	if _, err := Load(); err == nil {
		t.Fatal("缺少 FP_IM_APPS_FILE 必须报错")
	}
}

func TestLoadRejectsBadPipelineAndRenew(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("FP_IM_PIPELINE_FLUSH_SIZE", "64")
	if _, err := Load(); err == nil {
		t.Fatal("flush_size>1 且无 flush_interval 必须报错")
	}
	t.Setenv("FP_IM_PIPELINE_FLUSH_SIZE", "1")
	t.Setenv("FP_IM_CONN_FIELD_RENEW", "20m")
	if _, err := Load(); err == nil {
		t.Fatal("field_renew 必须小于 field_ttl 的一半，否则续期赶不上过期")
	}
}

func TestLoadRejectsDeadAfterNotGreaterThanHeartbeat(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("FP_IM_NODE_HEARTBEAT", "10s")
	t.Setenv("FP_IM_NODE_DEAD_AFTER", "10s")
	if _, err := Load(); err == nil {
		t.Fatal("dead_after 必须大于 heartbeat，相等也要报错")
	}
	t.Setenv("FP_IM_NODE_DEAD_AFTER", "9s")
	if _, err := Load(); err == nil {
		t.Fatal("dead_after 小于 heartbeat 更加不合理，必须报错")
	}
}

// TestLoadRejectsFieldTTLBelowOneSecond 覆盖债务 4：FP_IM_CONN_FIELD_TTL 会
// 被 registry.Conns.Handshake 换算成 int64(seconds) 传给 Redis 的 HEXPIRE，
// 小于一秒的值会被截断成 0，而 HEXPIRE 传 0 的语义是"立刻让这个 field 过期"
// ——也就是说，把这个值配成几百毫秒，效果不是"缩短存活时长"，而是每条连接
// 刚握手登记就被 Redis 判定过期删除，client 无法查到自己是谁在线。必须在
// 配置加载阶段就拒绝，不能等到线上排查"连接注册了但查不到"这种诡异现象。
func TestLoadRejectsFieldTTLBelowOneSecond(t *testing.T) {
	// field_renew 默认 10m，而这里测的 field_ttl 都远小于 1s：不显式把
	// renew 也调小的话，renew*2 >= ttl 那条既有校验会先一步报错，测试就
	// 验证不到本条（债务 4）新加的下界校验到底有没有生效。三个子用例统一
	// 把 renew 设成 100ms（renew*2=200ms），只在 field_ttl=1s 时不足以
	// 触发 renew 那条规则，从而把"报错"或"通过"精确归因到 field_ttl 本身。
	setRequiredEnv(t)
	t.Setenv("FP_IM_CONN_FIELD_RENEW", "100ms")

	t.Setenv("FP_IM_CONN_FIELD_TTL", "500ms")
	if _, err := Load(); err == nil {
		t.Fatal("FP_IM_CONN_FIELD_TTL 小于 1s 换算成整秒会被截断为 0，必须报错")
	}
	t.Setenv("FP_IM_CONN_FIELD_TTL", "999ms")
	if _, err := Load(); err == nil {
		t.Fatal("FP_IM_CONN_FIELD_TTL 仍小于 1s（999ms），必须报错")
	}
	t.Setenv("FP_IM_CONN_FIELD_TTL", "1s")
	if _, err := Load(); err != nil {
		t.Fatalf("FP_IM_CONN_FIELD_TTL 恰好等于 1s 应该通过：%v", err)
	}
}

// TestLoadRejectsIdleTimeoutBelowClientHeartbeat 是乙三的一半：配置加载时
// 给空闲超时加下界。
//
// 另一半（下界这个数字确实等于 client SDK 心跳间隔的两倍）在
// internal/integration 的 TestClientPingAndIdleTimeoutPairing 里，那里同时
// 看得见 sdk/im 与本包；本包看不见 sdk/im，只能守住"下界被真的执行了"。
func TestLoadRejectsIdleTimeoutBelowClientHeartbeat(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("FP_IM_CONN_IDLE_TIMEOUT", "20s")
	if _, err := Load(); err == nil {
		t.Fatal("空闲超时低于 client 心跳间隔的两倍必须报错：" +
			"配成 20s 会让全网每个 client 每 20 秒被踢一次并立即重连（4005 的契约就是不退避），形成稳定的重连风暴")
	}
	// 恰好等于下界要放行：下界是"允许的最小值"，不是"必须严格大于"。
	t.Setenv("FP_IM_CONN_IDLE_TIMEOUT", MinIdleTimeout.String())
	if _, err := Load(); err != nil {
		t.Fatalf("空闲超时恰好等于下界应放行，实际报错：%v", err)
	}
}
