package notify_test

import (
	"errors"
	"testing"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/notify"
)

func aliyunCfg() notify.AliyunConfig {
	return notify.AliyunConfig{
		AccessKeyID:     "ak",
		AccessKeySecret: "sk",
		SignName:        "示例签名",
		Templates: map[string]string{
			"login_code": "SMS_123456",
		},
	}
}

func TestBuildAliyunRequest(t *testing.T) {
	phone, sign, tmpl, param, err := notify.BuildAliyunRequest(aliyunCfg(), notify.Message{
		Channel:  notify.ChannelSMS,
		To:       "13800138000",
		Template: "login_code",
		Params:   map[string]string{"code": "123456"},
	})
	if err != nil {
		t.Fatalf("BuildAliyunRequest: %v", err)
	}
	if phone != "13800138000" {
		t.Errorf("phone = %q", phone)
	}
	if sign != "示例签名" {
		t.Errorf("sign = %q", sign)
	}
	if tmpl != "SMS_123456" {
		t.Errorf("templateCode = %q, want SMS_123456", tmpl)
	}
	if param != `{"code":"123456"}` {
		t.Errorf("templateParam = %q", param)
	}
}

// fp 内部模板 key 没有映射到阿里云模板 ID 时必须显式报错，
// 而不是发出一条模板为空的短信——这正是 3s 里模板 ID 硬编码要解决的问题。
func TestBuildAliyunRequestUnmappedTemplate(t *testing.T) {
	_, _, _, _, err := notify.BuildAliyunRequest(aliyunCfg(), notify.Message{
		Channel: notify.ChannelSMS, To: "13800138000", Template: "unknown_key",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestBuildAliyunRequestEmptyParams(t *testing.T) {
	_, _, _, param, err := notify.BuildAliyunRequest(aliyunCfg(), notify.Message{
		Channel: notify.ChannelSMS, To: "13800138000", Template: "login_code",
	})
	if err != nil {
		t.Fatalf("BuildAliyunRequest: %v", err)
	}
	if param != `{}` {
		t.Errorf("空参数应序列化为 {}, got %q", param)
	}
}

// 模板参数的键顺序必须稳定，否则同一条消息每次生成的请求体都不同，无法排障。
func TestBuildAliyunRequestParamOrderIsStable(t *testing.T) {
	msg := notify.Message{
		Channel: notify.ChannelSMS, To: "13800138000", Template: "login_code",
		Params: map[string]string{"z": "1", "a": "2", "m": "3"},
	}
	_, _, _, first, err := notify.BuildAliyunRequest(aliyunCfg(), msg)
	if err != nil {
		t.Fatalf("BuildAliyunRequest: %v", err)
	}
	for i := 0; i < 20; i++ {
		_, _, _, again, err := notify.BuildAliyunRequest(aliyunCfg(), msg)
		if err != nil {
			t.Fatalf("BuildAliyunRequest: %v", err)
		}
		if again != first {
			t.Fatalf("参数序列化不稳定: %q vs %q", first, again)
		}
	}
	if first != `{"a":"2","m":"3","z":"1"}` {
		t.Fatalf("应按键名排序, got %q", first)
	}
}

func TestNewAliyunSMSRequiresCredentials(t *testing.T) {
	cfg := aliyunCfg()
	cfg.AccessKeyID = ""
	// 缺凭据是**启动配置错误**，不是调用方传错参数——这个错误永远到不了
	// 终端用户，所以归 INTERNAL 而不是 INVALID_ARGUMENT。
	var de *domain.Error
	if _, err := notify.NewAliyunSMS(cfg); !errors.As(err, &de) || de.Code != domain.CodeInternal {
		t.Fatalf("err = %v, want CodeInternal", err)
	}
}

func TestAliyunConfigSchemaCoversCredentials(t *testing.T) {
	p, err := notify.NewAliyunSMS(aliyunCfg())
	if err != nil {
		t.Fatalf("NewAliyunSMS: %v", err)
	}
	if p.Channel() != notify.ChannelSMS {
		t.Fatalf("Channel = %q", p.Channel())
	}

	byKey := map[string]domain.Field{}
	for _, f := range p.ConfigSchema() {
		byKey[f.Key] = f
	}
	for _, key := range []string{"accessKeyId", "accessKeySecret", "signName"} {
		f, ok := byKey[key]
		if !ok {
			t.Fatalf("ConfigSchema 缺少 %q", key)
		}
		if !f.Required {
			t.Errorf("%q 应为必填", key)
		}
	}
	// 密钥必须标为 secret，管理 UI 才会脱敏显示、落库才会加密。
	if byKey["accessKeySecret"].Type != domain.FieldTypeSecret {
		t.Errorf("accessKeySecret 类型 = %q, want secret", byKey["accessKeySecret"].Type)
	}
}
