package notify

import (
	"errors"
	"fmt"
	"net/textproto"
	"testing"
)

// 阿里云邮件推送的同步回复:地址本身不行的包成 ErrRecipientRejected,发信方的问题(认证、额度、反垃圾)原样返回。
func TestRecipientRejection(t *testing.T) {
	cases := []struct {
		code int
		msg  string
		want bool
	}{
		{559, `Invalid rcptto [@sm070102] at DATA State ... invaddr reject`, true},
		{563, "Rcptto is on the account-level bounce suppression list", true},
		{501, "Bad address syntax", true},
		{561, "Honeypot rcptto", true},
		{550, "Mailbox not found", true},
		{554, "delivery error: dd This user doesn't have a yahoo.com account (no such user)", true},
		{550, "SPF check failed", false},
		{554, "Reject by content spam", false},
		{552, "Daily sending quota exceeded", false},
		{535, "Authentication failure.", false},
		{553, `"MAIL FROM" is local account`, false},
	}
	for _, c := range cases {
		err := recipientRejection(fmt.Errorf("SMTP 提交邮件: %w", &textproto.Error{Code: c.code, Msg: c.msg}))
		if got := errors.Is(err, ErrRecipientRejected); got != c.want {
			t.Errorf("%d %s: 拒收 = %v, want %v", c.code, c.msg, got, c.want)
		}
		var te *textproto.Error
		if !errors.As(err, &te) || te.Code != c.code {
			t.Errorf("%d: 原始回复要还在错误链里: %v", c.code, err)
		}
	}
	if err := recipientRejection(errors.New("连接 SMTP 服务器: timeout")); errors.Is(err, ErrRecipientRejected) {
		t.Fatal("不是 SMTP 回复的错误不算拒收")
	}
}
