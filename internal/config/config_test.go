package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig 把 body 写成一个临时的 config.yaml 并返回它的路径。
// 每条测试一个独立的 t.TempDir()，互不干扰；也不再有旧版 t.Setenv 那种
// "本机 .env.local 恰好设了同名变量"的污染问题——文件的内容完全由测试决定。
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("写临时配置文件：%v", err)
	}
	return p
}

// minimal 是只写了必填项的最小配置，供只关心默认值或某一项的测试复用。
const minimal = `
postgres:
  url: postgres://x/y
redis:
  url: redis://localhost:6379/0
`

// TestLoadReadsEveryField 逐字段断言，是 yaml tag 的护栏。
//
// 漏写 tag 的多词字段（比如 BootstrapAdmin 上漏了 `yaml:"bootstrap_admin"`）
// 会被 yaml.v3 按"字段名整个小写"的默认规则映射到 bootstrapadmin，于是配置
// 文件里的 bootstrap_admin 变成未知键——KnownFields(true) 会把它报成"配置
// 文件写错了"，指错方向。只有逐字段断言读到的值才能把这类错误钉在加载器上。
func TestLoadReadsEveryField(t *testing.T) {
	p := writeConfig(t, `
env: prod
log:
  level: debug
http:
  addr: ":18080"
grpc:
  addr: ":19090"
postgres:
  url: postgres://u:p@h:5432/fp?sslmode=disable
redis:
  url: redis://:pw@h:6379/0
bootstrap_admin:
  user: root
  password: s3cret
sms:
  aliyun:
    access_key_id: ak
    access_key_secret: sk
    sign_name: 测试签名
    template_login_code: SMS_0001
    endpoint: dysmsapi.example.com
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, tc := range []struct{ name, got, want string }{
		{"env", cfg.Env, "prod"},
		{"log.level", cfg.Log.Level, "debug"},
		{"http.addr", cfg.HTTP.Addr, ":18080"},
		{"grpc.addr", cfg.GRPC.Addr, ":19090"},
		{"postgres.url", cfg.Postgres.URL, "postgres://u:p@h:5432/fp?sslmode=disable"},
		{"redis.url", cfg.Redis.URL, "redis://:pw@h:6379/0"},
		{"bootstrap_admin.user", cfg.BootstrapAdmin.User, "root"},
		{"bootstrap_admin.password", cfg.BootstrapAdmin.Password, "s3cret"},
		{"sms.aliyun.access_key_id", cfg.SMS.Aliyun.AccessKeyID, "ak"},
		{"sms.aliyun.access_key_secret", cfg.SMS.Aliyun.AccessKeySecret, "sk"},
		{"sms.aliyun.sign_name", cfg.SMS.Aliyun.SignName, "测试签名"},
		{"sms.aliyun.template_login_code", cfg.SMS.Aliyun.TemplateLoginCode, "SMS_0001"},
		{"sms.aliyun.endpoint", cfg.SMS.Aliyun.Endpoint, "dysmsapi.example.com"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Env != "dev" {
		t.Errorf("Env = %q, want dev", cfg.Env)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("Log.Level = %q, want info", cfg.Log.Level)
	}
	if cfg.HTTP.Addr != ":8080" {
		t.Errorf("HTTP.Addr = %q, want :8080", cfg.HTTP.Addr)
	}
	if cfg.GRPC.Addr != ":9090" {
		t.Errorf("GRPC.Addr = %q, want :9090", cfg.GRPC.Addr)
	}
}

// TestLoadRejectsUnknownField 是这次从环境变量迁到文件净新增的能力：
// 环境变量那个介质压根没有"这个键我不认识"的概念，拼错只会静默回落到
// 默认值；文件有，所以必须报错。
func TestLoadRejectsUnknownField(t *testing.T) {
	_, err := Load(writeConfig(t, minimal+`
log:
  lvel: debug
`))
	if err == nil {
		t.Fatal("拼错的键必须报错，不能静默用默认值")
	}
	if !strings.Contains(err.Error(), "lvel") {
		t.Errorf("错误信息 %q 里应当出现拼错的那个键名 lvel", err)
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "不存在.yaml")
	if _, err := Load(p); err == nil {
		t.Fatal("文件不存在必须报错，不能回落到一整套默认值")
	}
}

// TestLoadMissingRequired 同时钉住"报错"和"错误信息用 YAML 路径而不是
// 环境变量名"——后者才是这次迁移对排障的实际改善。
func TestLoadMissingRequired(t *testing.T) {
	_, err := Load(writeConfig(t, "redis:\n  url: redis://localhost:6379/0\n"))
	if err == nil {
		t.Fatal("缺 postgres.url 必须报错")
	}
	if !strings.Contains(err.Error(), "postgres.url") {
		t.Errorf("错误信息 %q 里应当出现 YAML 路径 postgres.url", err)
	}
	if strings.Contains(err.Error(), "FP_POSTGRES_URL") {
		t.Errorf("错误信息 %q 里不该再出现环境变量名", err)
	}
}

// TestIsProd 钉住大小写不敏感与默认值。IsProd 决定管理端 cookie 带不带
// Secure，是个真正有安全后果的判定。
func TestIsProd(t *testing.T) {
	for _, tt := range []struct {
		env  string
		want bool
	}{
		{"prod", true},
		{"PROD", true},
		{"Prod", true},
		{"dev", false},
		{"", false},
		{"production", false}, // 只认 prod，不做前缀匹配
	} {
		if got := (&Config{Env: tt.env}).IsProd(); got != tt.want {
			t.Errorf("Env=%q IsProd() = %v, want %v", tt.env, got, tt.want)
		}
	}
}

// TestLoadMissingAliyunRequiredInProd 钉住阿里云短信凭据在生产环境是必填项。
//
// 没有它的话，Load 会允许生产环境下阿里云凭据全部留空——cmd/fp/main.go 会
// 因此在生产环境悄悄退化成假供应商，验证码只进内存、没有任何真实用户能收到，
// 且不会有任何启动期报错，只会在运营发现"用户投诉收不到验证码"时才暴露。
func TestLoadMissingAliyunRequiredInProd(t *testing.T) {
	_, err := Load(writeConfig(t, minimal+`
env: prod
sms:
  aliyun:
    access_key_secret: sk
    sign_name: 签名
    template_login_code: SMS_0001
`))
	if err == nil {
		t.Fatal("生产环境缺 sms.aliyun.access_key_id 必须报错")
	}
	if !strings.Contains(err.Error(), "sms.aliyun.access_key_id") {
		t.Errorf("错误信息 %q 里应当出现 sms.aliyun.access_key_id", err)
	}
}

// TestLoadAllowsMissingAliyunOutsideProd 钉住阿里云短信凭据在非生产环境
// 允许留空——Load 本身不该因为缺它们而失败。本机开发、CI 跑 cmd/fp 二进制，
// 都不该被要求先备齐一份连假的都算不上的阿里云凭据才能启动。真正装配假
// 供应商并打 WARN 的逻辑在 cmd/fp/main.go 里，这里只钉住"Load 不替它做
// 这个决定"。
func TestLoadAllowsMissingAliyunOutsideProd(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatalf("Load() error = %v，非生产环境阿里云凭据允许留空", err)
	}
	if cfg.SMS.Aliyun.AccessKeyID != "" {
		t.Errorf("SMS.Aliyun.AccessKeyID = %q, want empty", cfg.SMS.Aliyun.AccessKeyID)
	}
}
