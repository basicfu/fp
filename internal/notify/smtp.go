package notify

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/basicfu/fp/internal/domain"
)

const (
	smtpTimeout = 30 * time.Second
	// 邮件被接受之后只给 QUIT 留这么久：拖到调用方超时，它换个键重发就重复投递了。
	smtpQuitTimeout = 2 * time.Second
)

var smtpSpec = TypeSpec{
	Type:    "smtp",
	Channel: domain.NotifyChannelEmail,
	ConfigSchema: []domain.Field{
		{Key: "host", Label: "SMTP 主机", Type: domain.FieldTypeString, Required: true},
		{Key: "port", Label: "端口", Type: domain.FieldTypeInt, Required: true, Default: 465},
		{Key: "username", Label: "用户名", Type: domain.FieldTypeString},
		{Key: "password", Label: "密码", Type: domain.FieldTypeSecret},
		{Key: "from", Label: "发件人", Type: domain.FieldTypeString, Required: true, Help: "例如 noreply@example.com"},
		{Key: "tls", Label: "加密方式", Type: domain.FieldTypeString, Default: "ssl", Help: "ssl（465 端口，隐式 TLS）/ starttls（587 端口）/ none"},
	},
	New: newSMTP,
}

type smtpProvider struct {
	host     string
	port     int
	username string
	password string
	from     *mail.Address
	tlsMode  string
}

func newSMTP(cfg Config) (Provider, error) {
	p := &smtpProvider{
		host: cfg.String("host"), port: cfg.Int("port"),
		username: cfg.String("username"), password: cfg.String("password"),
		tlsMode: cfg.String("tls"),
	}
	if p.tlsMode == "" {
		p.tlsMode = "ssl"
	}
	if p.host == "" {
		return nil, errors.New("host 不能为空")
	}
	if p.port < 1 || p.port > 65535 {
		return nil, errors.New("port 必须在 1–65535 之间")
	}
	if p.tlsMode != "ssl" && p.tlsMode != "starttls" && p.tlsMode != "none" {
		return nil, errors.New("tls 只能是 ssl、starttls 或 none")
	}
	from, err := mail.ParseAddress(cfg.String("from"))
	if err != nil {
		return nil, errors.New("from 不是合法的邮箱地址")
	}
	p.from = from
	// net/smtp 的 PlainAuth 拒绝在不加密的连接上发密码（本机回环除外）；在配置时就报，
	// 比等到第一次发送才失败好。
	if p.tlsMode == "none" && p.username != "" && !isLoopbackHost(p.host) {
		return nil, errors.New("不加密（tls=none）时不能配置用户名密码：请改用 ssl 或 starttls")
	}
	return p, nil
}

// isLoopbackHost 与 net/smtp 的 PlainAuth 认的"本机"保持一致，只有这三个名字：放宽的话，
// 127.0.0.2、::ffff:127.0.0.1 这类配置保存时通过，第一次发送才报 unencrypted connection。
func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (p *smtpProvider) Send(ctx context.Context, d Delivery) error {
	// vendor 模板永不被渲染，故无内容可发，拒绝发送。
	if d.Template.Mode == domain.NotifyModeVendor {
		return errors.New("SMTP 供应商仅支持发送自定义（fp 渲染）模板，不支持供应商模板")
	}
	ct := d.Template.Content.ContentType
	if ct == "" {
		ct = "text/plain"
	}
	to, msg, err := buildEmail(p.from, d.To, d.Rendered.Subject, ct, d.Rendered.Body, time.Now())
	if err != nil {
		return err
	}
	return p.deliver(ctx, to, msg)
}

// buildEmail 组装一封 RFC 5322 邮件，返回收件人地址与报文。
//
// 收件人经 mail.ParseAddress 校验、只取地址部分：它来自业务方，换行符会注入新的邮件头。
// 主题用 RFC 2047 编码；正文一律 base64，省得处理 8bit 与行长限制。
func buildEmail(from *mail.Address, to, subject, contentType, body string, now time.Time) (string, []byte, error) {
	toAddr, err := mail.ParseAddress(to)
	// ParseAddress 会去掉本地部分的引号（引号里的空格与 TAB 它都放行），去引号后的地址原样进 RCPT 命令：
	// 空白与尖括号能往信封里塞 ESMTP 参数（"x> NOTIFY=NEVER"@a.com），多出来的 @ 能让实际收件域不是业务方校验过的那个。
	if err != nil || strings.ContainsAny(toAddr.Address, " \t<>\"\\") || strings.Count(toAddr.Address, "@") != 1 {
		return "", nil, errors.New("收件人邮箱不合法")
	}
	var b strings.Builder
	b.WriteString("From: " + from.String() + "\r\n")
	b.WriteString("To: " + (&mail.Address{Address: toAddr.Address}).String() + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n")
	b.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: " + messageID(from.Address) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: " + contentType + "; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")

	enc := base64.StdEncoding.EncodeToString([]byte(body))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return toAddr.Address, []byte(b.String()), nil
}

// messageID 是随机 16 字节加发件人的域名：Gmail 等收件方要求邮件带有效的 Message-ID，
// 经自建中继直投时没有它会被拒。除了发件人的域名不带任何别的信息。
func messageID(fromAddress string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // Go 1.24 起 crypto/rand.Read 不会失败
	return "<" + hex.EncodeToString(b) + "@" + fromAddress[strings.LastIndex(fromAddress, "@")+1:] + ">"
}

func (p *smtpProvider) deliver(ctx context.Context, to string, msg []byte) error {
	addr := net.JoinHostPort(p.host, strconv.Itoa(p.port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var (
		conn net.Conn
		err  error
	)
	if p.tlsMode == "ssl" {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: p.host}}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("连接 SMTP 服务器: %w", err)
	}
	defer conn.Close()
	// 连上之后 net/smtp 不看 ctx，只认连接上的截止时间：ctx 一取消就关掉连接，卡在读写里的命令立刻返回。
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	deadline := time.Now().Add(smtpTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	c, err := smtp.NewClient(conn, p.host)
	if err != nil {
		return fmt.Errorf("SMTP 握手: %w", err)
	}
	defer c.Close()

	if p.tlsMode == "starttls" {
		if err := c.StartTLS(&tls.Config{ServerName: p.host}); err != nil {
			return fmt.Errorf("SMTP STARTTLS: %w", err)
		}
	}
	if p.username != "" {
		if err := c.Auth(smtp.PlainAuth("", p.username, p.password, p.host)); err != nil {
			return fmt.Errorf("SMTP 认证: %w", err)
		}
	}
	if err := c.Mail(p.from.Address); err != nil {
		return fmt.Errorf("SMTP MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("SMTP RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("SMTP 写入正文: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP 提交邮件: %w", err)
	}
	// 邮件已被服务器接受：之后 QUIT 失败、卡住，或者 ctx 取消关掉了连接，都不改变成功的结果。
	_ = conn.SetDeadline(time.Now().Add(smtpQuitTimeout))
	_ = c.Quit()
	return nil
}
