package notify

import (
	"context"
	"log/slog"
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

// LoggingFakeProvider 包装 FakeProvider，在每次发送成功后把整条消息
// （含验证码，就在 Params 里）以 WARN 级别打进日志。
//
// 门控条件是"当前用的是假供应商"，不能换成"非生产环境"：cmd/fp/main.go
// 按三档规则选供应商——阿里云四项凭据配齐就用真实供应商，不管是不是生产
// 环境；凭据不全且是生产环境则启动失败；只有真正落到"用假供应商"这一档，
// 调用方才会构造 LoggingFakeProvider。这意味着任何配齐了阿里云凭据的部署
// （哪怕是非生产）都走不到这里，日志里不会出现真实验证码；反过来，如果
// 门控条件写成"非生产环境"，一个非生产但配齐凭据、走真实短信通道的部署
// 也会命中，那就是把真实验证码打进了日志——是真实凭据泄露，不是权宜之计。
//
// 假供应商在用时打日志不是权宜之计，而是唯一的送达途径：FakeProvider
// 只把消息留在内存里，进程外读不到，验证码除了日志没有其他地方可以看到
// （见 examples/demo/README.md 手工验收第 2 步）。
//
// 只应在 cmd/fp/main.go 判定"要用假供应商"的那个分支里构造；生产部署
// 走不到那个分支，也就不会用到这个类型。
type LoggingFakeProvider struct {
	*FakeProvider
}

// NewLoggingFakeProvider 构造一个发送成功后会把消息打进 WARN 日志的假供应商。
func NewLoggingFakeProvider(ch Channel, name string) *LoggingFakeProvider {
	return &LoggingFakeProvider{FakeProvider: NewFakeProvider(ch, name)}
}

// Send 实现 Provider。先委托给 FakeProvider——内存留存不能丢，
// examples/demo 与既有测试都靠 Sent()/LastParam() 读取；委托成功后才打
// 日志，发送失败没有码可打，也不该打一条看起来像"发出去了"的记录。
func (p *LoggingFakeProvider) Send(ctx context.Context, msg Message) error {
	if err := p.FakeProvider.Send(ctx, msg); err != nil {
		return err
	}
	slog.Warn("notify: 假供应商收到一条消息——这不是真短信，不会真的送达，"+
		"仅用于本地开发/手工验收；验证码就在 params 里",
		"channel", msg.Channel, "to", msg.To, "template", msg.Template, "params", msg.Params)
	return nil
}
