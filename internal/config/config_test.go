package config

import (
	"strings"
	"testing"
)

// TestParseReadsEveryField 逐字段断言，是 yaml tag 的护栏：漏写 tag 的
// 多词字段会被 yaml.v3 按"字段名整个小写"的默认规则映射错位，
// KnownFields(true) 会把它报成"未知键"，只有逐字段断言才抓得住。
func TestParseReadsEveryField(t *testing.T) {
	cfg, err := Parse(`
env: prod
log:
  level: debug
http:
  addr: ":18080"
grpc:
  addr: ":19090"
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
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	for _, tc := range []struct{ name, got, want string }{
		{"env", cfg.Env, "prod"},
		{"log.level", cfg.Log.Level, "debug"},
		{"http.addr", cfg.HTTP.Addr, ":18080"},
		{"grpc.addr", cfg.GRPC.Addr, ":19090"},
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

// TestParseEmptyTextUsesAllDefaults 钉住"系统配置表还没有任何版本"这个
// 首次启动的正常状态：空文本不是错误，全部用零值默认。
func TestParseEmptyTextUsesAllDefaults(t *testing.T) {
	cfg, err := Parse("")
	if err != nil {
		t.Fatalf(`Parse("") error = %v`, err)
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

// TestParseRejectsUnknownField 是从文件迁到数据库依然保留的能力：拼错的
// 键当场报错，不静默回落到默认值。
func TestParseRejectsUnknownField(t *testing.T) {
	_, err := Parse("log:\n  lvel: debug\n")
	if err == nil {
		t.Fatal("拼错的键必须报错，不能静默用默认值")
	}
	if !strings.Contains(err.Error(), "lvel") {
		t.Errorf("错误信息 %q 里应当出现拼错的那个键名 lvel", err)
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

func TestParseMissingAliyunRequiredInProd(t *testing.T) {
	_, err := Parse(`
env: prod
sms:
  aliyun:
    access_key_secret: sk
    sign_name: 签名
    template_login_code: SMS_0001
`)
	if err == nil {
		t.Fatal("生产环境缺 sms.aliyun.access_key_id 必须报错")
	}
	if !strings.Contains(err.Error(), "sms.aliyun.access_key_id") {
		t.Errorf("错误信息 %q 里应当出现 sms.aliyun.access_key_id", err)
	}
}

func TestParseAllowsMissingAliyunOutsideProd(t *testing.T) {
	cfg, err := Parse("")
	if err != nil {
		t.Fatalf("Parse() error = %v，非生产环境阿里云凭据允许留空", err)
	}
	if cfg.SMS.Aliyun.AccessKeyID != "" {
		t.Errorf("SMS.Aliyun.AccessKeyID = %q, want empty", cfg.SMS.Aliyun.AccessKeyID)
	}
}
