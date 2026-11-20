package notify

import (
	"strings"
	"testing"

	"github.com/basicfu/fp/internal/domain"
)

func TestFitTemplateValues(t *testing.T) {
	long := strings.Repeat("字", 40)
	got := fitTemplateValues(map[string]string{
		"thing1":            long,
		"character_string2": strings.Repeat("a", 40),
		"time3":             long,
		"things":            long, // 不是 thing 类型的字段名，不截
	})
	if n := len([]rune(got["thing1"])); n != 20 || !strings.HasSuffix(got["thing1"], "…") {
		t.Fatalf("thing1 = %q", got["thing1"])
	}
	if n := len(got["character_string2"]); n != len("…")+31 {
		t.Fatalf("character_string2 长度 = %d", n)
	}
	if got["time3"] != long || got["things"] != long {
		t.Fatal("其他字段不该被截断")
	}
}

func TestWechatMPTemplateValidation(t *testing.T) {
	base := domain.NotifyContent{Content: "{{thing1.DATA}}", Variables: []string{"thing1"}}
	if _, err := ValidateContent(domain.NotifyChannelWechatMP, domain.NotifyModeVendor, base); err != nil {
		t.Fatal(err)
	}
	withLink := base
	withLink.Link = "https://a.com/x"
	if _, err := ValidateContent(domain.NotifyChannelWechatMP, domain.NotifyModeVendor, withLink); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]domain.NotifyContent{
		"自定义模式":  base,
		"没有字段":   {Content: "x"},
		"链接不是网址": {Content: "x", Variables: []string{"thing1"}, Link: "javascript:alert(1)"},
	} {
		mode := domain.NotifyModeVendor
		if name == "自定义模式" {
			mode = domain.NotifyModeCustom
		}
		if _, err := ValidateContent(domain.NotifyChannelWechatMP, mode, c); err == nil {
			t.Errorf("%s 应被拒", name)
		}
	}
	// 其他渠道不支持 link。
	sms := domain.NotifyContent{Content: "x", Variables: []string{"code"}, Link: "https://a.com"}
	if _, err := ValidateContent(domain.NotifyChannelSMS, domain.NotifyModeVendor, sms); err == nil {
		t.Error("短信模板不该接受 link")
	}
}
