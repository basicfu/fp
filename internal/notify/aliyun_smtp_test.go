package notify

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/alibabacloud-go/tea/tea"

	"github.com/basicfu/fp/internal/domain"
)

func TestBuildAliyunRequest(t *testing.T) {
	cfg := aliyunConfig{signName: "示例签名"}
	req, err := buildAliyunRequest(cfg, Delivery{To: "13800138000", ProviderTemplateID: "SMS_123456", Params: map[string]string{"code": "123456"}})
	if err != nil {
		t.Fatalf("buildAliyunRequest: %v", err)
	}
	if tea.StringValue(req.PhoneNumbers) != "13800138000" || tea.StringValue(req.SignName) != "示例签名" ||
		tea.StringValue(req.TemplateCode) != "SMS_123456" || tea.StringValue(req.TemplateParam) != `{"code":"123456"}` {
		t.Fatalf("req = %+v", req)
	}
}

// 供应商侧模板 ID 缺失必须显式报错，而不是发出一条模板为空的短信。
func TestBuildAliyunRequestRequiresTemplateID(t *testing.T) {
	if _, err := buildAliyunRequest(aliyunConfig{}, Delivery{To: "13800138000"}); err == nil {
		t.Fatal("缺模板 ID 应报错")
	}
}

func TestBuildAliyunRequestEmptyParams(t *testing.T) {
	for name, params := range map[string]map[string]string{"nil": nil, "空": {}} {
		req, err := buildAliyunRequest(aliyunConfig{}, Delivery{To: "13800138000", ProviderTemplateID: "SMS_1", Params: params})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := tea.StringValue(req.TemplateParam); got != "{}" {
			t.Errorf("%s: 空参数应序列化为 {}, got %q", name, got)
		}
	}
}

// 模板参数的键顺序必须稳定，否则同一条消息每次生成的请求体都不同，无法排障。
func TestBuildAliyunRequestParamOrderIsStable(t *testing.T) {
	d := Delivery{To: "13800138000", ProviderTemplateID: "SMS_1", Params: map[string]string{"z": "1", "a": "2", "m": "3"}}
	first, _ := buildAliyunRequest(aliyunConfig{}, d)
	want := `{"a":"2","m":"3","z":"1"}`
	for i := 0; i < 20; i++ {
		again, _ := buildAliyunRequest(aliyunConfig{}, d)
		if tea.StringValue(again.TemplateParam) != want || tea.StringValue(first.TemplateParam) != want {
			t.Fatalf("param = %q, want %q", tea.StringValue(again.TemplateParam), want)
		}
	}
}

// 阿里云客户端必须带着显式的超时被创建出来：SDK 默认不设超时，而 Send 拿不到 ctx。
// 两个值的单位不同：SDK 默认的拨号器按秒读 ConnectTimeout，ReadTimeout 按毫秒设成 http.Client.Timeout。
// 按错单位填，连接超时就成了几千秒，或者短到连不上。
func TestNewAliyunSetsExplicitTimeouts(t *testing.T) {
	p, err := aliyunSpec.New(Config{"accessKeyId": "ak", "accessKeySecret": "sk", "signName": "示例签名"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client := p.(*aliyunProvider).client
	if client.ConnectTimeout == nil || client.ReadTimeout == nil {
		t.Fatal("客户端没有设置超时——吊死的接入点会永久占住一个 goroutine")
	}
	connect := time.Duration(tea.IntValue(client.ConnectTimeout)) * time.Second
	total := time.Duration(tea.IntValue(client.ReadTimeout)) * time.Millisecond
	if connect != 5*time.Second || total != 10*time.Second {
		t.Fatalf("连接超时 = %v，整次调用上限 = %v, want 5s / 10s", connect, total)
	}
}

func TestNewAliyunRejectsIncompleteConfig(t *testing.T) {
	if _, err := aliyunSpec.New(Config{"accessKeyId": "ak", "accessKeySecret": "sk"}); err == nil {
		t.Fatal("缺 signName 应被拒绝")
	}
}

func TestNewSMTPValidatesConfig(t *testing.T) {
	ok := Config{"host": "smtp.example.com", "port": float64(465), "from": "noreply@example.com", "tls": "ssl"}
	if _, err := smtpSpec.New(ok); err != nil {
		t.Fatalf("合法配置被拒绝: %v", err)
	}
	cases := map[string]Config{
		"缺 host":    {"port": 465, "from": "a@b.com"},
		"端口越界":      {"host": "h", "port": 70000, "from": "a@b.com"},
		"from 不是邮箱": {"host": "h", "port": 25, "from": "not an address"},
		"tls 取值非法":  {"host": "h", "port": 25, "from": "a@b.com", "tls": "tls1.3"},
		"不加密却配了密码（非回环主机）": {"host": "smtp.example.com", "port": 25, "from": "a@b.com", "tls": "none", "username": "u", "password": "p"},
		// net/smtp 的 PlainAuth 只认这三个名字是本机，别的回环写法保存时放过、第一次发送才报错。
		"不加密却配了密码（127.0.0.2）":        {"host": "127.0.0.2", "port": 25, "from": "a@b.com", "tls": "none", "username": "u", "password": "p"},
		"不加密却配了密码（::ffff:127.0.0.1）": {"host": "::ffff:127.0.0.1", "port": 25, "from": "a@b.com", "tls": "none", "username": "u", "password": "p"},
	}
	for name, cfg := range cases {
		if _, err := smtpSpec.New(cfg); err == nil {
			t.Errorf("%s 应被拒绝", name)
		}
	}
	// 本机回环可以不加密带认证（开发用的本地 SMTP 捕获工具）。
	for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
		if _, err := smtpSpec.New(Config{"host": host, "port": 1025, "from": "a@b.com", "tls": "none", "username": "u", "password": "p"}); err != nil {
			t.Errorf("回环主机 %s 应允许: %v", host, err)
		}
	}
}

func TestBuildEmail(t *testing.T) {
	from := &mail.Address{Name: "fp 通知", Address: "noreply@example.com"}
	to, raw, err := buildEmail(from, "Alice <alice@example.com>", "你好，Alice", "text/html", "<p>"+strings.Repeat("长", 100)+"</p>", time.Unix(1700000000, 0).UTC())
	if err != nil {
		t.Fatalf("buildEmail: %v", err)
	}
	if to != "alice@example.com" {
		t.Fatalf("收件人只取地址部分: %q", to)
	}
	m, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("报文不合法: %v", err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil || subject != "你好，Alice" {
		t.Fatalf("Subject = %q, err = %v", subject, err)
	}
	if ct := m.Header.Get("Content-Type"); ct != "text/html; charset=UTF-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if addr, _ := mail.ParseAddress(m.Header.Get("To")); addr == nil || addr.Address != "alice@example.com" {
		t.Fatalf("To = %q", m.Header.Get("To"))
	}
	// Gmail 等收件方要求有效的 Message-ID；它只带随机数与发件人的域名。
	if id := m.Header.Get("Message-ID"); !regexp.MustCompile(`^<[0-9a-f]{32}@example\.com>$`).MatchString(id) {
		t.Fatalf("Message-ID = %q", id)
	}
	if _, again, _ := buildEmail(from, "alice@example.com", "s", "text/plain", "x", time.Now()); strings.Contains(string(again), m.Header.Get("Message-ID")) {
		t.Fatal("两封邮件的 Message-ID 不能相同")
	}
	body, _ := io.ReadAll(m.Body)
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(string(body), "\r\n", ""))
	if err != nil || string(decoded) != "<p>"+strings.Repeat("长", 100)+"</p>" {
		t.Fatalf("正文往返失败: err = %v", err)
	}
	for _, line := range strings.Split(string(body), "\r\n") {
		if len(line) > 76 {
			t.Fatalf("base64 行超过 76 字符: %d", len(line))
		}
	}
}

// 【辨别力】收件人来自业务方：换行符不能注入新的邮件头（比如偷偷加一个 Bcc）。
// 带引号的本地部分会被 ParseAddress 去掉引号、原样进 RCPT 命令：空格与尖括号能往信封里塞 ESMTP 参数，
// 多出来的 @ 能让收件域与业务方按后缀校验过的那个不一样。
func TestBuildEmailRejectsHeaderInjectionInRecipient(t *testing.T) {
	from := &mail.Address{Address: "noreply@example.com"}
	for _, bad := range []string{
		"a@b.com\r\nBcc: evil@example.com", "a@b.com\nBcc: evil@example.com", "not an address", "",
		`"x> NOTIFY=NEVER"@example.com`,
		`"a@evil.com> ORCPT=rfc822;x"@corp.com`,
		`"a@evil.com"@corp.com`,
	} {
		if _, _, err := buildEmail(from, bad, "s", "text/plain", "x", time.Now()); err == nil {
			t.Errorf("收件人 %q 应被拒绝", bad)
		}
	}
	// 引号里没有特殊字符的本地部分照常接受，信封里用去掉引号后的地址。
	to, _, err := buildEmail(from, `"john.doe"@example.com`, "s", "text/plain", "x", time.Now())
	if err != nil || to != "john.doe@example.com" {
		t.Errorf(`"john.doe"@example.com: to = %q, err = %v`, to, err)
	}
}

type fakeMail struct{ from, to, data string }

// smtpFault 让假服务器在某个命令上出岔子；零值表示一切正常。
type smtpFault struct {
	// dropOnQuit：收到 QUIT 直接断开、不回复。
	dropOnQuit bool
	// hangOn 非空时，收到以它开头的命令（大写，如 "QUIT"、"MAIL FROM:"）后既不回复也不断开，
	// 直到测试结束；开始挂起时关闭 hung（非 nil 时）。
	hangOn string
	hung   chan struct{}
}

// startFakeSMTP 起一个只处理一次投递的假 SMTP 服务器（明文、无认证）。fault 最多给一个。
func startFakeSMTP(t *testing.T, fault ...smtpFault) (port int, got <-chan fakeMail) {
	t.Helper()
	var f smtpFault
	if len(fault) > 0 {
		f = fault[0]
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听: %v", err)
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		close(done)
		_ = ln.Close()
	})
	out := make(chan fakeMail, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		reply := func(s string) { _, _ = fmt.Fprintf(conn, "%s\r\n", s) }
		between := func(s string) string {
			i, j := strings.Index(s, "<"), strings.LastIndex(s, ">")
			if i < 0 || j < i {
				return ""
			}
			return s[i+1 : j]
		}
		var m fakeMail
		reply("220 fake ESMTP")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			if f.hangOn != "" && strings.HasPrefix(cmd, f.hangOn) {
				if f.hung != nil {
					close(f.hung)
				}
				<-done
				return
			}
			switch {
			case strings.HasPrefix(cmd, "MAIL FROM:"):
				m.from = between(line)
				reply("250 ok")
			case strings.HasPrefix(cmd, "RCPT TO:"):
				m.to = between(line)
				reply("250 ok")
			case cmd == "DATA":
				reply("354 go ahead")
				var sb strings.Builder
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if l == ".\r\n" {
						break
					}
					sb.WriteString(l)
				}
				m.data = sb.String()
				reply("250 queued")
				out <- m
			case cmd == "QUIT":
				if f.dropOnQuit {
					return
				}
				reply("221 bye")
				return
			default: // EHLO / HELO 等
				reply("250 fake")
			}
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, out
}

func TestSMTPSendDeliversOverPlainConnection(t *testing.T) {
	port, got := startFakeSMTP(t)
	p, err := smtpSpec.New(Config{"host": "127.0.0.1", "port": port, "from": "noreply@example.com", "tls": "none"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := Delivery{
		To:       "alice@example.com",
		Template: domain.NotifyTemplate{Content: domain.NotifyContent{ContentType: "text/plain"}},
		Rendered: Rendered{Subject: "你好", Body: "正文"},
	}
	if err := p.Send(context.Background(), d); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case m := <-got:
		if m.from != "noreply@example.com" || m.to != "alice@example.com" {
			t.Fatalf("信封 = %+v", m)
		}
		msg, err := mail.ReadMessage(strings.NewReader(m.data))
		if err != nil {
			t.Fatalf("收到的报文不合法: %v", err)
		}
		if msg.Header.Get("Content-Transfer-Encoding") != "base64" {
			t.Fatalf("headers = %v", msg.Header)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("假服务器没有收到邮件")
	}
}

func TestSMTPSendReportsConnectionFailure(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close() // 端口已空，连接必然被拒
	p, _ := smtpSpec.New(Config{"host": "127.0.0.1", "port": port, "from": "a@example.com", "tls": "none"})
	d := Delivery{To: "b@example.com", Rendered: Rendered{Subject: "s", Body: "x"}}
	if err := p.Send(context.Background(), d); err == nil {
		t.Fatal("连不上应报错")
	}
}

// 网络层失败时 SDK 返回的 *url.Error 带着整条请求 URL：手机号、模板变量（验证码）、AccessKeyId 都在 query 里，
// 而错误文本会进 notify_log 与服务端日志。
func TestAliyunSendDoesNotExposePhoneOrCredentialsInError(t *testing.T) {
	// 1 号端口上没人监听，连接立刻被拒。
	p, err := aliyunSpec.New(Config{
		"accessKeyId":     "test_ak_123456789",
		"accessKeySecret": "test_sk_secret_value",
		"signName":        "示例签名",
		"endpoint":        "127.0.0.1:1",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := Delivery{
		To:                 "13800138000",
		ProviderTemplateID: "SMS_1",
		Params:             map[string]string{"code": "654321"},
	}
	err = p.Send(context.Background(), d)
	if err == nil {
		t.Fatal("向关闭的端口发送应报错")
	}
	errStr := err.Error()
	for _, secret := range []string{"13800138000", "654321", "test_ak_123456789", "test_sk_secret_value"} {
		if strings.Contains(errStr, secret) {
			t.Errorf("错误信息不应包含 %q，实际: %s", secret, errStr)
		}
	}
}

// aliyunAgainst 让阿里云客户端改走明文 HTTP，打到按给定状态码与响应体回应的本地服务器上。
func aliyunAgainst(t *testing.T, status int, body string) Provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	p, err := aliyunSpec.New(Config{
		"accessKeyId": "test_ak_123", "accessKeySecret": "test_sk_456", "signName": "示例签名",
		"endpoint": strings.TrimPrefix(srv.URL, "http://"),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.(*aliyunProvider).client.Protocol = tea.String("http")
	return p
}

// sendAliyunExpectingSafeError 发一条带验证码的短信，要求失败，且错误里没有请求里的敏感值：
// 这段文本会进 notify_log（控制台可见）与服务端日志。
func sendAliyunExpectingSafeError(t *testing.T, p Provider) string {
	t.Helper()
	err := p.Send(context.Background(), Delivery{To: "13800138000", ProviderTemplateID: "SMS_1", Params: map[string]string{"code": "654321"}})
	if err == nil {
		t.Fatal("应报错")
	}
	for _, secret := range []string{"13800138000", "654321", "test_ak_123", "test_sk_456"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("错误里带了 %q: %s", secret, err)
		}
	}
	return err.Error()
}

// 签名不符（AK Secret 填错，最常见的配置错误）时，阿里云在 4xx 回包的 Message 里回显完整的待签串：
// AccessKeyId、手机号、验证码都在里面。错误里只能留状态码、错误码与请求 ID。
func TestAliyunSendDropsEchoedRequestFrom4xx(t *testing.T) {
	p := aliyunAgainst(t, http.StatusBadRequest, `{"Code":"SignatureDoesNotMatch",`+
		`"Message":"Specified signature is not matched with our calculation. server string to sign is:POST&%2F&AccessKeyId%3Dtest_ak_123%26PhoneNumbers%3D13800138000%26TemplateParam%3D%257B%2522code%2522%253A%2522654321%2522%257D",`+
		`"RequestId":"RID-1","Recommend":"https://api.aliyun.com/troubleshoot?q=SignatureDoesNotMatch"}`)
	msg := sendAliyunExpectingSafeError(t, p)
	for _, want := range []string{"400", "SignatureDoesNotMatch", "RID-1"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误里应有 %q，便于排障: %s", want, msg)
		}
	}
}

// 200 但业务失败：Message 是供应商写的说明，不保证不回显请求内容，只留错误码与请求 ID。
func TestAliyunSendReportsBusinessFailureWithoutMessage(t *testing.T) {
	p := aliyunAgainst(t, http.StatusOK, `{"Code":"isv.BUSINESS_LIMIT_CONTROL","Message":"触发流控 13800138000 654321","RequestId":"RID-2"}`)
	msg := sendAliyunExpectingSafeError(t, p)
	if !strings.Contains(msg, "isv.BUSINESS_LIMIT_CONTROL") || !strings.Contains(msg, "RID-2") {
		t.Errorf("错误里应有错误码与请求 ID: %s", msg)
	}
}

// 响应体不是 JSON 对象时 SDK 会直接 panic，panic 的值里有响应体原文；gRPC 不替 handler 兜底，
// 一条坏响应就能带走整个进程。必须变成一个普通的发送失败，且不带响应内容。
func TestAliyunSendRejectsUnparseableResponses(t *testing.T) {
	cases := []struct {
		status int
		body   string
	}{
		{http.StatusOK, ""},
		{http.StatusOK, "null"},
		{http.StatusOK, "<html>13800138000</html>"},
		{http.StatusBadRequest, `["13800138000","654321"]`},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%d %s", c.status, c.body), func(t *testing.T) {
			sendAliyunExpectingSafeError(t, aliyunAgainst(t, c.status, c.body))
		})
	}
}

// PhoneNumbers 是逗号分隔的列表：不校验的话，业务方传来的一个"收件人"就能扇出成多条短信。
// 拒绝时也不回显号码，错误文本会进日志。
func TestBuildAliyunRequestValidatesPhoneNumber(t *testing.T) {
	cases := map[string]bool{
		"13800138000":             true,  // 合法
		"+8613800138000":          true,  // 合法：带 + 前缀
		"12345":                   true,  // 合法：最短 5 位
		"123456789012345678901":   false, // 过长：21 位
		"1234":                    false, // 过短：4 位
		"13800138000,13900139000": false, // 含逗号：非法
		"13800138000 ":            false, // 末尾空格
		" 13800138000":            false, // 开头空格
		"1380013800a":             false, // 含字母
		"":                        false, // 空字符串
	}
	for phone, shouldPass := range cases {
		_, err := buildAliyunRequest(aliyunConfig{}, Delivery{
			To:                 phone,
			ProviderTemplateID: "SMS_1",
		})
		if shouldPass && err != nil {
			t.Errorf("合法号码 %q 被拒绝: %v", phone, err)
		} else if !shouldPass && err == nil {
			t.Errorf("非法号码 %q 不应被接受", phone)
		} else if !shouldPass && err != nil && phone != "" && strings.Contains(err.Error(), phone) {
			t.Errorf("错误信息不应回显号码 %q，实际: %v", phone, err)
		}
	}
}

// 服务器已经接受了邮件，QUIT 失败也算发送成功：报成失败会触发降级，同一封邮件就发了两次。
func TestSMTPSendSucceedsEvenIfQuitFails(t *testing.T) {
	port, got := startFakeSMTP(t, smtpFault{dropOnQuit: true})
	p, err := smtpSpec.New(Config{"host": "127.0.0.1", "port": port, "from": "noreply@example.com", "tls": "none"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := Delivery{
		To:       "alice@example.com",
		Template: domain.NotifyTemplate{Content: domain.NotifyContent{ContentType: "text/plain"}},
		Rendered: Rendered{Subject: "test", Body: "body"},
	}
	if err := p.Send(context.Background(), d); err != nil {
		t.Fatalf("Send 应成功（QUIT 失败不算）: %v", err)
	}
	select {
	case m := <-got:
		if m.to != "alice@example.com" {
			t.Fatalf("收件人: %q", m.to)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("假服务器没有收到邮件")
	}
}

// vendor 模板从不由 fp 渲染，SMTP 拿到它就没有内容可发：必须报错，不能发出一封空邮件还记成成功。
func TestSMTPRefusesVendorTemplates(t *testing.T) {
	// 1 号端口上没人监听：守卫生效时根本走不到连接这一步，错误是守卫给的，不是连接失败。
	p, err := smtpSpec.New(Config{
		"host": "127.0.0.1", "port": 1,
		"from": "noreply@example.com", "tls": "none",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := Delivery{
		To:       "alice@example.com",
		Template: domain.NotifyTemplate{Mode: domain.NotifyModeVendor},
		Rendered: Rendered{},
	}
	err = p.Send(context.Background(), d)
	if err == nil {
		t.Fatal("vendor 模板应被拒绝")
	}
	if errStr := err.Error(); !strings.Contains(errStr, "供应商模板") {
		t.Errorf("错误应提及供应商模板，实际: %s", errStr)
	}
}

// plainSMTP 是连本机假服务器的 SMTP 供应商（明文、无认证）。
func plainSMTP(t *testing.T, port int) Provider {
	t.Helper()
	p, err := smtpSpec.New(Config{"host": "127.0.0.1", "port": port, "from": "noreply@example.com", "tls": "none"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func textEmail(to string) Delivery {
	return Delivery{
		To:       to,
		Template: domain.NotifyTemplate{Content: domain.NotifyContent{ContentType: "text/plain"}},
		Rendered: Rendered{Subject: "s", Body: "b"},
	}
}

// 邮件已被接受之后 QUIT 卡住：只能再等一小会儿就按成功返回。拖到 30 秒的截止时间的话，
// 调用方（SDK 每次 RPC 也是 30 秒）会先超时，换新键重发就成了重复投递。
func TestSMTPSendReturnsSoonWhenQuitHangsAfterAcceptance(t *testing.T) {
	port, got := startFakeSMTP(t, smtpFault{hangOn: "QUIT"})
	p := plainSMTP(t, port)
	errc := make(chan error, 1)
	go func() { errc <- p.Send(context.Background(), textEmail("alice@example.com")) }()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Send: %v（邮件已被接受，QUIT 卡住不算失败）", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("QUIT 卡住时 Send 没有在 5 秒内返回")
	}
	select {
	case <-got:
	default:
		t.Fatal("假服务器没有收到邮件")
	}
}

// 连上服务器之后调用方取消：Send 要尽快返回（Provider 的约定），不能等到 30 秒的截止时间。
func TestSMTPSendReturnsSoonWhenCancelledAfterConnecting(t *testing.T) {
	hung := make(chan struct{})
	port, _ := startFakeSMTP(t, smtpFault{hangOn: "MAIL FROM:", hung: hung})
	p := plainSMTP(t, port)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- p.Send(ctx, textEmail("alice@example.com")) }()
	select {
	case <-hung:
	case <-time.After(5 * time.Second):
		t.Fatal("Send 没有走到 MAIL FROM")
	}

	cancel()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("取消之后邮件并没有被接受，应报错")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("取消之后 Send 没有在 2 秒内返回")
	}
}
