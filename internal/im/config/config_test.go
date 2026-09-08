package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config-im.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("写临时配置文件：%v", err)
	}
	return p
}

// minimal 只写必填项，供只关心默认值或某一项的测试复用。
const minimal = `
redis:
  url: redis://localhost:6379/0
apps_file: apps.json
`

// TestLoadReadsEveryField 逐字段断言，是 yaml tag 的护栏——多词字段
// （dead_after、field_ttl、trust_proxy、flush_interval…）漏写 tag 会被
// yaml.v3 按"字段名整个小写"映射成 deadafter 之类，配置文件里的正确写法
// 反而变成未知键。
func TestLoadReadsEveryField(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
env: prod
log:
  level: debug
http:
  addr: ":18081"
  trust_proxy: true
grpc:
  addr: ":19091"
redis:
  url: redis://:pw@h:6379/2
fpsdk:
  addr: fp.internal:9090
apps_file: ./tmp/im-apps.json
node:
  heartbeat: 1s
  dead_after: 4s
conn:
  field_ttl: 20m
  field_renew: 5m
  idle_timeout: 90s
  auth_timeout: 3s
  send_queue: 128
pipeline:
  flush_interval: 5ms
  flush_size: 64
`))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Env != "prod" || cfg.Log.Level != "debug" {
		t.Errorf("env/log.level = %q/%q, want prod/debug", cfg.Env, cfg.Log.Level)
	}
	if cfg.HTTP.Addr != ":18081" || !cfg.HTTP.TrustProxy {
		t.Errorf("http = %+v, want addr :18081 且 trust_proxy true", cfg.HTTP)
	}
	if cfg.GRPC.Addr != ":19091" {
		t.Errorf("grpc.addr = %q, want :19091", cfg.GRPC.Addr)
	}
	if cfg.Redis.URL != "redis://:pw@h:6379/2" {
		t.Errorf("redis.url = %q", cfg.Redis.URL)
	}
	if cfg.FPSDK.Addr != "fp.internal:9090" {
		t.Errorf("fpsdk.addr = %q, want fp.internal:9090", cfg.FPSDK.Addr)
	}
	if cfg.AppsFile != "./tmp/im-apps.json" {
		t.Errorf("apps_file = %q", cfg.AppsFile)
	}
	if cfg.Node.Heartbeat.Std() != time.Second || cfg.Node.DeadAfter.Std() != 4*time.Second {
		t.Errorf("node = %+v, want heartbeat 1s / dead_after 4s", cfg.Node)
	}
	if cfg.Conn.FieldTTL.Std() != 20*time.Minute || cfg.Conn.FieldRenew.Std() != 5*time.Minute ||
		cfg.Conn.IdleTimeout.Std() != 90*time.Second || cfg.Conn.AuthTimeout.Std() != 3*time.Second ||
		cfg.Conn.SendQueue != 128 {
		t.Errorf("conn = %+v", cfg.Conn)
	}
	if cfg.Pipeline.FlushInterval.Std() != 5*time.Millisecond || cfg.Pipeline.FlushSize != 64 {
		t.Errorf("pipeline = %+v", cfg.Pipeline)
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Env != "dev" || cfg.Log.Level != "info" {
		t.Errorf("env/log.level = %q/%q, want dev/info", cfg.Env, cfg.Log.Level)
	}
	if cfg.HTTP.Addr != ":8081" || cfg.HTTP.TrustProxy {
		t.Errorf("http = %+v, want addr :8081 且 trust_proxy false", cfg.HTTP)
	}
	if cfg.GRPC.Addr != ":9091" {
		t.Errorf("grpc.addr = %q, want :9091", cfg.GRPC.Addr)
	}
	if cfg.Node.Heartbeat.Std() != 3*time.Second || cfg.Node.DeadAfter.Std() != 10*time.Second ||
		cfg.Conn.FieldTTL.Std() != 30*time.Minute || cfg.Conn.FieldRenew.Std() != 10*time.Minute ||
		cfg.Conn.IdleTimeout.Std() != 60*time.Second || cfg.Conn.AuthTimeout.Std() != 5*time.Second ||
		cfg.Conn.SendQueue != 256 || cfg.Pipeline.FlushInterval != 0 || cfg.Pipeline.FlushSize != 1 {
		t.Fatalf("默认值与设计文档第四节不符：%+v", cfg)
	}
}

// TestLoadDoesNotRequireFPSDKAddr 钉住"谁用谁校验"：config 包不知道谁会用
// 这个地址，不该替使用方决定它是不是必需。空值仍会在启动时炸，但那是
// fpauth.New 的职责（见 internal/im/fpauth 的 TestNewRequiresFPAddr）。
func TestLoadDoesNotRequireFPSDKAddr(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatalf("Load() error = %v，fpsdk.addr 不该是 config 的必填项", err)
	}
	if cfg.FPSDK.Addr != "" {
		t.Errorf("fpsdk.addr = %q, want empty", cfg.FPSDK.Addr)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	_, err := Load(writeConfig(t, minimal+`
conn:
  idle_timout: 90s
`))
	if err == nil {
		t.Fatal("拼错的键必须报错，不能静默用默认值")
	}
	if !strings.Contains(err.Error(), "idle_timout") {
		t.Errorf("错误信息 %q 里应当出现拼错的那个键名", err)
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "不存在.yaml")); err == nil {
		t.Fatal("文件不存在必须报错")
	}
}

// TestLoadRequiresRedisAndAppsFile 两个必填项各自单独缺失都要报错，
// 且错误信息用 YAML 路径而不是环境变量名。
func TestLoadRequiresRedisAndAppsFile(t *testing.T) {
	_, err := Load(writeConfig(t, "apps_file: apps.json\n"))
	if err == nil || !strings.Contains(err.Error(), "redis.url") {
		t.Fatalf("缺 redis.url 必须报错且信息里含 redis.url，实际 %v", err)
	}
	if strings.Contains(err.Error(), "FP_IM_") {
		t.Errorf("错误信息 %q 里不该再出现环境变量名", err)
	}
	_, err = Load(writeConfig(t, "redis:\n  url: redis://localhost:6379/0\n"))
	if err == nil || !strings.Contains(err.Error(), "apps_file") {
		t.Fatalf("缺 apps_file 必须报错且信息里含 apps_file，实际 %v", err)
	}
}

// TestDurationRejectsBareNumber：配置里写 2 是想表达 2 秒还是 2 纳秒，
// 没人说得清，与其猜一个不如让配置加载直接失败。与 model.Duration 同一纪律。
func TestDurationRejectsBareNumber(t *testing.T) {
	if _, err := Load(writeConfig(t, minimal+"node:\n  heartbeat: 2\n")); err == nil {
		t.Fatal("裸数字时长必须报错")
	}
	if _, err := Load(writeConfig(t, minimal+"node:\n  heartbeat: 不是时长\n")); err == nil {
		t.Fatal("非法时长必须报错，而不是静默变成 0")
	}
	// 0s 是合法的：pipeline.flush_interval 的"不合批"就是它。
	if _, err := Load(writeConfig(t, minimal+"pipeline:\n  flush_interval: 0s\n")); err != nil {
		t.Fatalf("0s 应当合法：%v", err)
	}
}

// TestInsecureDerivesFromEnv 钉住设计第六节：删掉 insecure 旋钮之后，fp-im
// 连 fp 走不走明文完全由 env 决定。这条判定有真实的安全后果——appSecret 随
// 每个 RPC 的 metadata 发送，明文传输等于把一个能签发任意用户会话的凭据
// 印在网线上。
func TestInsecureDerivesFromEnv(t *testing.T) {
	for _, tt := range []struct {
		env  string
		want bool
	}{
		{"dev", true},
		{"", true},
		{"prod", false},
		{"PROD", false},
		{"Prod", false},
		{"production", true}, // 只认 prod，不做前缀匹配
	} {
		if got := (&Config{Env: tt.env}).Insecure(); got != tt.want {
			t.Errorf("Env=%q Insecure() = %v, want %v", tt.env, got, tt.want)
		}
	}
}

func TestLoadRejectsBadPipelineAndRenew(t *testing.T) {
	_, err := Load(writeConfig(t, minimal+"pipeline:\n  flush_size: 64\n"))
	if err == nil || !strings.Contains(err.Error(), "pipeline.flush_interval") {
		t.Fatalf("flush_size>1 且无 flush_interval 必须报错，实际 %v", err)
	}
	_, err = Load(writeConfig(t, minimal+"conn:\n  field_renew: 20m\n"))
	if err == nil || !strings.Contains(err.Error(), "conn.field_renew") {
		t.Fatalf("field_renew 必须小于 field_ttl 的一半，实际 %v", err)
	}
}

// TestLoadRejectsNonPositiveCounts 覆盖旧 num() 助手曾经承担的"必须是正整数"
// 校验：换成 YAML 之后 yaml.v3 只管把整数解出来，下界得自己判，否则
// send_queue: 0 会一路走到 hub 里变成一个零容量发送队列。
func TestLoadRejectsNonPositiveCounts(t *testing.T) {
	if _, err := Load(writeConfig(t, minimal+"conn:\n  send_queue: 0\n")); err == nil {
		t.Fatal("conn.send_queue 必须 >= 1")
	}
	if _, err := Load(writeConfig(t, minimal+"pipeline:\n  flush_size: 0\n")); err == nil {
		t.Fatal("pipeline.flush_size 必须 >= 1")
	}
}

func TestLoadRejectsDeadAfterNotGreaterThanHeartbeat(t *testing.T) {
	_, err := Load(writeConfig(t, minimal+"node:\n  heartbeat: 10s\n  dead_after: 10s\n"))
	if err == nil || !strings.Contains(err.Error(), "node.dead_after") {
		t.Fatalf("dead_after 必须大于 heartbeat，相等也要报错，实际 %v", err)
	}
	if _, err := Load(writeConfig(t, minimal+"node:\n  heartbeat: 10s\n  dead_after: 9s\n")); err == nil {
		t.Fatal("dead_after 小于 heartbeat 更加不合理，必须报错")
	}
}

// TestLoadRejectsFieldTTLBelowOneSecond：conn.field_ttl 会被
// registry.Conns.Handshake 换算成 int64(seconds) 传给 Redis 的 HEXPIRE，
// 小于一秒的值会被截断成 0，而 HEXPIRE 传 0 的语义是"立刻让这个 field 过期"
// ——把它配成几百毫秒，效果不是"缩短存活时长"，而是每条连接刚握手登记就被
// Redis 判定过期删除，client 无法查到自己是谁在线。必须在配置加载阶段就
// 拒绝，不能等到线上排查"连接注册了但查不到"这种诡异现象。
//
// field_renew 默认 10m 而这里的 field_ttl 都远小于 1s：不显式把 renew 也
// 调小的话，renew*2 >= ttl 那条既有校验会先一步报错，就验证不到本条。
func TestLoadRejectsFieldTTLBelowOneSecond(t *testing.T) {
	for _, ttl := range []string{"500ms", "999ms"} {
		body := minimal + "conn:\n  field_renew: 100ms\n  field_ttl: " + ttl + "\n"
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Fatalf("conn.field_ttl = %s 小于 1s，换算成整秒会被截断为 0，必须报错", ttl)
		}
	}
	body := minimal + "conn:\n  field_renew: 100ms\n  field_ttl: 1s\n"
	if _, err := Load(writeConfig(t, body)); err != nil {
		t.Fatalf("conn.field_ttl 恰好等于 1s 应该通过：%v", err)
	}
}

// TestLoadRejectsIdleTimeoutBelowClientHeartbeat 是给空闲超时加下界的那一半。
//
// 另一半（下界这个数字确实等于 client SDK 心跳间隔的两倍）在
// internal/integration 的 TestClientPingAndIdleTimeoutPairing 里，那里同时
// 看得见 sdk/im 与本包；本包看不见 sdk/im，只能守住"下界被真的执行了"。
func TestLoadRejectsIdleTimeoutBelowClientHeartbeat(t *testing.T) {
	if _, err := Load(writeConfig(t, minimal+"conn:\n  idle_timeout: 20s\n")); err == nil {
		t.Fatal("空闲超时低于 client 心跳间隔的两倍必须报错：" +
			"配成 20s 会让全网每个 client 每 20 秒被踢一次并立即重连（4005 的契约就是不退避），形成稳定的重连风暴")
	}
	// 恰好等于下界要放行：下界是"允许的最小值"，不是"必须严格大于"。
	body := minimal + "conn:\n  idle_timeout: " + MinIdleTimeout.String() + "\n"
	if _, err := Load(writeConfig(t, body)); err != nil {
		t.Fatalf("空闲超时恰好等于下界应放行，实际报错：%v", err)
	}
}
