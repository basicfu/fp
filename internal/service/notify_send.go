package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/notify"
)

const (
	// 幂等键的两个状态。"处理中"的 TTL 要大于一次发送的最长耗时（逐家降级、每家 30s 超时），
	// 进程在发送中途崩掉时它自己过期，重试就能重发；"已完成"留 24 小时。
	notifyIdemProcessing    = "1"
	notifyIdemDone          = "2"
	notifyIdemProcessingTTL = 60 * time.Second
	notifyIdemDoneTTL       = 24 * time.Hour

	notifyAttemptTimeout = 30 * time.Second
	notifyMaxErrorRunes  = 500
)

// NotifySendInput 是一次发送请求。
type NotifySendInput struct {
	// AppID 是调用方应用；控制台测试发送为空。只进发送记录。
	AppID string
	Code  string
	// To 是收件人：sms 是手机号，email 是邮箱；IM 与 webhook 必须为空。
	To     string
	Params map[string]string
	// IdempotencyKey 非空时，相同 code + key 在 24 小时内只会真正发送一次。
	IdempotencyKey string
}

type notifyCandidate struct {
	providerID         uuid.UUID
	typ                string
	config             map[string]any
	updatedAt          time.Time
	providerTemplateID string
	priority           int
}

// Send 按模板 code 发送一条通知。
//
// 流程：取模板 → 校验收件人与变量 → 幂等占位 → 选供应商并发送（sms / email 逐家降级）→ 落记录。
// 全部供应商都失败时只返回通用错误：具体原因进 notify_log 与服务端日志，不回给调用方，
// 避免把供应商侧的信息泄露出去。
func (s *NotifyService) Send(ctx context.Context, in NotifySendInput) error {
	tpl, err := s.loadTemplate(ctx, in.Code)
	if err != nil {
		return err
	}
	if !tpl.Enabled {
		return domain.Failf(domain.ErrForbidden, domain.CodeNotifyTemplateDisabled, "通知模板 %q 已停用", in.Code)
	}
	if tpl.Channel.NeedsRecipient() && in.To == "" {
		return domain.Failf(domain.ErrInvalidArgument, domain.CodeNotifyRecipientInvalid, "%s 渠道必须指定收件人", tpl.Channel)
	}
	if !tpl.Channel.NeedsRecipient() && in.To != "" {
		return domain.Failf(domain.ErrInvalidArgument, domain.CodeNotifyRecipientInvalid, "%s 渠道发给配置好的固定目标，不能指定收件人", tpl.Channel)
	}
	if err := notify.ValidateParams(tpl.Content.Variables, in.Params); err != nil {
		return err
	}

	idem, err := s.acquireIdem(ctx, in)
	if err != nil {
		return err
	}
	if idem == idemDone {
		return nil
	}
	sendErr := s.dispatch(ctx, tpl, in)
	s.settleIdem(ctx, in, idem, sendErr)
	return sendErr
}

type idemState int

const (
	idemSkipped  idemState = iota // 没带幂等键
	idemAcquired                  // 抢到了，这次由我们发送
	idemDone                      // 相同的请求已经发送成功
)

func notifyIdemKey(code, key string) string { return "fp:notify:idem:" + code + ":" + key }

func (s *NotifyService) acquireIdem(ctx context.Context, in NotifySendInput) (idemState, error) {
	if in.IdempotencyKey == "" {
		return idemSkipped, nil
	}
	key := notifyIdemKey(in.Code, in.IdempotencyKey)
	inProgress := domain.Fail(domain.ErrConflict, domain.CodeNotifyInProgress, "相同幂等键的通知正在发送中，请稍后重试")
	for range 2 {
		ok, err := s.rdb.SetNX(ctx, key, notifyIdemProcessing, notifyIdemProcessingTTL).Result()
		if err != nil {
			return 0, fmt.Errorf("service: 幂等键占位: %w", err)
		}
		if ok {
			return idemAcquired, nil
		}
		v, err := s.rdb.Get(ctx, key).Result()
		if errors.Is(err, redis.Nil) {
			continue // 刚好过期，再抢一次
		}
		if err != nil {
			return 0, fmt.Errorf("service: 读取幂等键: %w", err)
		}
		if v == notifyIdemDone {
			return idemDone, nil
		}
		return 0, inProgress
	}
	return 0, inProgress
}

// settleIdem 把幂等键置为"已完成"，发送失败时删掉它——让重试真的能重发。
// 用脱离取消的 ctx：调用方断开连接不该让"已发送"这件事没记上，否则它的重试会重发。
func (s *NotifyService) settleIdem(ctx context.Context, in NotifySendInput, st idemState, sendErr error) {
	if st != idemAcquired {
		return
	}
	ctx = context.WithoutCancel(ctx)
	key := notifyIdemKey(in.Code, in.IdempotencyKey)
	var err error
	if sendErr != nil {
		err = s.rdb.Del(ctx, key).Err()
	} else {
		err = s.rdb.Set(ctx, key, notifyIdemDone, notifyIdemDoneTTL).Err()
	}
	if err != nil {
		slog.Warn("notify: 更新幂等键失败", "code", in.Code, "err", err)
	}
}

func (s *NotifyService) candidates(ctx context.Context, code string) ([]notifyCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.type, p.config, p.updated_at, tp.provider_template_id, tp.priority
		FROM notify_template_provider tp JOIN notify_provider p ON p.id = tp.provider_id
		WHERE tp.code = $1 AND tp.enabled AND p.enabled
		ORDER BY p.created_at, p.id`, code)
	if err != nil {
		return nil, fmt.Errorf("service: 查询候选供应商: %w", err)
	}
	defer rows.Close()
	var out []notifyCandidate
	for rows.Next() {
		var (
			c   notifyCandidate
			raw []byte
		)
		if err := rows.Scan(&c.providerID, &c.typ, &raw, &c.updatedAt, &c.providerTemplateID, &c.priority); err != nil {
			return nil, fmt.Errorf("service: 扫描候选供应商: %w", err)
		}
		if err := json.Unmarshal(raw, &c.config); err != nil {
			return nil, fmt.Errorf("service: 解析供应商配置: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// 候选先按创建顺序取出（上面的 ORDER BY），让"洗牌前的顺序"是确定的，测试才能注入洗牌函数得到可预期的结果。
// order 把候选排成尝试顺序：priority 高的在前，同优先级内随机。
// 先整体洗牌、再按 priority 稳定排序，同优先级内的相对顺序就是洗牌的结果。
func (s *NotifyService) order(cands []notifyCandidate) {
	s.shuffle(len(cands), func(i, j int) { cands[i], cands[j] = cands[j], cands[i] })
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].priority > cands[j].priority })
}

func (s *NotifyService) dispatch(ctx context.Context, tpl *domain.NotifyTemplate, in NotifySendInput) error {
	cands, err := s.candidates(ctx, tpl.Code)
	if err != nil {
		return err
	}
	if len(cands) == 0 {
		return domain.Failf(domain.ErrNotFound, domain.CodeNotifyProviderMissing, "通知模板 %q 没有可用的供应商", tpl.Code)
	}

	d := notify.Delivery{To: in.To, Params: in.Params, Template: *tpl}
	if tpl.Mode == domain.NotifyModeCustom {
		d.Rendered = notify.Render(*tpl, in.Params)
	}

	s.order(cands)
	if !tpl.Channel.NeedsRecipient() {
		cands = cands[:1] // IM 与 webhook 一对一，不降级
	}

	var lastErr error
	for _, c := range cands {
		d.ProviderTemplateID = c.providerTemplateID
		err := s.sendVia(ctx, c, d)
		s.writeNotifyLog(ctx, tpl, in, c, err)
		if err == nil {
			return nil
		}
		lastErr = err
		slog.Warn("notify: 供应商发送失败", "code", tpl.Code, "provider", c.typ, "providerId", c.providerID, "err", err)
	}
	slog.Error("notify: 全部供应商发送失败", "code", tpl.Code, "err", lastErr)
	return domain.Fail(domain.ErrInternal, domain.CodeNotifySendFailed, "通知发送失败，请稍后重试")
}

func (s *NotifyService) sendVia(ctx context.Context, c notifyCandidate, d notify.Delivery) error {
	p, err := s.providerFor(c)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, notifyAttemptTimeout)
	defer cancel()
	return p.Send(ctx, d)
}

// providerFor 按 id@updated_at 缓存已构造的供应商：改配置后 updated_at 变化，天然换新，
// 不需要跨实例的失效广播。
func (s *NotifyService) providerFor(c notifyCandidate) (notify.Provider, error) {
	key := c.providerID.String() + "@" + c.updatedAt.UTC().Format(time.RFC3339Nano)
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.cache[key]; ok {
		return p, nil
	}
	sp, ok := s.reg.Spec(c.typ)
	if !ok {
		return nil, fmt.Errorf("供应商类型 %q 已不受支持", c.typ)
	}
	p, err := sp.New(notify.Config(c.config))
	if err != nil {
		return nil, fmt.Errorf("构造供应商: %w", err)
	}
	if len(s.cache) >= 256 {
		clear(s.cache)
	}
	s.cache[key] = p
	return p, nil
}

// writeNotifyLog 写一次尝试的记录。**绝不写变量取值与渲染后的内容**：里面可能是验证码、
// 姓名之类的敏感值。记录失败不影响发送结果，只打日志。
func (s *NotifyService) writeNotifyLog(ctx context.Context, tpl *domain.NotifyTemplate, in NotifySendInput, c notifyCandidate, sendErr error) {
	errText := ""
	if sendErr != nil {
		errText = truncateRunes(sendErr.Error(), notifyMaxErrorRunes)
	}
	_, err := s.pool.Exec(context.WithoutCancel(ctx), `
		INSERT INTO notify_log (channel, target, code, provider, provider_id, app_id, success, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		string(tpl.Channel), in.To, tpl.Code, c.typ, c.providerID, in.AppID, sendErr == nil, errText)
	if err != nil {
		slog.Error("notify: 写入发送记录失败", "err", err)
	}
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
