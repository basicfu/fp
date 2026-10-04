package notify

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net"
	"net/mail"
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
		req, err := buildAliyunRequest(aliyunConfig{}, Delivery{ProviderTemplateID: "SMS_1", Params: params})
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
	d := Delivery{ProviderTemplateID: "SMS_1", Params: map[string]string{"z": "1", "a": "2", "m": "3"}}
	first, _ := buildAliyunRequest(aliyunConfig{}, d)
	want := `{"a":"2","m":"3","z":"1"}`
	for i := 0; i < 20; i++ {
		again, _ := buildAliyunRequest(aliyunConfig{}, d)
		if tea.StringValue(again.TemplateParam) != want || tea.StringValue(first.TemplateParam) != want {
			t.Fatalf("param = %q, want %q", tea.StringValue(again.TemplateParam), want)
		}
	}
}

// 阿里云客户端必须带着显式的连接/读取超时被创建出来：SDK 默认不设超时，而 Send 拿不到 ctx。
func TestNewAliyunSetsExplicitTimeouts(t *testing.T) {
	p, err := aliyunSpec.New(Config{"accessKeyId": "ak", "accessKeySecret": "sk", "signName": "示例签名"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client := p.(*aliyunProvider).client
	if client.ConnectTimeout == nil || client.ReadTimeout == nil {
		t.Fatal("客户端没有设置超时——吊死的接入点会永久占住一个 goroutine")
	}
	if tea.IntValue(client.ConnectTimeout) != aliyunConnectTimeoutMS || tea.IntValue(client.ReadTimeout) != aliyunReadTimeoutMS {
		t.Fatalf("超时 = %d / %d", tea.IntValue(client.ConnectTimeout), tea.IntValue(client.ReadTimeout))
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
	}
	for name, cfg := range cases {
		if _, err := smtpSpec.New(cfg); err == nil {
			t.Errorf("%s 应被拒绝", name)
		}
	}
	// 本机回环可以不加密带认证（开发用的本地 SMTP 捕获工具）。
	if _, err := smtpSpec.New(Config{"host": "127.0.0.1", "port": 1025, "from": "a@b.com", "tls": "none", "username": "u", "password": "p"}); err != nil {
		t.Errorf("回环主机应允许: %v", err)
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
func TestBuildEmailRejectsHeaderInjectionInRecipient(t *testing.T) {
	from := &mail.Address{Address: "noreply@example.com"}
	for _, bad := range []string{"a@b.com\r\nBcc: evil@example.com", "a@b.com\nBcc: evil@example.com", "not an address", ""} {
		if _, _, err := buildEmail(from, bad, "s", "text/plain", "x", time.Now()); err == nil {
			t.Errorf("收件人 %q 应被拒绝", bad)
		}
	}
}

type fakeMail struct{ from, to, data string }

// startFakeSMTP 起一个只处理一次投递的假 SMTP 服务器（明文、无认证）。
func startFakeSMTP(t *testing.T) (port int, got <-chan fakeMail) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
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
