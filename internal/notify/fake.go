package notify

import (
	"context"
	"sync"

	"github.com/basicfu/fp/internal/domain"
)

// FakeProvider 是内存中的假供应商，用于测试与本地开发。
// 它把发出的消息留在内存里，测试可以直接读出验证码，无需真实短信通道。
//
// 生产部署绝不应注册它。
type FakeProvider struct {
	name string
	ch   Channel

	mu       sync.Mutex
	sent     []Message
	failNext error
}

// NewFakeProvider 构造一个假供应商。
func NewFakeProvider(ch Channel, name string) *FakeProvider {
	return &FakeProvider{name: name, ch: ch}
}

// Name 实现 Provider。
func (p *FakeProvider) Name() string { return p.name }

// Channel 实现 Provider。
func (p *FakeProvider) Channel() Channel { return p.ch }

// ConfigSchema 实现 Provider。假供应商没有可配置项。
func (p *FakeProvider) ConfigSchema() []domain.Field { return nil }

// Send 实现 Provider。若已通过 FailNext 预置错误，则消费掉该错误并返回。
func (p *FakeProvider) Send(_ context.Context, msg Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.failNext != nil {
		err := p.failNext
		p.failNext = nil
		return err
	}
	p.sent = append(p.sent, msg)
	return nil
}

// FailNext 让下一次 Send 返回 err。
func (p *FakeProvider) FailNext(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failNext = err
}

// Sent 返回已成功发出的全部消息的副本。
func (p *FakeProvider) Sent() []Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Message, len(p.sent))
	copy(out, p.sent)
	return out
}

// LastParam 返回最后一条消息里指定参数的值，没有消息时返回空串。
func (p *FakeProvider) LastParam(key string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.sent) == 0 {
		return ""
	}
	return p.sent[len(p.sent)-1].Params[key]
}
