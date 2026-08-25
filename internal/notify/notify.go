// Package notify 是 fp 的通知中心。
//
// 它把「发什么」（Message）与「谁来发」（Provider）分开：
// 业务只声明模板与参数，具体走哪家供应商由配置决定，主供应商失败自动降级到下一家。
// 这是对 3s 中切换供应商靠改代码注释、模板 ID 硬编码在 if-else 里的直接修正。
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/store"
)

// Channel 是通知通道。
type Channel string

const (
	ChannelSMS   Channel = "sms"
	ChannelEmail Channel = "email"
)

// Message 是一条待发送的通知。Template 是 fp 内部的模板 key，
// 由各 Provider 映射到自己那边的模板 ID。
type Message struct {
	Channel  Channel
	To       string
	Template string
	Params   map[string]string
}

// Provider 是一家通知供应商。
type Provider interface {
	// Name 是供应商标识，写入发送记录用于排障。
	Name() string
	// Channel 是该供应商负责的通道。
	Channel() Channel
	// ConfigSchema 描述该供应商的可配置项，供管理 UI 渲染表单。
	ConfigSchema() []domain.Field
	// Send 发送一条通知。失败时 Sender 会降级到下一家。
	Send(ctx context.Context, msg Message) error
}

// RateRule 是一条频率限制规则，按接收方（手机号 / 邮箱）计数。
type RateRule struct {
	Name   string
	Window time.Duration
	Limit  int
}

// DefaultSMSRateRules 是短信的默认频率限制，取自 3s 的 risk:smssend 策略。
func DefaultSMSRateRules() []RateRule {
	return []RateRule{
		{Name: "30s", Window: 30 * time.Second, Limit: 1},
		{Name: "1h", Window: time.Hour, Limit: 5},
		{Name: "1d", Window: 24 * time.Hour, Limit: 10},
	}
}

// Sender 编排一次发送：频率限制 → 按顺序尝试供应商 → 落发送记录。
type Sender struct {
	pool      *pgxpool.Pool
	limiter   *store.RateLimiter
	rules     []RateRule
	providers map[Channel][]Provider
}

// NewSender 构造 Sender。rules 为 nil 表示不做频率限制。
func NewSender(pool *pgxpool.Pool, limiter *store.RateLimiter, rules []RateRule) *Sender {
	return &Sender{
		pool:      pool,
		limiter:   limiter,
		rules:     rules,
		providers: make(map[Channel][]Provider),
	}
}

// AddProvider 追加一家供应商。同通道内按加入顺序尝试，先加入的是主供应商。
func (s *Sender) AddProvider(p Provider) {
	ch := p.Channel()
	s.providers[ch] = append(s.providers[ch], p)
}

// Send 发送一条通知。
//
// 频率超限返回 domain.ErrRateLimited；该通道没有可用供应商返回 domain.ErrNotFound；
// 全部供应商都失败时返回最后一次的错误。
func (s *Sender) Send(ctx context.Context, msg Message) error {
	if msg.To == "" {
		return domain.Errorf(domain.ErrInvalidArgument, "接收方不能为空")
	}
	providers := s.providers[msg.Channel]
	if len(providers) == 0 {
		return domain.Errorf(domain.ErrNotFound, "通道 %s 没有配置供应商", msg.Channel)
	}

	// 频率限制先于供应商调用，避免被限流的请求也消耗供应商额度。
	for _, rule := range s.rules {
		key := fmt.Sprintf("notify:%s:%s:%s", msg.Channel, rule.Name, msg.To)
		allowed, retryAfter, err := s.limiter.Allow(ctx, key, rule.Window, rule.Limit)
		if err != nil {
			return err
		}
		if !allowed {
			return domain.Errorf(domain.ErrRateLimited,
				"发送过于频繁，请 %d 秒后重试", int(retryAfter.Seconds())+1)
		}
	}

	var lastErr error
	for _, p := range providers {
		err := p.Send(ctx, msg)
		s.writeLog(ctx, msg, p.Name(), err)
		if err == nil {
			return nil
		}
		lastErr = err
		slog.Warn("notify: 供应商发送失败，尝试降级",
			"provider", p.Name(), "channel", msg.Channel, "err", err)
	}
	return fmt.Errorf("notify: 全部供应商发送失败: %w", lastErr)
}

// writeLog 写发送记录。
//
// 只记录通道、接收方、模板 key、供应商与结果——**绝不写入 Params**，
// 因为验证码就在里面。记录失败不影响发送结果，只打日志。
func (s *Sender) writeLog(ctx context.Context, msg Message, provider string, sendErr error) {
	errText := ""
	if sendErr != nil {
		errText = sendErr.Error()
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO notify_log (channel, target, template, provider, success, error)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		string(msg.Channel), msg.To, msg.Template, provider, sendErr == nil, errText)
	if err != nil {
		slog.Error("notify: 写入发送记录失败", "err", err)
	}
}
