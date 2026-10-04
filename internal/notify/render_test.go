package notify

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/basicfu/fp/internal/domain"
)

func TestExtractPlaceholders(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"恭喜{name}登录成功", []string{"name"}},
		{"{a}{b}{a}", []string{"a", "b"}},
		// JSON 的花括号后面是引号，不是占位符；数字位置上的 {count} 是。
		{`{"text":"hi {name}","n":{count}}`, []string{"name", "count"}},
		// 供应商模板的常见写法：占位符名字都能取出来，供控制台预填 variables。
		{"验证码 ${code}，{{minutes}} 分钟内有效", []string{"code", "minutes"}},
		{"{}", nil},
		{"{ name }", nil},
		{"{1abc}", nil},
		{"{a-b}", nil},
	}
	for _, c := range cases {
		if got := ExtractPlaceholders(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ExtractPlaceholders(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSubstituteEscapesByTarget(t *testing.T) {
	params := map[string]string{"v": `a "b" <c> & d`}
	cases := []struct {
		name string
		esc  func(string) string
		want string
	}{
		{"原样", escapeNone, `[a "b" <c> & d]`},
		{"JSON", escapeJSON, `[a \"b\" <c> & d]`},
		{"HTML", escapeHTML, `[a &#34;b&#34; &lt;c&gt; &amp; d]`},
		{"URL query", escapeQuery, `[a%20%22b%22%20%3Cc%3E%20%26%20d]`},
	}
	for _, c := range cases {
		if got := Substitute("[{v}]", params, c.esc); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// 【辨别力】替换进去的值里带着 {other}，不能被再次当成占位符——否则调用方传来的
// 数据能把模板里的其他变量"读"出来。
func TestSubstituteIsSinglePass(t *testing.T) {
	got := Substitute("{a}", map[string]string{"a": "{b}", "b": "SECRET"}, escapeNone)
	if got != "{b}" {
		t.Fatalf("got %q, want {b}", got)
	}
}

func TestValidateParams(t *testing.T) {
	if err := ValidateParams([]string{"code"}, map[string]string{"code": "1"}); err != nil {
		t.Fatalf("一致时应通过: %v", err)
	}
	if err := ValidateParams(nil, nil); err != nil {
		t.Fatalf("没有变量时应通过: %v", err)
	}

	err := ValidateParams([]string{"a", "b"}, map[string]string{"a": "1", "c": "2", "d": "3"})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeNotifyParamsInvalid || !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want NOTIFY_PARAMS_INVALID / ErrInvalidArgument", err)
	}
	if !reflect.DeepEqual(de.Detail["missing"], []string{"b"}) ||
		!reflect.DeepEqual(de.Detail["unexpected"], []string{"c", "d"}) {
		t.Fatalf("detail = %v", de.Detail)
	}
}

func TestValidateContentAcceptsAndNormalizes(t *testing.T) {
	cases := []struct {
		name string
		ch   domain.NotifyChannel
		mode domain.NotifyMode
		in   domain.NotifyContent
		want domain.NotifyContent
	}{
		{"短信 vendor：content 只是原文，写法不解析",
			domain.NotifyChannelSMS, domain.NotifyModeVendor,
			domain.NotifyContent{Content: "您的验证码是${code}", Variables: []string{"code"}},
			domain.NotifyContent{Content: "您的验证码是${code}", Variables: []string{"code"}}},
		{"邮件 vendor",
			domain.NotifyChannelEmail, domain.NotifyModeVendor,
			domain.NotifyContent{Content: "原文", Variables: nil},
			domain.NotifyContent{Content: "原文", Variables: []string{}}},
		{"邮件 custom：contentType 缺省补 text/plain",
			domain.NotifyChannelEmail, domain.NotifyModeCustom,
			domain.NotifyContent{Subject: "你好 {name}", Content: "正文 {name}", Variables: []string{"name"}},
			domain.NotifyContent{Subject: "你好 {name}", Content: "正文 {name}", ContentType: "text/plain", Variables: []string{"name"}}},
		{"telegram",
			domain.NotifyChannelTelegram, domain.NotifyModeCustom,
			domain.NotifyContent{Content: "订单 {id} 已发货", Variables: []string{"id"}},
			domain.NotifyContent{Content: "订单 {id} 已发货", Variables: []string{"id"}}},
		{"企业微信机器人：@ 配置属于模板",
			domain.NotifyChannelWecomBot, domain.NotifyModeCustom,
			domain.NotifyContent{Content: "告警 {msg}", Variables: []string{"msg"}, MentionedList: []string{"@all"}},
			domain.NotifyContent{Content: "告警 {msg}", Variables: []string{"msg"}, MentionedList: []string{"@all"}}},
		{"钉钉机器人",
			domain.NotifyChannelDingtalkBot, domain.NotifyModeCustom,
			domain.NotifyContent{Content: "告警", Variables: []string{}, AtMobiles: []string{"13800138000"}, IsAtAll: false},
			domain.NotifyContent{Content: "告警", Variables: []string{}, AtMobiles: []string{"13800138000"}}},
		{"webhook：method 与 contentType 缺省补 POST / application/json",
			domain.NotifyChannelWebhook, domain.NotifyModeCustom,
			domain.NotifyContent{Content: `{"text":"hi {name}"}`, Variables: []string{"name"}},
			domain.NotifyContent{Content: `{"text":"hi {name}"}`, Method: "POST", ContentType: "application/json", Variables: []string{"name"}}},
		{"webhook GET：content 是 query 模板",
			domain.NotifyChannelWebhook, domain.NotifyModeCustom,
			domain.NotifyContent{Content: "title={title}", Method: "GET", Variables: []string{"title"}},
			domain.NotifyContent{Content: "title={title}", Method: "GET", Variables: []string{"title"}}},
		{"webhook POST 纯文本",
			domain.NotifyChannelWebhook, domain.NotifyModeCustom,
			domain.NotifyContent{Content: "hello {a}", Method: "POST", ContentType: "text/plain", Variables: []string{"a"}},
			domain.NotifyContent{Content: "hello {a}", Method: "POST", ContentType: "text/plain", Variables: []string{"a"}}},
	}
	for _, c := range cases {
		got, err := ValidateContent(c.ch, c.mode, c.in)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
}

func TestValidateContentRejects(t *testing.T) {
	cases := []struct {
		name    string
		ch      domain.NotifyChannel
		mode    domain.NotifyMode
		in      domain.NotifyContent
		wantMsg string
	}{
		{"未知渠道", "push", domain.NotifyModeCustom, domain.NotifyContent{Content: "x"}, "未知的渠道"},
		{"短信不能 custom", domain.NotifyChannelSMS, domain.NotifyModeCustom, domain.NotifyContent{Content: "x"}, "不支持"},
		{"telegram 不能 vendor", domain.NotifyChannelTelegram, domain.NotifyModeVendor, domain.NotifyContent{Content: "x"}, "不支持"},
		{"内容为空白", domain.NotifyChannelTelegram, domain.NotifyModeCustom, domain.NotifyContent{Content: "  "}, "不能为空"},
		{"变量名不合法", domain.NotifyChannelSMS, domain.NotifyModeVendor, domain.NotifyContent{Content: "x", Variables: []string{"1a"}}, "不合法"},
		{"变量重复", domain.NotifyChannelSMS, domain.NotifyModeVendor, domain.NotifyContent{Content: "x", Variables: []string{"a", "a"}}, "重复"},
		{"custom 用了没声明的变量", domain.NotifyChannelTelegram, domain.NotifyModeCustom, domain.NotifyContent{Content: "{a}{b}", Variables: []string{"a"}}, "没有声明"},
		{"custom 声明了没用的变量", domain.NotifyChannelTelegram, domain.NotifyModeCustom, domain.NotifyContent{Content: "{a}", Variables: []string{"a", "b"}}, "没有用到"},
		{"邮件主题里的变量也要声明", domain.NotifyChannelEmail, domain.NotifyModeCustom, domain.NotifyContent{Subject: "{s}", Content: "x"}, "没有声明"},
		{"vendor 带了 subject", domain.NotifyChannelEmail, domain.NotifyModeVendor, domain.NotifyContent{Content: "x", Subject: "s"}, "不支持 subject"},
		{"自定义邮件缺主题", domain.NotifyChannelEmail, domain.NotifyModeCustom, domain.NotifyContent{Content: "x"}, "主题"},
		{"邮件 contentType 不合法", domain.NotifyChannelEmail, domain.NotifyModeCustom, domain.NotifyContent{Subject: "s", Content: "x", ContentType: "text/xml"}, "contentType"},
		{"telegram 带了 @ 配置", domain.NotifyChannelTelegram, domain.NotifyModeCustom, domain.NotifyContent{Content: "x", AtMobiles: []string{"1"}}, "不支持 atMobiles"},
		{"企业微信带了钉钉的 @", domain.NotifyChannelWecomBot, domain.NotifyModeCustom, domain.NotifyContent{Content: "x", IsAtAll: true}, "不支持 isAtAll"},
		{"webhook method 非法", domain.NotifyChannelWebhook, domain.NotifyModeCustom, domain.NotifyContent{Content: "x", Method: "PUT"}, "GET 或 POST"},
		{"webhook GET 不能设 contentType", domain.NotifyChannelWebhook, domain.NotifyModeCustom, domain.NotifyContent{Content: "a=1", Method: "GET", ContentType: "text/plain"}, "GET"},
		{"webhook contentType 非法", domain.NotifyChannelWebhook, domain.NotifyModeCustom, domain.NotifyContent{Content: "x", ContentType: "text/html"}, "contentType"},
		{"webhook JSON 模板不是合法 JSON", domain.NotifyChannelWebhook, domain.NotifyModeCustom, domain.NotifyContent{Content: `{"a": {x}`, Variables: []string{"x"}}, "合法的 JSON"},
	}
	for _, c := range cases {
		_, err := ValidateContent(c.ch, c.mode, c.in)
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodeNotifyTemplateInvalid || !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: err = %v, want NOTIFY_TEMPLATE_INVALID", c.name, err)
			continue
		}
		if !strings.Contains(de.Msg, c.wantMsg) {
			t.Errorf("%s: msg = %q, want 包含 %q", c.name, de.Msg, c.wantMsg)
		}
	}
}

func TestRender(t *testing.T) {
	mk := func(ch domain.NotifyChannel, c domain.NotifyContent) domain.NotifyTemplate {
		return domain.NotifyTemplate{Channel: ch, Mode: domain.NotifyModeCustom, Content: c}
	}
	params := map[string]string{"name": `<b>"x"</b>`}

	html := Render(mk(domain.NotifyChannelEmail, domain.NotifyContent{Subject: "你好 {name}", Content: "<p>{name}</p>", ContentType: "text/html"}), params)
	if html.Body != "<p>&lt;b&gt;&#34;x&#34;&lt;/b&gt;</p>" {
		t.Errorf("HTML 邮件正文必须转义: %q", html.Body)
	}
	plain := Render(mk(domain.NotifyChannelEmail, domain.NotifyContent{Subject: "s", Content: "{name}", ContentType: "text/plain"}), params)
	if plain.Body != `<b>"x"</b>` {
		t.Errorf("纯文本邮件原样: %q", plain.Body)
	}

	// 变量值里的换行不能注入邮件头。
	inj := Render(mk(domain.NotifyChannelEmail, domain.NotifyContent{Subject: "你好 {name}", Content: "x"}), map[string]string{"name": "a\r\nBcc: evil@example.com"})
	if strings.ContainsAny(inj.Subject, "\r\n") {
		t.Errorf("主题里不能有换行: %q", inj.Subject)
	}

	js := Render(mk(domain.NotifyChannelWebhook, domain.NotifyContent{Content: `{"text":"hi {name}"}`, Method: "POST", ContentType: "application/json"}), params)
	if js.Body != `{"text":"hi <b>\"x\"</b>"}` {
		t.Errorf("JSON body: %q", js.Body)
	}
	get := Render(mk(domain.NotifyChannelWebhook, domain.NotifyContent{Content: "title={title}&body={body}", Method: "GET"}), map[string]string{"title": "a b", "body": "x"})
	if get.Body != "title=a%20b&body=x" {
		t.Errorf("GET query: %q", get.Body)
	}
	txt := Render(mk(domain.NotifyChannelWebhook, domain.NotifyContent{Content: "hello {name}", Method: "POST", ContentType: "text/plain"}), params)
	if txt.Body != `hello <b>"x"</b>` {
		t.Errorf("text/plain 原样: %q", txt.Body)
	}
	tg := Render(mk(domain.NotifyChannelTelegram, domain.NotifyContent{Content: "订单 {name}"}), params)
	if tg.Body != `订单 <b>"x"</b>` {
		t.Errorf("telegram 原样: %q", tg.Body)
	}
}
