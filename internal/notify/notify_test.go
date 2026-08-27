package notify_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/notify"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

func newSender(t *testing.T, rules []notify.RateRule) (*notify.Sender, *notify.FakeProvider) {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	limiter := store.NewRateLimiter(testsupport.NewTestRedis(t))
	s := notify.NewSender(pool, limiter, rules)
	p := notify.NewFakeProvider(notify.ChannelSMS, "fake")
	s.AddProvider(p)
	return s, p
}

func msg(to string) notify.Message {
	return notify.Message{
		Channel:  notify.ChannelSMS,
		To:       to,
		Template: "login_code",
		Params:   map[string]string{"code": "123456"},
	}
}

func TestSenderDelivers(t *testing.T) {
	s, p := newSender(t, nil)

	if err := s.Send(context.Background(), msg("13800138000")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	sent := p.Sent()
	if len(sent) != 1 {
		t.Fatalf("发送数量 = %d, want 1", len(sent))
	}
	if sent[0].To != "13800138000" || sent[0].Template != "login_code" {
		t.Fatalf("sent = %+v", sent[0])
	}
	if p.LastParam("code") != "123456" {
		t.Fatalf("code 参数 = %q", p.LastParam("code"))
	}
}

func TestSenderRejectsUnknownChannel(t *testing.T) {
	s, _ := newSender(t, nil)
	m := msg("13800138000")
	m.Channel = notify.ChannelEmail

	if err := s.Send(context.Background(), m); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSenderEnforcesRateRules(t *testing.T) {
	rules := []notify.RateRule{{Name: "burst", Window: time.Minute, Limit: 2}}
	s, p := newSender(t, rules)
	ctx := context.Background()

	for i := 1; i <= 2; i++ {
		if err := s.Send(ctx, msg("13800138000")); err != nil {
			t.Fatalf("第 %d 次 Send: %v", i, err)
		}
	}
	if err := s.Send(ctx, msg("13800138000")); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("第 3 次 err = %v, want ErrRateLimited", err)
	}
	if len(p.Sent()) != 2 {
		t.Fatalf("被限流的请求不应到达 provider, sent = %d", len(p.Sent()))
	}

	// 限流按号码隔离
	if err := s.Send(ctx, msg("13900139000")); err != nil {
		t.Fatalf("另一号码 Send: %v", err)
	}
}

// rules 传 nil 必须落到 DefaultSMSRateRules()，而不是"什么都不限"。
//
// 这条断言盯的是一个纯配置事故：仓库里唯一一份 Sender 的装配示例长期写着
// NewSender(pool, limiter, nil)，DefaultSMSRateRules 一个调用方都没有。
// 照着抄一份上线，就是一个手机号可以无限刷验证码——账单和骚扰都是真的。
func TestNilRulesFallsBackToDefaultSMSLimits(t *testing.T) {
	s, p := newSender(t, nil)
	ctx := context.Background()

	// 默认规则最紧的一档是"30 秒 1 条"
	if err := s.Send(ctx, msg("13800138000")); err != nil {
		t.Fatalf("首次 Send: %v", err)
	}
	if err := s.Send(ctx, msg("13800138000")); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("第二次 Send err = %v, want ErrRateLimited——nil rules 没有套用默认限制", err)
	}
	if len(p.Sent()) != 1 {
		t.Fatalf("被限流的请求不应到达 provider, sent = %d", len(p.Sent()))
	}
}

// 空切片才是"显式关掉限制"的写法，必须与 nil 区分开——测试装配依赖它。
func TestEmptyRulesDisablesRateLimiting(t *testing.T) {
	s, p := newSender(t, []notify.RateRule{})
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		if err := s.Send(ctx, msg("13800138000")); err != nil {
			t.Fatalf("第 %d 次 Send: %v", i, err)
		}
	}
	if len(p.Sent()) != 3 {
		t.Fatalf("sent = %d, want 3（空切片应当完全不限流）", len(p.Sent()))
	}
}

// 主供应商失败时自动降级到下一个——对症 3s 靠改注释切供应商的问题。
func TestSenderFallsBackToNextProvider(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	limiter := store.NewRateLimiter(testsupport.NewTestRedis(t))
	s := notify.NewSender(pool, limiter, nil)

	primary := notify.NewFakeProvider(notify.ChannelSMS, "primary")
	backup := notify.NewFakeProvider(notify.ChannelSMS, "backup")
	s.AddProvider(primary)
	s.AddProvider(backup)

	primary.FailNext(errors.New("供应商余额不足"))

	ctx := context.Background()
	if err := s.Send(ctx, msg("13800138000")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(primary.Sent()) != 0 {
		t.Fatalf("primary 不应成功发出, sent = %d", len(primary.Sent()))
	}
	if len(backup.Sent()) != 1 {
		t.Fatalf("backup 应接手, sent = %d", len(backup.Sent()))
	}

	// 降级必须在发送记录里留下痕迹：失败一条 + 成功一条。
	// 只看"消息最终送达"是不够的，排障时要能看出主供应商挂过。
	var total, failed int
	if err := pool.QueryRow(ctx,
		`SELECT count(*), count(*) FILTER (WHERE NOT success) FROM notify_log WHERE target = $1`,
		"13800138000").Scan(&total, &failed); err != nil {
		t.Fatalf("查询发送记录: %v", err)
	}
	if total != 2 || failed != 1 {
		t.Fatalf("发送记录 total=%d failed=%d, want 2/1（每次尝试一条）", total, failed)
	}
}

// 只有"主挂了备接手"是测不出顺序的：反序尝试、或者无脑广播给所有供应商，
// 观察到的结果完全一样。用两个都健康的供应商才能钉住"按顺序、命中即停"。
func TestSenderUsesFirstProviderAndStops(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	limiter := store.NewRateLimiter(testsupport.NewTestRedis(t))
	s := notify.NewSender(pool, limiter, nil)

	primary := notify.NewFakeProvider(notify.ChannelSMS, "primary")
	backup := notify.NewFakeProvider(notify.ChannelSMS, "backup")
	s.AddProvider(primary)
	s.AddProvider(backup)

	ctx := context.Background()
	if err := s.Send(ctx, msg("13800138000")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(primary.Sent()) != 1 {
		t.Fatalf("primary 应收到, sent = %d（先加入者即主供应商）", len(primary.Sent()))
	}
	if len(backup.Sent()) != 0 {
		t.Fatalf("primary 成功后不应再试 backup, sent = %d", len(backup.Sent()))
	}

	var total int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM notify_log WHERE target = $1`, "13800138000").Scan(&total); err != nil {
		t.Fatalf("查询发送记录: %v", err)
	}
	if total != 1 {
		t.Fatalf("发送记录数 = %d, want 1（命中即停，不应给每个供应商都记一条）", total)
	}
}

func TestSenderReturnsErrorWhenAllProvidersFail(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	limiter := store.NewRateLimiter(testsupport.NewTestRedis(t))
	s := notify.NewSender(pool, limiter, nil)

	p := notify.NewFakeProvider(notify.ChannelSMS, "only")
	cause := errors.New("网络不可达")
	p.FailNext(cause)
	s.AddProvider(p)

	err := s.Send(context.Background(), msg("13800138000"))
	if err == nil {
		t.Fatal("全部供应商失败时应返回错误")
	}
	// 必须把底层原因包在错误链里，否则排障时只能看到"全部失败"这种废话
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, 未包住底层原因 %v", err, cause)
	}
}

// 发送记录必须落库，但绝不能写入验证码本身。
func TestSenderWritesLogWithoutCode(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	limiter := store.NewRateLimiter(testsupport.NewTestRedis(t))
	s := notify.NewSender(pool, limiter, nil)
	s.AddProvider(notify.NewFakeProvider(notify.ChannelSMS, "fake"))
	ctx := context.Background()

	if err := s.Send(ctx, msg("13800138000")); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var (
		n        int
		provider string
	)
	if err := pool.QueryRow(ctx,
		`SELECT count(*), coalesce(max(provider), '') FROM notify_log WHERE target = $1`,
		"13800138000").Scan(&n, &provider); err != nil {
		t.Fatalf("查询发送记录: %v", err)
	}
	if n != 1 {
		t.Fatalf("发送记录数 = %d, want 1", n)
	}
	if provider != "fake" {
		t.Fatalf("provider = %q, want fake", provider)
	}

	// 整行转成文本再查，而不是逐列列举。
	//
	// 逐列写法有个隐蔽的失效模式：将来有人给 notify_log 加了 params 列并把
	// msg.Params 写进去，行数断言照样是 1，而 LIKE 子句压根不会看那个新列——
	// 测试通过，验证码却泄露了。整行匹配天然覆盖未来新增的任何列。
	var leaked int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM notify_log WHERE notify_log::text LIKE '%123456%'`).Scan(&leaked); err != nil {
		t.Fatalf("检查验证码泄露: %v", err)
	}
	if leaked != 0 {
		t.Fatal("发送记录中出现了验证码明文")
	}
}
