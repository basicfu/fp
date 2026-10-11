package notify

import (
	"context"
	"strings"
	"testing"
	"time"
)

func zhutongAt(baseURL string) *zhutongProvider {
	return &zhutongProvider{endpoint: baseURL, username: "user", password: "pw", signature: "【示例签名】",
		now: func() time.Time { return time.Unix(1700000000, 0) }}
}

func TestZhutongSend(t *testing.T) {
	srv, got := capturingServer(t, 200, `{"code":200,"msg":"success","msgId":"1"}`)
	d := Delivery{To: "13800138000", ProviderTemplateID: "762179", Params: map[string]string{"code": "123456"}}
	if err := zhutongAt(srv.URL).Send(context.Background(), d); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.method != "POST" || got.path != "/v2/sendSmsTp" {
		t.Fatalf("请求 = %s %s", got.method, got.path)
	}
	m := jsonOf(t, got.body)
	// password 是 md5(md5("pw")+"1700000000")，明文密码不能出现在请求里。
	if m["username"] != "user" || m["password"] != "574f35be77656f452710acd413064db3" || m["tKey"] != float64(1700000000) ||
		m["signature"] != "【示例签名】" || m["tpId"] != "762179" {
		t.Fatalf("body = %s", got.body)
	}
	rec := m["records"].([]any)[0].(map[string]any)
	if rec["mobile"] != "13800138000" || rec["tpContent"].(map[string]any)["code"] != "123456" {
		t.Fatalf("records = %v", rec)
	}
}

// 没有变量的模板：tpContent 必须是 {} 而不是 null。
func TestZhutongEmptyParamsSendObject(t *testing.T) {
	srv, got := capturingServer(t, 200, `{"code":"200"}`)
	if err := zhutongAt(srv.URL).Send(context.Background(), Delivery{To: "13800138000", ProviderTemplateID: "1"}); err != nil {
		t.Fatalf("Send（code 是字符串 \"200\" 也算成功）: %v", err)
	}
	if !strings.Contains(string(got.body), `"tpContent":{}`) {
		t.Fatalf("body = %s", got.body)
	}
}

func TestZhutongRejectsBadDeliveryBeforeCalling(t *testing.T) {
	srv, got := capturingServer(t, 200, `{"code":200}`)
	for name, d := range map[string]Delivery{
		"缺模板 ID": {To: "13800138000"},
		"手机号不合法": {To: "13800138000,13900139000", ProviderTemplateID: "1"},
	} {
		if err := zhutongAt(srv.URL).Send(context.Background(), d); err == nil {
			t.Errorf("%s: 应报错", name)
		}
	}
	if got.method != "" {
		t.Fatal("校验不过不该发出请求")
	}
}

// 合法 JSON 却没有 code 的 200 不是成功，理由同 TestIMProvidersRejectResponsesWithoutResultField。
func TestZhutongReportsFailures(t *testing.T) {
	cases := map[string]string{
		`{"code":4004,"msg":"手机号格式不正确"}`: "code=4004 手机号格式不正确",
		`{}`:                   "响应缺少 code",
		`null`:                 "响应缺少 code",
		`{"code":null}`:        "响应缺少 code",
		`<html>blocked</html>`: "响应不是合法 JSON",
	}
	for body, want := range cases {
		srv, _ := capturingServer(t, 200, body)
		err := zhutongAt(srv.URL).Send(context.Background(), Delivery{To: "13800138000", ProviderTemplateID: "1"})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("回包 %s: err = %v, want 含 %q", body, err, want)
		}
	}
}

func TestZhutongNew(t *testing.T) {
	if _, err := zhutongSpec.New(Config{"username": "u", "password": "p"}); err == nil {
		t.Fatal("缺 signature 应报错")
	}
	p, err := zhutongSpec.New(Config{"username": "u", "password": "p", "signature": "【x】", "endpoint": "https://example.com/"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := p.(*zhutongProvider).endpoint; got != "https://example.com" {
		t.Fatalf("endpoint = %q, 末尾的 / 应去掉", got)
	}
	p, _ = zhutongSpec.New(Config{"username": "u", "password": "p", "signature": "【x】"})
	if got := p.(*zhutongProvider).endpoint; got != defaultZhutongEndpoint {
		t.Fatalf("endpoint = %q, want 默认接入点", got)
	}
}
