package notify

import (
	"context"
	"fmt"
	"sort"

	"github.com/basicfu/fp/internal/domain"
)

// Config 是一个供应商实例的配置（notify_provider.config），键由该 type 的 ConfigSchema 定义。
// 从数据库读回来的 JSON 数字是 float64，取值一律经下面的访问器。
type Config map[string]any

func (c Config) String(key string) string {
	s, _ := c[key].(string)
	return s
}

func (c Config) Int(key string) int {
	switch n := c[key].(type) {
	case int64:
		return int(n)
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

func (c Config) Bool(key string) bool {
	b, _ := c[key].(bool)
	return b
}

// Delivery 是交给某个供应商实例的一次发送。
type Delivery struct {
	To       string // sms / email 的收件人；IM 与 webhook 为空
	Params   map[string]string
	Template domain.NotifyTemplate
	// ProviderTemplateID 仅 vendor 模式：该实例在供应商那边审核通过的模板 ID。
	ProviderTemplateID string
	// Rendered 仅 custom 模式：已经渲染好的内容。
	Rendered Rendered
}

// Provider 是一个供应商实例。实现必须在 ctx 取消时尽快返回，且返回的错误里不能带凭据。
type Provider interface {
	Send(ctx context.Context, d Delivery) error
}

// TypeSpec 描述一种供应商类型：代码里写死的实现。
type TypeSpec struct {
	Type string
	// Channel 是这种类型服务的渠道；log 类型留空，渠道由实例 config 里的 channel 决定。
	Channel domain.NotifyChannel
	// ConfigSchema 描述可配置项，控制台据此渲染表单，新增类型不用改前端。
	ConfigSchema []domain.Field
	// DevOnly 的类型在 prod 环境不可建、不暴露。
	DevOnly bool
	// New 用已经过 domain.NormalizeConfig 的配置构造实例；配置不合法时返回不含凭据的错误。
	New func(cfg Config) (Provider, error)
}

// ChannelOf 返回用这种类型、这份配置的实例服务的渠道。
func (s TypeSpec) ChannelOf(cfg map[string]any) domain.NotifyChannel {
	if s.Channel != "" {
		return s.Channel
	}
	ch, _ := cfg["channel"].(string)
	return domain.NotifyChannel(ch)
}

// Registry 是进程内的供应商类型注册表。构造后只读，无需加锁。
type Registry struct {
	specs map[string]TypeSpec
}

func NewRegistry() *Registry {
	return &Registry{specs: make(map[string]TypeSpec)}
}

// Register 登记一种类型。类型为空或重复时报错。
func (r *Registry) Register(s TypeSpec) error {
	if s.Type == "" || s.New == nil {
		return fmt.Errorf("notify: 供应商类型与构造函数不能为空")
	}
	if _, ok := r.specs[s.Type]; ok {
		return fmt.Errorf("notify: 供应商类型 %q 已注册", s.Type)
	}
	r.specs[s.Type] = s
	return nil
}

func (r *Registry) Spec(typ string) (TypeSpec, bool) {
	s, ok := r.specs[typ]
	return s, ok
}

// Types 按类型名排序返回全部类型；isProd 时去掉 DevOnly 的。
func (r *Registry) Types(isProd bool) []TypeSpec {
	out := make([]TypeSpec, 0, len(r.specs))
	for _, s := range r.specs {
		if isProd && s.DevOnly {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// DefaultRegistry 登记全部内置的供应商类型。新增一家供应商就是在这里加一行。
func DefaultRegistry() *Registry {
	r := NewRegistry()
	for _, s := range []TypeSpec{aliyunSpec, smtpSpec, telegramSpec, wecomSpec, dingtalkSpec, webhookSpec, logSpec} {
		if err := r.Register(s); err != nil {
			panic(err) // 内置类型重复登记是编程错误，由 TestDefaultRegistry 守住
		}
	}
	return r
}
