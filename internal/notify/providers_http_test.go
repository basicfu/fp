package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/domain"
)

// captured 是测试服务器收到的那一个请求。
type captured struct {
	method, path, rawQuery, contentType, signature string
	body                                           []byte
}

// capturingServer 起一个只记录请求、按给定状态码和响应体回应的服务器。
func capturingServer(t *testing.T, status int, respBody string) (*httptest.Server, *captured) {
	t.Helper()
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path, got.rawQuery = r.Method, r.URL.Path, r.URL.RawQuery
		got.contentType = r.Header.Get("Content-Type")
		got.signature = r.Header.Get("X-Fp-Signature")
		got.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func delivery(body string, c domain.NotifyContent) Delivery {
	return Delivery{Template: domain.NotifyTemplate{Content: c}, Rendered: Rendered{Body: body}}
}

func jsonOf(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("请求体不是 JSON: %v, %s", err, b)
	}
	return m
}

func TestTelegramSend(t *testing.T) {
	srv, got := capturingServer(t, 200, `{"ok":true}`)
	p := &telegramProvider{baseURL: srv.URL, token: "TOKEN", chatID: "-100123"}
	if err := p.Send(context.Background(), delivery("hello", domain.NotifyContent{})); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.method != "POST" || got.path != "/botTOKEN/sendMessage" {
		t.Fatalf("请求 = %s %s", got.method, got.path)
	}
	if m := jsonOf(t, got.body); m["chat_id"] != "-100123" || m["text"] != "hello" {
		t.Fatalf("body = %s", got.body)
	}
}

func TestTelegramReportsApiError(t *testing.T) {
	srv, _ := capturingServer(t, 400, `{"ok":false,"description":"chat not found"}`)
	p := &telegramProvider{baseURL: srv.URL, token: "TOKEN", chatID: "1"}
	err := p.Send(context.Background(), delivery("x", domain.NotifyContent{}))
	if err == nil || !strings.Contains(err.Error(), "chat not found") || strings.Contains(err.Error(), "TOKEN") {
		t.Fatalf("err = %v", err)
	}
}

// 【辨别力】token 就在 URL 路径里。网络层失败时 *url.Error 默认会把整条 URL 打进错误文本，
// 进而进 notify_log 和服务端日志。
func TestHTTPErrorsNeverContainCredentials(t *testing.T) {
	srv, _ := capturingServer(t, 200, `{}`)
	baseURL := srv.URL
	srv.Close() // 之后的请求必然在网络层失败

	cases := map[string]Provider{
		"telegram":     &telegramProvider{baseURL: baseURL, token: "SECRET-TOKEN", chatID: "1"},
		"wecom_bot":    &wecomProvider{baseURL: baseURL, key: "SECRET-TOKEN"},
		"dingtalk_bot": &dingtalkProvider{baseURL: baseURL, accessToken: "SECRET-TOKEN", now: time.Now},
		"webhook":      &webhookProvider{url: baseURL + "/hook?token=SECRET-TOKEN"},
	}
	for name, p := range cases {
		err := p.Send(context.Background(), delivery("x", domain.NotifyContent{Method: "GET"}))
		if err == nil {
			t.Errorf("%s: 服务器已关闭，应该失败", name)
			continue
		}
		if strings.Contains(err.Error(), "SECRET-TOKEN") {
			t.Errorf("%s: 错误里带了凭据: %v", name, err)
		}
	}
}

// 【辨别力】构造请求时的错误（URL 解析失败等）同样不能带出凭据：http.NewRequest 的错误里有整条 URL。
// token 或 URL 里有控制字符时，url.Parse 或 http.NewRequest 会失败。
func TestConstructionErrorsNeverContainCredentials(t *testing.T) {
	cases := map[string]Provider{
		// token 粘贴时多带了一个换行：URL 里有控制字符，url.Parse 失败。
		"telegram": &telegramProvider{baseURL: "https://api.telegram.org", token: "SECRET-TOKEN\n", chatID: "1"},
		// GET 模板的固定文本里有换行：拼出来的 URL 带控制字符。
		"webhook-get": &webhookProvider{url: "https://example.com/hook?token=SECRET-TOKEN"},
	}
	deliveries := map[string]Delivery{
		"telegram":    delivery("x", domain.NotifyContent{}),
		"webhook-get": delivery("a=1\nb=2", domain.NotifyContent{Method: "GET"}),
	}
	for name, p := range cases {
		err := p.Send(context.Background(), deliveries[name])
		if err == nil {
			t.Errorf("%s: 应在构造请求时失败", name)
			continue
		}
		if strings.Contains(err.Error(), "SECRET-TOKEN") {
			t.Errorf("%s: 错误里带了凭据: %v", name, err)
		}
		// 必须是构造阶段的失败：构造成功的话这条用例会变成一次真实的外网请求，上面的断言也就没在测它想测的东西。
		if !strings.Contains(err.Error(), "URL 不合法") {
			t.Errorf("%s: err = %q, want 构造请求失败（URL 不合法）", name, err.Error())
		}
	}
}

func TestWecomSend(t *testing.T) {
	srv, got := capturingServer(t, 200, `{"errcode":0,"errmsg":"ok"}`)
	p := &wecomProvider{baseURL: srv.URL + "/cgi-bin/webhook/send", key: "KEY 1"}
	d := delivery("告警", domain.NotifyContent{MentionedList: []string{"@all"}, MentionedMobileList: []string{"13800138000"}})
	if err := p.Send(context.Background(), d); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.path != "/cgi-bin/webhook/send" || got.rawQuery != "key=KEY+1" {
		t.Fatalf("请求 = %s ? %s（key 要做 query 转义）", got.path, got.rawQuery)
	}
	m := jsonOf(t, got.body)
	text := m["text"].(map[string]any)
	if m["msgtype"] != "text" || text["content"] != "告警" {
		t.Fatalf("body = %s", got.body)
	}
	if l := text["mentioned_list"].([]any); len(l) != 1 || l[0] != "@all" {
		t.Fatalf("mentioned_list = %v", l)
	}
	if l := text["mentioned_mobile_list"].([]any); len(l) != 1 || l[0] != "13800138000" {
		t.Fatalf("mentioned_mobile_list = %v", l)
	}
}

func TestWecomOmitsEmptyMentionsAndReportsErrcode(t *testing.T) {
	srv, got := capturingServer(t, 200, `{"errcode":93000,"errmsg":"invalid webhook url"}`)
	p := &wecomProvider{baseURL: srv.URL, key: "k"}
	err := p.Send(context.Background(), delivery("x", domain.NotifyContent{}))
	if err == nil || !strings.Contains(err.Error(), "93000") {
		t.Fatalf("errcode 非 0 应报错, err = %v", err)
	}
	text := jsonOf(t, got.body)["text"].(map[string]any)
	if _, ok := text["mentioned_list"]; ok {
		t.Fatalf("没配 @ 时不该带 mentioned_list: %v", text)
	}
}

func TestWecomRejectsNonJSONResponse(t *testing.T) {
	for _, body := range []string{"<html>blocked</html>", ""} {
		srv, _ := capturingServer(t, 200, body)
		p := &wecomProvider{baseURL: srv.URL, key: "SECRET-KEY"}
		err := p.Send(context.Background(), delivery("x", domain.NotifyContent{}))
		if err == nil {
			t.Errorf("body=%q: 应报错", body)
			continue
		}
		if !strings.Contains(err.Error(), "响应不是合法 JSON") {
			t.Errorf("body=%q: err = %v, want 响应不是合法 JSON", body, err)
		}
		if strings.Contains(err.Error(), "SECRET-KEY") {
			t.Errorf("body=%q: 错误里带了凭据: %v", body, err)
		}
	}
}

// 合法 JSON 却没有结果字段的 200（{}、null、WAF 拦截页的 JSON）不是成功：记成成功的话，
// 幂等键记下"已完成"，消息就静默丢了。企业微信与钉钉看 errcode，telegram 看 ok。
func TestIMProvidersRejectResponsesWithoutResultField(t *testing.T) {
	providers := map[string]struct {
		mk      func(baseURL string) Provider
		wantErr string
	}{
		"wecom_bot":    {func(u string) Provider { return &wecomProvider{baseURL: u, key: "k"} }, "响应缺少 errcode"},
		"dingtalk_bot": {func(u string) Provider { return &dingtalkProvider{baseURL: u, accessToken: "t", now: time.Now} }, "响应缺少 errcode"},
		"telegram":     {func(u string) Provider { return &telegramProvider{baseURL: u, token: "t", chatID: "1"} }, "HTTP 200"},
	}
	for name, p := range providers {
		for _, body := range []string{`{}`, `null`, `{"status":"blocked"}`} {
			srv, _ := capturingServer(t, 200, body)
			err := p.mk(srv.URL).Send(context.Background(), delivery("x", domain.NotifyContent{}))
			if err == nil || !strings.Contains(err.Error(), p.wantErr) {
				t.Errorf("%s: 回包 %s: err = %v, want 失败（%s）", name, body, err, p.wantErr)
			}
		}
	}
}

func TestDingtalkSignVector(t *testing.T) {
	const want = "7LVwF0dAF3/+MRRulbpE4y72Ogzykc6bS2nG4I99T4s="
	if got := dingtalkSign("SECtestsecret", 1700000000000); got != want {
		t.Fatalf("dingtalkSign = %q, want %q", got, want)
	}
}

func TestDingtalkSend(t *testing.T) {
	srv, got := capturingServer(t, 200, `{"errcode":0,"errmsg":"ok"}`)
	fixed := time.UnixMilli(1700000000000)
	p := &dingtalkProvider{baseURL: srv.URL, accessToken: "TOK", secret: "SECtestsecret", now: func() time.Time { return fixed }}
	d := delivery("告警", domain.NotifyContent{AtMobiles: []string{"13800138000"}, IsAtAll: true})
	if err := p.Send(context.Background(), d); err != nil {
		t.Fatalf("Send: %v", err)
	}
	const wantQuery = "access_token=TOK&sign=7LVwF0dAF3%2F%2BMRRulbpE4y72Ogzykc6bS2nG4I99T4s%3D&timestamp=1700000000000"
	if got.rawQuery != wantQuery {
		t.Fatalf("query = %s\nwant    %s", got.rawQuery, wantQuery)
	}
	m := jsonOf(t, got.body)
	if text, _ := m["text"].(map[string]any); m["msgtype"] != "text" || text["content"] != "告警" {
		t.Fatalf("body = %s", got.body)
	}
	at := m["at"].(map[string]any)
	if at["isAtAll"] != true || at["atMobiles"].([]any)[0] != "13800138000" {
		t.Fatalf("at = %v", at)
	}
}

func TestDingtalkWithoutSecretDoesNotSign(t *testing.T) {
	srv, got := capturingServer(t, 200, `{"errcode":0}`)
	p := &dingtalkProvider{baseURL: srv.URL, accessToken: "TOK", now: time.Now}
	if err := p.Send(context.Background(), delivery("x", domain.NotifyContent{})); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.rawQuery != "access_token=TOK" {
		t.Fatalf("query = %s", got.rawQuery)
	}
	// atMobiles 必须是 [] 而不是 null，钉钉对 null 会报参数错误。
	if !strings.Contains(string(got.body), `"atMobiles":[]`) {
		t.Fatalf("body = %s", got.body)
	}
}

func TestDingtalkRejectsNonJSONResponse(t *testing.T) {
	for _, body := range []string{"<html>blocked</html>", ""} {
		srv, _ := capturingServer(t, 200, body)
		p := &dingtalkProvider{baseURL: srv.URL, accessToken: "SECRET-TOKEN", now: time.Now}
		err := p.Send(context.Background(), delivery("x", domain.NotifyContent{}))
		if err == nil {
			t.Errorf("body=%q: 应报错", body)
			continue
		}
		if !strings.Contains(err.Error(), "响应不是合法 JSON") {
			t.Errorf("body=%q: err = %v, want 响应不是合法 JSON", body, err)
		}
		if strings.Contains(err.Error(), "SECRET-TOKEN") {
			t.Errorf("body=%q: 错误里带了凭据: %v", body, err)
		}
	}
}

func TestTelegramRejectsNonJSONResponse(t *testing.T) {
	for _, body := range []string{"<html>blocked</html>", ""} {
		srv, _ := capturingServer(t, 200, body)
		p := &telegramProvider{baseURL: srv.URL, token: "SECRET-TOKEN", chatID: "1"}
		err := p.Send(context.Background(), delivery("x", domain.NotifyContent{}))
		if err == nil {
			t.Errorf("body=%q: 应报错", body)
			continue
		}
		if !strings.Contains(err.Error(), "响应不是合法 JSON") {
			t.Errorf("body=%q: err = %v, want 响应不是合法 JSON", body, err)
		}
		if strings.Contains(err.Error(), "SECRET-TOKEN") {
			t.Errorf("body=%q: 错误里带了凭据: %v", body, err)
		}
	}
}

func TestWebhookPostSignsBody(t *testing.T) {
	srv, got := capturingServer(t, 200, ``)
	p := &webhookProvider{url: srv.URL + "/hook", secret: "whsecret"}
	d := delivery(`{"text":"hello"}`, domain.NotifyContent{Method: "POST", ContentType: "application/json"})
	if err := p.Send(context.Background(), d); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.method != "POST" || got.contentType != "application/json" || string(got.body) != `{"text":"hello"}` {
		t.Fatalf("请求 = %s %s %s", got.method, got.contentType, got.body)
	}
	const want = "sha256=605715228c3e335d03625cf04e66b83ca93d7b660e668421b8d940eb1f5e4e1a"
	if got.signature != want {
		t.Fatalf("signature = %s, want %s", got.signature, want)
	}
}

func TestWebhookGetAppendsQueryAndSignsIt(t *testing.T) {
	srv, got := capturingServer(t, 204, ``)
	p := &webhookProvider{url: srv.URL + "/hook?token=abc", secret: "whsecret"}
	d := delivery("title=a%20b&body=x", domain.NotifyContent{Method: "GET"})
	if err := p.Send(context.Background(), d); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.method != "GET" || got.rawQuery != "token=abc&title=a%20b&body=x" || len(got.body) != 0 {
		t.Fatalf("请求 = %s ?%s body=%q", got.method, got.rawQuery, got.body)
	}
	const want = "sha256=d6bbf2e8902d0fbd191f58af54a8ff6117e0bf0158ecbbc7a39eb4979bbf4471"
	if got.signature != want {
		t.Fatalf("GET 签名签的是渲染出的 query: signature = %s, want %s", got.signature, want)
	}
}

func TestWebhookWithoutSecretSendsNoSignature(t *testing.T) {
	srv, got := capturingServer(t, 200, ``)
	p := &webhookProvider{url: srv.URL}
	if err := p.Send(context.Background(), delivery("x", domain.NotifyContent{Method: "POST", ContentType: "text/plain"})); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.signature != "" || got.contentType != "text/plain" {
		t.Fatalf("signature = %q, contentType = %q", got.signature, got.contentType)
	}
}

func TestWebhookNon2xxIsAnError(t *testing.T) {
	srv, _ := capturingServer(t, 500, `boom`)
	p := &webhookProvider{url: srv.URL}
	if err := p.Send(context.Background(), delivery("x", domain.NotifyContent{})); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v", err)
	}
}

// 重定向不跟随：会把签名头与 query 里的密钥带去别的主机；3xx 也不是成功。
func TestWebhookDoesNotFollowRedirects(t *testing.T) {
	var hit bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	err := (&webhookProvider{url: srv.URL}).Send(context.Background(), delivery("x", domain.NotifyContent{}))
	if err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("err = %v, want HTTP 302 错误", err)
	}
	if hit {
		t.Fatal("跟随了重定向")
	}
}

func TestWebhookNewValidatesURL(t *testing.T) {
	// 带 # 的 url：GET 渲染出的 query 会被拼进片段，接收端拿不到参数却回 2xx，发送被记成成功。
	// 只有 # 没有片段的那种 u.Fragment 也是空串，所以要按字符拒绝。
	for _, bad := range []string{"", "not a url", "ftp://example.com/x", "http://", "/relative", "https://example.com/hook#frag", "https://example.com/hook#"} {
		if _, err := webhookSpec.New(Config{"url": bad}); err == nil {
			t.Errorf("url %q 应被拒绝", bad)
		}
	}
	if _, err := webhookSpec.New(Config{"url": "https://example.com/hook?x=1"}); err != nil {
		t.Errorf("合法 url 被拒绝: %v", err)
	}
}

func TestAppendQuery(t *testing.T) {
	cases := []struct{ base, query, want string }{
		{"http://h/p", "a=1", "http://h/p?a=1"},
		{"http://h/p?t=1", "a=1", "http://h/p?t=1&a=1"},
		{"http://h/p?", "a=1", "http://h/p?a=1"},
		{"http://h/p?t=1&", "a=1", "http://h/p?t=1&a=1"},
		{"http://h/p?t=1", "", "http://h/p?t=1"},
	}
	for _, c := range cases {
		if got := appendQuery(c.base, c.query); got != c.want {
			t.Errorf("appendQuery(%q, %q) = %q, want %q", c.base, c.query, got, c.want)
		}
	}
}

func TestLogProvider(t *testing.T) {
	if _, err := logSpec.New(Config{"channel": "push"}); err == nil {
		t.Fatal("未知渠道应被拒绝")
	}
	p, err := logSpec.New(Config{"channel": "sms"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Send(context.Background(), Delivery{To: "13800138000"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestDefaultRegistry(t *testing.T) {
	r := DefaultRegistry()
	dev := r.Types(false)
	var names []string
	for _, s := range dev {
		names = append(names, s.Type)
	}
	if got := strings.Join(names, ","); got != "aliyun,dingtalk_bot,log,smtp,telegram,webhook,wecom_bot" {
		t.Fatalf("开发环境的类型 = %s", got)
	}
	// prod 环境的类型必须不包含 log（DevOnly）
	prod := r.Types(true)
	var prodNames []string
	for _, s := range prod {
		prodNames = append(prodNames, s.Type)
		if s.DevOnly {
			t.Fatalf("prod 环境不该暴露 DevOnly 类型 %s", s.Type)
		}
	}
	if got := strings.Join(prodNames, ","); got != "aliyun,dingtalk_bot,smtp,telegram,webhook,wecom_bot" {
		t.Fatalf("prod 环境的类型 = %s, 期望 aliyun,dingtalk_bot,smtp,telegram,webhook,wecom_bot", got)
	}
	if err := r.Register(telegramSpec); err == nil {
		t.Fatal("重复登记应报错")
	}
	if s, ok := r.Spec("log"); !ok || s.ChannelOf(map[string]any{"channel": "email"}) != domain.NotifyChannelEmail {
		t.Fatal("log 类型的渠道应取自 config.channel")
	}
	if s, _ := r.Spec("telegram"); s.ChannelOf(nil) != domain.NotifyChannelTelegram {
		t.Fatal("telegram 类型的渠道是固定的")
	}
}
