package testsupport

import (
	"context"
	"sync"

	"github.com/basicfu/fp/internal/service"
)

// FakeLoginSMS 顶替通知中心接住 AuthService.SendLoginCode 的发送请求，测试从里面取回验证码。
// 它满足 service.LoginCodeSender。
type FakeLoginSMS struct {
	mu   sync.Mutex
	sent []service.NotifySendInput
	err  error
}

// Send 记录请求；设过 FailWith 就返回那个错误（请求照样记下，限流等行为要靠它判断有没有真发）。
func (f *FakeLoginSMS) Send(_ context.Context, in service.NotifySendInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, in)
	return f.err
}

// FailWith 让之后的发送都返回 err；传 nil 恢复正常。
func (f *FakeLoginSMS) FailWith(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// Sent 返回迄今收到的全部请求。
func (f *FakeLoginSMS) Sent() []service.NotifySendInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]service.NotifySendInput(nil), f.sent...)
}

// LastCode 返回最近一次请求带的验证码；没有请求时为空串。
func (f *FakeLoginSMS) LastCode() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return ""
	}
	return f.sent[len(f.sent)-1].Params["code"]
}
