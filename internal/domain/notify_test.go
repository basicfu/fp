package domain_test

import (
	"testing"

	"github.com/basicfu/fp/internal/domain"
)

func TestNotifyChannelRules(t *testing.T) {
	cases := []struct {
		ch                    domain.NotifyChannel
		valid, vendor, custom bool
		needsRecipient        bool
	}{
		{domain.NotifyChannelSMS, true, true, false, true},
		{domain.NotifyChannelEmail, true, true, true, true},
		{domain.NotifyChannelTelegram, true, false, true, false},
		{domain.NotifyChannelWecomBot, true, false, true, false},
		{domain.NotifyChannelDingtalkBot, true, false, true, false},
		{domain.NotifyChannelWebhook, true, false, true, false},
		{"push", false, false, false, false},
		{"", false, false, false, false},
	}
	for _, c := range cases {
		if got := c.ch.Valid(); got != c.valid {
			t.Errorf("%q.Valid() = %v, want %v", c.ch, got, c.valid)
		}
		if got := c.ch.AllowsMode(domain.NotifyModeVendor); got != c.vendor {
			t.Errorf("%q.AllowsMode(vendor) = %v, want %v", c.ch, got, c.vendor)
		}
		if got := c.ch.AllowsMode(domain.NotifyModeCustom); got != c.custom {
			t.Errorf("%q.AllowsMode(custom) = %v, want %v", c.ch, got, c.custom)
		}
		if got := c.ch.NeedsRecipient(); got != c.needsRecipient {
			t.Errorf("%q.NeedsRecipient() = %v, want %v", c.ch, got, c.needsRecipient)
		}
	}
}

var testSchema = []domain.Field{
	{Key: "host", Label: "主机", Type: domain.FieldTypeString, Required: true},
	{Key: "port", Label: "端口", Type: domain.FieldTypeInt, Required: true},
	{Key: "password", Label: "密码", Type: domain.FieldTypeSecret},
	{Key: "tls", Label: "TLS", Type: domain.FieldTypeString, Default: "starttls"},
	{Key: "debug", Label: "调试", Type: domain.FieldTypeBool},
}

func TestNormalizeConfig(t *testing.T) {
	got, err := domain.NormalizeConfig(testSchema, map[string]any{
		"host": "smtp.example.com", "port": float64(465), "password": "pw",
	})
	if err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	if got["port"] != int64(465) {
		t.Errorf("port = %#v, want int64(465)（JSON 数字要规整成整数）", got["port"])
	}
	if got["tls"] != "starttls" {
		t.Errorf("tls = %#v, want 缺省值 starttls", got["tls"])
	}
	if _, ok := got["debug"]; ok {
		t.Errorf("没填且没有默认值的可选项不该出现: %#v", got)
	}
}

func TestNormalizeConfigRejects(t *testing.T) {
	cases := []struct {
		name    string
		in      map[string]any
		wantKey string
	}{
		{"缺必填项", map[string]any{"port": float64(25)}, "host"},
		{"必填项为空串", map[string]any{"host": "", "port": float64(25)}, "host"},
		{"未知键", map[string]any{"host": "h", "port": float64(25), "typo": "x"}, "typo"},
		{"字符串字段给了数字", map[string]any{"host": float64(1), "port": float64(25)}, "host"},
		{"整数字段给了小数", map[string]any{"host": "h", "port": 25.5}, "port"},
		{"整数字段给了字符串", map[string]any{"host": "h", "port": "25"}, "port"},
		{"布尔字段给了字符串", map[string]any{"host": "h", "port": float64(25), "debug": "yes"}, "debug"},
	}
	for _, c := range cases {
		_, err := domain.NormalizeConfig(testSchema, c.in)
		if err == nil || err.Key != c.wantKey {
			t.Errorf("%s: err = %v, want 出错的键是 %q", c.name, err, c.wantKey)
		}
	}
}

func TestMaskAndMergeSecrets(t *testing.T) {
	stored := map[string]any{"host": "h", "password": "real-secret"}

	masked := domain.MaskSecrets(testSchema, stored)
	if masked["password"] != domain.SecretMask || masked["host"] != "h" {
		t.Fatalf("masked = %#v", masked)
	}
	if stored["password"] != "real-secret" {
		t.Fatal("MaskSecrets 不该改入参")
	}
	empty := domain.MaskSecrets(testSchema, map[string]any{"host": "h", "password": ""})
	if empty["password"] != "" {
		t.Errorf("空 secret 不该被遮成掩码（那会让界面显示'已设置'）: %#v", empty)
	}

	// 控制台把掩码原样传回来 → 保持原值；传了新值 → 用新值。
	kept := domain.MergeSecrets(testSchema, map[string]any{"host": "h2", "password": domain.SecretMask}, stored)
	if kept["password"] != "real-secret" || kept["host"] != "h2" {
		t.Errorf("kept = %#v", kept)
	}
	changed := domain.MergeSecrets(testSchema, map[string]any{"password": "new"}, stored)
	if changed["password"] != "new" {
		t.Errorf("changed = %#v", changed)
	}
	// 原来就没有值、传回掩码：不能把掩码当真值存进去。
	none := domain.MergeSecrets(testSchema, map[string]any{"password": domain.SecretMask}, map[string]any{})
	if _, ok := none["password"]; ok {
		t.Errorf("none = %#v，掩码不该被存成真值", none)
	}
}

// schema 之外的键一律不输出：某个 secret 字段日后改名或删掉，库里残留的旧键可能就是明文。
// secret 字段只要非空就遮，不管值是什么类型。
func TestMaskSecretsEmitsOnlySchemaKeys(t *testing.T) {
	stored := map[string]any{"host": "h", "password": float64(123456), "legacyPassword": "old-plaintext"}
	masked := domain.MaskSecrets(testSchema, stored)
	if _, ok := masked["legacyPassword"]; ok {
		t.Errorf("schema 之外的键不该输出: %#v", masked)
	}
	if masked["password"] != domain.SecretMask {
		t.Errorf("非字符串的 secret 也要遮成掩码: %#v", masked)
	}
	if masked["host"] != "h" || len(masked) != 2 {
		t.Errorf("masked = %#v, want 只有 host 与 password", masked)
	}
}
