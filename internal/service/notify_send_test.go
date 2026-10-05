package service_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

// reverseShuffle 把候选整个倒过来：洗牌前的顺序是创建顺序（a, b, c），洗牌后是 c, b, a，
// 再按 priority 稳定排序，同优先级内就保持倒序——"随机"因此变成确定的。
func reverseShuffle(n int, swap func(i, j int)) {
	for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
		swap(i, j)
	}
}

func notifySend(e *notifyEnv, in service.NotifySendInput) error {
	return e.svc.Send(context.Background(), in)
}

func notifySMSInput(code string) service.NotifySendInput {
	return service.NotifySendInput{Code: code, To: "13800138000", Params: map[string]string{"code": "123456"}}
}

// 三个短信供应商 a、b、c（按创建顺序），挂在同一个模板 login_sms 下。
func threeSMSProviders(t *testing.T, e *notifyEnv, prioA, prioB, prioC int) (a, b, c uuid.UUID) {
	t.Helper()
	e.smsTemplate(t, "login_sms")
	a = e.provider(t, "fake_sms", "a")
	b = e.provider(t, "fake_sms", "b")
	c = e.provider(t, "fake_sms", "c")
	e.link(t, "login_sms", a, "SMS_A", prioA)
	e.link(t, "login_sms", b, "SMS_B", prioB)
	e.link(t, "login_sms", c, "SMS_C", prioC)
	return
}

func TestNotifySendPrefersHigherPriorityThenFollowsShuffleWithinGroup(t *testing.T) {
	e := newNotifyEnv(t, service.WithNotifyShuffle(reverseShuffle))
	threeSMSProviders(t, e, 10, 0, 0)

	// a 优先级最高，先试且成功，b、c 一次都不该被调用。
	if err := notifySend(e, notifySMSInput("login_sms")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if e.fake("a").callCount() != 1 || e.fake("b").callCount() != 0 || e.fake("c").callCount() != 0 {
		t.Fatalf("calls a/b/c = %d/%d/%d", e.fake("a").callCount(), e.fake("b").callCount(), e.fake("c").callCount())
	}

	// 指定优先的失败后同样降级到其他未禁用的；同优先级的 b、c 按洗牌结果（倒序）先 c 后 b。
	e.fake("a").setFail(errors.New("a down"))
	e.fake("c").setFail(errors.New("c down"))
	if err := notifySend(e, notifySMSInput("login_sms")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if e.fake("a").callCount() != 2 || e.fake("c").callCount() != 1 || e.fake("b").callCount() != 1 {
		t.Fatalf("calls a/b/c = %d/%d/%d, want 2/1/1（a 失败 → c 失败 → b 成功）",
			e.fake("a").callCount(), e.fake("b").callCount(), e.fake("c").callCount())
	}
}

func TestNotifySendFailsOverAndLogsEveryAttempt(t *testing.T) {
	e := newNotifyEnv(t, service.WithNotifyShuffle(func(int, func(i, j int)) {}))
	a, b, _ := threeSMSProviders(t, e, 3, 2, 1)
	e.fake("a").setFail(errors.New("a 余额不足"))

	in := notifySMSInput("login_sms")
	in.AppID = "app-1"
	if err := notifySend(e, in); err != nil {
		t.Fatalf("Send: %v", err)
	}
	logs, total, err := e.svc.ListLogs(context.Background(), service.NotifyLogFilter{Code: "login_sms"})
	if err != nil || total != 2 {
		t.Fatalf("logs = %+v, total = %d, err = %v", logs, total, err)
	}
	// 最新的在前：b 成功、a 失败。
	if !logs[0].Success || logs[0].ProviderID == nil || *logs[0].ProviderID != b {
		t.Fatalf("第二条应是 b 成功: %+v", logs[0])
	}
	if logs[1].Success || logs[1].ProviderID == nil || *logs[1].ProviderID != a || !strings.Contains(logs[1].Error, "余额不足") {
		t.Fatalf("第一条应是 a 失败并带错误文本: %+v", logs[1])
	}
	if logs[0].Provider != "fake_sms" || logs[0].Target != "13800138000" || logs[0].AppID != "app-1" || logs[0].Channel != "sms" {
		t.Fatalf("记录字段: %+v", logs[0])
	}
}

func TestNotifySendSkipsDisabledAtEveryLevel(t *testing.T) {
	e := newNotifyEnv(t, service.WithNotifyShuffle(func(int, func(i, j int)) {}))
	ctx := context.Background()
	a, b, c := threeSMSProviders(t, e, 3, 2, 1)

	// 关联级禁用 a、供应商级禁用 b：只剩 c。
	if err := e.svc.SetTemplateProvider(ctx, "login_sms", a, service.SetNotifyLinkInput{ProviderTemplateID: "SMS_A", Enabled: false, Priority: 3}); err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := e.svc.UpdateProvider(ctx, b, service.UpdateNotifyProviderInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if err := notifySend(e, notifySMSInput("login_sms")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if e.fake("a").callCount() != 0 || e.fake("b").callCount() != 0 || e.fake("c").callCount() != 1 {
		t.Fatalf("calls a/b/c = %d/%d/%d, want 0/0/1", e.fake("a").callCount(), e.fake("b").callCount(), e.fake("c").callCount())
	}

	// 全部禁用：没有可用的供应商。
	if _, err := e.svc.UpdateProvider(ctx, c, service.UpdateNotifyProviderInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if err := notifySend(e, notifySMSInput("login_sms")); !errors.Is(err, domain.ErrNotFound) || notifyErrCode(err) != domain.CodeNotifyProviderMissing {
		t.Fatalf("err = %v (code %q), want NOTIFY_PROVIDER_MISSING", err, notifyErrCode(err))
	}

	// 模板停用：直接拒绝。
	if _, err := e.svc.UpdateTemplate(ctx, "login_sms", service.UpdateNotifyTemplateInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if err := notifySend(e, notifySMSInput("login_sms")); !errors.Is(err, domain.ErrForbidden) || notifyErrCode(err) != domain.CodeNotifyTemplateDisabled {
		t.Fatalf("err = %v (code %q), want NOTIFY_TEMPLATE_DISABLED", err, notifyErrCode(err))
	}
}

// 全部失败只回通用错误：供应商侧的具体原因只进记录与日志，不回给调用方。
func TestNotifySendAllFailReturnsGenericError(t *testing.T) {
	e := newNotifyEnv(t)
	threeSMSProviders(t, e, 0, 0, 0)
	for _, n := range []string{"a", "b", "c"} {
		e.fake(n).setFail(errors.New("vendor said: account 4242 suspended"))
	}
	err := notifySend(e, notifySMSInput("login_sms"))
	if !errors.Is(err, domain.ErrInternal) || notifyErrCode(err) != domain.CodeNotifySendFailed {
		t.Fatalf("err = %v (code %q), want NOTIFY_SEND_FAILED / ErrInternal", err, notifyErrCode(err))
	}
	if strings.Contains(err.Error(), "4242") {
		t.Fatalf("供应商侧的信息不该回给调用方: %v", err)
	}
	if _, total, _ := e.svc.ListLogs(context.Background(), service.NotifyLogFilter{}); total != 3 {
		t.Fatalf("三家都试过，记录 = %d, want 3", total)
	}
}

// IM 与 webhook 一对一，失败不降级。应用层不允许挂第二个，这里绕过它直接插库，验证发送侧也不降级。
func TestNotifySendDoesNotFailOverForIMChannels(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	e.tgTemplate(t, "shipped")
	t1 := e.provider(t, "fake_tg", "t1")
	t2 := e.provider(t, "fake_tg", "t2")
	e.link(t, "shipped", t1, "", 0)
	if _, err := e.pool.Exec(ctx,
		`INSERT INTO notify_template_provider (code, provider_id) VALUES ('shipped', $1)`, t2); err != nil {
		t.Fatalf("插入第二条关联: %v", err)
	}
	e.fake("t1").setFail(errors.New("down"))
	e.fake("t2").setFail(errors.New("down"))

	if err := notifySend(e, service.NotifySendInput{Code: "shipped", Params: map[string]string{"id": "42"}}); notifyErrCode(err) != domain.CodeNotifySendFailed {
		t.Fatalf("err = %v", err)
	}
	if got := e.fake("t1").callCount() + e.fake("t2").callCount(); got != 1 {
		t.Fatalf("IM 渠道只该尝试一个供应商，实际尝试了 %d 个", got)
	}
}

func TestNotifySendValidatesInput(t *testing.T) {
	e := newNotifyEnv(t)
	threeSMSProviders(t, e, 0, 0, 0)
	e.tgTemplate(t, "shipped")
	tg := e.provider(t, "fake_tg", "t1")
	e.link(t, "shipped", tg, "", 0)

	var de *domain.Error
	err := notifySend(e, service.NotifySendInput{Code: "login_sms", To: "13800138000", Params: map[string]string{}})
	if !errors.As(err, &de) || de.Code != domain.CodeNotifyParamsInvalid || len(de.Detail["missing"].([]string)) != 1 {
		t.Errorf("缺变量: err = %v", err)
	}
	err = notifySend(e, service.NotifySendInput{Code: "login_sms", To: "13800138000", Params: map[string]string{"code": "1", "extra": "x"}})
	if !errors.As(err, &de) || de.Code != domain.CodeNotifyParamsInvalid || len(de.Detail["unexpected"].([]string)) != 1 {
		t.Errorf("多变量: err = %v", err)
	}
	if err := notifySend(e, service.NotifySendInput{Code: "login_sms", Params: map[string]string{"code": "1"}}); notifyErrCode(err) != domain.CodeNotifyRecipientInvalid {
		t.Errorf("短信缺收件人: err = %v", err)
	}
	if err := notifySend(e, service.NotifySendInput{Code: "shipped", To: "someone", Params: map[string]string{"id": "1"}}); notifyErrCode(err) != domain.CodeNotifyRecipientInvalid {
		t.Errorf("IM 不能指定收件人: err = %v", err)
	}
	if err := notifySend(e, service.NotifySendInput{Code: "nope", Params: nil}); !errors.Is(err, domain.ErrNotFound) || notifyErrCode(err) != domain.CodeNotifyTemplateNotFound {
		t.Errorf("未知 code: err = %v", err)
	}
	for _, n := range []string{"a", "b", "c", "t1"} {
		if e.fake(n).callCount() != 0 {
			t.Errorf("校验失败的请求不该到达供应商 %s", n)
		}
	}
}

func TestNotifySendDeliversTemplateIDParamsAndRenderedContent(t *testing.T) {
	e := newNotifyEnv(t)
	threeSMSProviders(t, e, 10, 0, 0)
	e.tgTemplate(t, "shipped")
	tg := e.provider(t, "fake_tg", "t1")
	e.link(t, "shipped", tg, "", 0)

	if err := notifySend(e, notifySMSInput("login_sms")); err != nil {
		t.Fatal(err)
	}
	d := e.fake("a").lastDelivery()
	if d.To != "13800138000" || d.ProviderTemplateID != "SMS_A" || d.Params["code"] != "123456" || d.Template.Code != "login_sms" {
		t.Fatalf("vendor 模式的 delivery = %+v", d)
	}
	if d.Rendered.Body != "" {
		t.Fatalf("vendor 模式由供应商渲染，fp 不该渲染: %q", d.Rendered.Body)
	}

	if err := notifySend(e, service.NotifySendInput{Code: "shipped", Params: map[string]string{"id": "42"}}); err != nil {
		t.Fatal(err)
	}
	d = e.fake("t1").lastDelivery()
	if d.To != "" || d.Rendered.Body != "订单 42 已发货" || d.ProviderTemplateID != "" {
		t.Fatalf("custom 模式的 delivery = %+v", d)
	}
}

func TestNotifySendIdempotency(t *testing.T) {
	e := newNotifyEnv(t)
	threeSMSProviders(t, e, 10, 0, 0)
	a := e.fake("a")
	in := notifySMSInput("login_sms")
	in.IdempotencyKey = "evt-1"

	// 相同 code + key：只真正发送一次，重复请求直接成功。
	if err := notifySend(e, in); err != nil {
		t.Fatal(err)
	}
	if err := notifySend(e, in); err != nil {
		t.Fatalf("重复请求应直接成功: %v", err)
	}
	if a.callCount() != 1 {
		t.Fatalf("calls = %d, want 1", a.callCount())
	}

	// 不同 key、没有 key：不去重。
	in2 := in
	in2.IdempotencyKey = "evt-2"
	_ = notifySend(e, in2)
	in3 := notifySMSInput("login_sms")
	_ = notifySend(e, in3)
	_ = notifySend(e, in3)
	if a.callCount() != 4 {
		t.Fatalf("calls = %d, want 4（evt-1 一次 + evt-2 一次 + 无 key 两次）", a.callCount())
	}
}

// 发送失败必须释放幂等键，否则 SDK 的重试永远被当成"已发送"而丢消息。
func TestNotifySendIdempotencyKeyIsReleasedOnFailure(t *testing.T) {
	e := newNotifyEnv(t)
	threeSMSProviders(t, e, 10, 0, 0)
	for _, n := range []string{"a", "b", "c"} {
		e.fake(n).setFail(errors.New("down"))
	}
	in := notifySMSInput("login_sms")
	in.IdempotencyKey = "evt-1"
	if err := notifySend(e, in); err == nil {
		t.Fatal("全部失败应报错")
	}

	for _, n := range []string{"a", "b", "c"} {
		e.fake(n).setFail(nil)
	}
	if err := notifySend(e, in); err != nil {
		t.Fatalf("供应商恢复后，相同幂等键的重试应真正发送: %v", err)
	}
	if e.fake("a").callCount() != 2 {
		t.Fatalf("a 的调用数 = %d, want 2（失败一次 + 重试成功一次）", e.fake("a").callCount())
	}
}

// 模板是全局共享的：两个应用拿同一个业务 ID 当幂等键，各自都得真正发送，不能互相把对方去重掉。
func TestNotifySendIdempotencyIsScopedPerApplication(t *testing.T) {
	e := newNotifyEnv(t)
	threeSMSProviders(t, e, 10, 0, 0)
	send := func(appID string) {
		t.Helper()
		in := notifySMSInput("login_sms")
		in.AppID, in.IdempotencyKey = appID, "order-42"
		if err := notifySend(e, in); err != nil {
			t.Fatalf("%s: %v", appID, err)
		}
	}

	send("app-a")
	send("app-b")
	if got := e.fake("a").callCount(); got != 2 {
		t.Fatalf("两个应用用同一个键各发一次，calls = %d, want 2", got)
	}
	// 各自重复一次：仍在自己的键下去重。
	send("app-a")
	send("app-b")
	if got := e.fake("a").callCount(); got != 2 {
		t.Fatalf("各自重复之后 calls = %d, want 仍是 2", got)
	}
}

func TestNotifySendIdempotencyInProgress(t *testing.T) {
	e := newNotifyEnv(t)
	threeSMSProviders(t, e, 0, 0, 0)
	// 模拟"另一个请求正在处理同一个幂等键"。
	if err := e.rdb.Set(context.Background(), idemRedisKey, "1", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	in := notifySMSInput("login_sms")
	in.IdempotencyKey = "evt-1"
	err := notifySend(e, in)
	if !errors.Is(err, domain.ErrConflict) || notifyErrCode(err) != domain.CodeNotifyInProgress {
		t.Fatalf("err = %v (code %q), want NOTIFY_IN_PROGRESS", err, notifyErrCode(err))
	}
	for _, n := range []string{"a", "b", "c"} {
		if e.fake(n).callCount() != 0 {
			t.Fatalf("处理中的请求不该再发送")
		}
	}
}

// 发送记录里绝不能有变量取值（验证码一类的敏感值）和渲染后的内容。
func TestNotifySendLogsNeverContainParamsOrRenderedContent(t *testing.T) {
	e := newNotifyEnv(t)
	threeSMSProviders(t, e, 10, 0, 0)
	e.tgTemplate(t, "shipped")
	tg := e.provider(t, "fake_tg", "t1")
	e.link(t, "shipped", tg, "", 0)

	in := notifySMSInput("login_sms")
	in.Params = map[string]string{"code": "987654"}
	if err := notifySend(e, in); err != nil {
		t.Fatal(err)
	}
	if err := notifySend(e, service.NotifySendInput{Code: "shipped", Params: map[string]string{"id": "SECRET-ORDER-77"}}); err != nil {
		t.Fatal(err)
	}
	var leaked int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM notify_log WHERE row_to_json(notify_log)::text ~ '987654|SECRET-ORDER-77|已发货'`).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatalf("%d 条发送记录里出现了变量取值或渲染内容", leaked)
	}
}

// 供应商按 id@updated_at 缓存：改了配置，下一次发送必须用新配置构造的供应商。
func TestNotifySendUsesNewProviderAfterConfigUpdate(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	e.smsTemplate(t, "login_sms")
	pid := e.provider(t, "fake_sms", "old")
	e.link(t, "login_sms", pid, "SMS_1", 0)

	if err := notifySend(e, notifySMSInput("login_sms")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.UpdateProvider(ctx, pid, service.UpdateNotifyProviderInput{Config: map[string]any{"name": "new"}}); err != nil {
		t.Fatal(err)
	}
	if err := notifySend(e, notifySMSInput("login_sms")); err != nil {
		t.Fatal(err)
	}
	if e.fake("old").callCount() != 1 || e.fake("new").callCount() != 1 {
		t.Fatalf("calls old/new = %d/%d, want 1/1", e.fake("old").callCount(), e.fake("new").callCount())
	}
}

// ---------------------------------------------------------------------------
// 幂等键的续期与归属
// ---------------------------------------------------------------------------

// 幂等键 evt-1（模板 login_sms，AppID 为空）在 Redis 里的键名。
const idemRedisKey = "fp:notify:idem::login_sms:evt-1"

// idemValue 读幂等键当前的值；键不存在时是空串。
func idemValue(e *notifyEnv) string {
	return e.rdb.Get(context.Background(), idemRedisKey).Val()
}

// gate 把假供应商的一次调用卡住：调用进入时关闭 entered，等 open 之后才放行。
type gate struct {
	entered chan struct{}
	opened  chan struct{}
	once    sync.Once
}

func newGate() *gate {
	return &gate{entered: make(chan struct{}), opened: make(chan struct{})}
}

// hold 在供应商的调用里执行：宣告已进入，然后等放行。
func (g *gate) hold() {
	close(g.entered)
	<-g.opened
}

// open 放行被卡住的调用，可重复调用。
func (g *gate) open() { g.once.Do(func() { close(g.opened) }) }

// asyncSend 是后台发起的一次发送。
type asyncSend struct {
	done chan struct{}
	err  error
}

// sendAsync 在后台发送。测试结束时（包括失败退出）会调 release 放行它并等它收尾：
// 还在跑的后台请求之后写进库与 Redis 的东西，会污染下一个测试。
func sendAsync(t *testing.T, e *notifyEnv, in service.NotifySendInput, release func()) *asyncSend {
	t.Helper()
	a := &asyncSend{done: make(chan struct{})}
	go func() {
		defer close(a.done)
		a.err = notifySend(e, in)
	}()
	t.Cleanup(func() {
		release()
		select {
		case <-a.done:
		case <-time.After(10 * time.Second):
			t.Error("后台发送没有收尾")
		}
	})
	return a
}

// wait 等后台发送返回，带超时：被测代码出问题时测试该失败，不是挂死。
func (a *asyncSend) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-a.done:
		return a.err
	case <-time.After(5 * time.Second):
		t.Fatal("等待后台发送返回超时")
		return nil
	}
}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("等待%s超时", what)
	}
}

// 逐家降级的总耗时可以超过"处理中"的 TTL（每家最多 30 秒）。每次尝试前都要把它续满，
// 否则同键的重试会在第一个请求还在降级时抢到键，造成重复发送。
func TestNotifySendRenewsIdempotencyKeyBeforeEachAttempt(t *testing.T) {
	e := newNotifyEnv(t, service.WithNotifyShuffle(func(int, func(i, j int)) {}))
	e.smsTemplate(t, "login_sms")
	a := e.provider(t, "fake_sms", "a")
	b := e.provider(t, "fake_sms", "b")
	e.link(t, "login_sms", a, "SMS_A", 10)
	e.link(t, "login_sms", b, "SMS_B", 0)

	// a 拖 2 秒后失败；b 成功，并在发送途中读幂等键的剩余 TTL。
	e.fake("a").setHook(func(context.Context, int) error {
		time.Sleep(2 * time.Second)
		return errors.New("a down")
	})
	var ttl time.Duration
	e.fake("b").setHook(func(ctx context.Context, _ int) error {
		var err error
		ttl, err = e.rdb.PTTL(ctx, idemRedisKey).Result()
		return err
	})

	in := notifySMSInput("login_sms")
	in.IdempotencyKey = "evt-1"
	if err := notifySend(e, in); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// 占位时设的是 60 秒；没有续期的话，a 耗掉 2 秒之后只剩不到 58 秒。
	if ttl <= 59*time.Second {
		t.Fatalf("b 发送时幂等键的剩余 TTL = %v, want > 59s（每次尝试前都该续满）", ttl)
	}
}

// 慢请求 A 的"处理中"标记过期、同键的 B 重新占位并发送成功之后，A 才失败收尾：
// A 的续期与收尾都只能动自己的标记。它既不能把 B 记下的"已完成"删掉，也不能给它续成 60 秒——
// 否则同键的重试会被放行（或 24 小时的去重窗口缩成 60 秒），造成重复发送。
func TestNotifySendStaleFailureDoesNotTouchNewerRequestsDoneMark(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	e.smsTemplate(t, "login_sms")
	a := e.provider(t, "fake_sms", "a")
	b := e.provider(t, "fake_sms", "b")
	e.link(t, "login_sms", a, "SMS_A", 10)
	e.link(t, "login_sms", b, "SMS_B", 0)
	in := notifySMSInput("login_sms")
	in.IdempotencyKey = "evt-1"

	// A 先卡在 a 里再失败，然后降级到同样失败的 b；B 的 a 直接成功。
	gA := newGate()
	e.fake("a").setHook(func(_ context.Context, n int) error {
		if n > 1 {
			return nil
		}
		gA.hold()
		return errors.New("a down")
	})
	e.fake("b").setFail(errors.New("b down"))
	reqA := sendAsync(t, e, in, gA.open)
	waitClosed(t, gA.entered, "A 进入供应商")

	// 模拟 A 的"处理中"标记过期：同键的 B 重新占位、发送成功、记下"已完成"。
	if err := e.rdb.Del(ctx, idemRedisKey).Err(); err != nil {
		t.Fatal(err)
	}
	if err := notifySend(e, in); err != nil {
		t.Fatalf("B: %v", err)
	}
	if got := idemValue(e); got != "2" {
		t.Fatalf("B 成功后键 = %q, want 2", got)
	}

	gA.open()
	if err := reqA.wait(t); notifyErrCode(err) != domain.CodeNotifySendFailed {
		t.Fatalf("A: err = %v, want NOTIFY_SEND_FAILED", err)
	}
	if got := e.fake("b").callCount(); got != 1 {
		t.Fatalf("b 的调用数 = %d, want 1（A 降级到了 b，所以续过一次期）", got)
	}
	if got := idemValue(e); got != "2" {
		t.Fatalf("A 失败收尾后键 = %q, want 仍是 B 记下的 2（A 不该删掉别人的标记）", got)
	}
	if ttl := e.rdb.PTTL(ctx, idemRedisKey).Val(); ttl < 23*time.Hour {
		t.Fatalf("A 收尾后 B 的\"已完成\"剩余 TTL = %v, want 约 24 小时（A 不该给别人的标记续期）", ttl)
	}
	// 同键的重试仍被当成已发送，不会再到供应商。
	if err := notifySend(e, in); err != nil {
		t.Fatalf("重试: %v", err)
	}
	if got := e.fake("a").callCount(); got != 2 {
		t.Fatalf("a 的调用数 = %d, want 2（A 与 B 各一次，重试不再发送）", got)
	}
}

// 慢请求 A 的标记过期、同键的 B 重新占位还在发送时，A 成功了：消息已经送达，A 必须照样记下"已完成"，
// 盖掉 B 的"处理中"。否则 B 随后失败、删掉键，同键的第三个请求就会把 A 已送达的消息再发一遍。
func TestNotifySendStaleSuccessRecordsDoneOverNewerRequestsProcessingMark(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	e.smsTemplate(t, "login_sms")
	e.link(t, "login_sms", e.provider(t, "fake_sms", "a"), "SMS_A", 0)
	in := notifySMSInput("login_sms")
	in.IdempotencyKey = "evt-1"

	// 第 1 次调用是 A：卡住后成功；第 2 次是 B：卡住后失败。
	gA, gB := newGate(), newGate()
	e.fake("a").setHook(func(_ context.Context, n int) error {
		switch n {
		case 1:
			gA.hold()
		case 2:
			gB.hold()
			return errors.New("a down")
		}
		return nil
	})
	a := sendAsync(t, e, in, gA.open)
	waitClosed(t, gA.entered, "A 进入供应商")

	// 模拟 A 的"处理中"标记过期：B 重新占位，并卡在供应商里。
	if err := e.rdb.Del(ctx, idemRedisKey).Err(); err != nil {
		t.Fatal(err)
	}
	b := sendAsync(t, e, in, gB.open)
	waitClosed(t, gB.entered, "B 进入供应商")
	if processing := idemValue(e); processing == "" || processing == "2" {
		t.Fatalf("B 在发送途中，键应是它的处理中标记，实际 %q", processing)
	}

	gA.open()
	if err := a.wait(t); err != nil {
		t.Fatalf("A: %v", err)
	}
	if got := idemValue(e); got != "2" {
		t.Fatalf("A 成功收尾后键 = %q, want 2（已送达就是已完成）", got)
	}

	// B 失败收尾时键已不是它的标记：不能删。
	gB.open()
	if err := b.wait(t); notifyErrCode(err) != domain.CodeNotifySendFailed {
		t.Fatalf("B: err = %v, want NOTIFY_SEND_FAILED", err)
	}
	if got := idemValue(e); got != "2" {
		t.Fatalf("B 失败收尾后键 = %q, want 仍是 2", got)
	}
	if err := notifySend(e, in); err != nil {
		t.Fatalf("第三个同键请求: %v", err)
	}
	if got := e.fake("a").callCount(); got != 2 {
		t.Fatalf("a 的调用数 = %d, want 2（A 与 B 各一次，第三个请求不再发送）", got)
	}
}

// 慢请求的"处理中"标记过期、也没有别的请求接手：它成功收尾时仍要记下"已完成"，
// 否则同键的重试会把已经发出去的消息再发一遍。
func TestNotifySendRecordsDoneWhenProcessingMarkExpiredMeanwhile(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	e.smsTemplate(t, "login_sms")
	e.link(t, "login_sms", e.provider(t, "fake_sms", "a"), "SMS_A", 0)
	in := notifySMSInput("login_sms")
	in.IdempotencyKey = "evt-1"

	g := newGate()
	e.fake("a").setHook(func(_ context.Context, n int) error {
		if n == 1 {
			g.hold()
		}
		return nil
	})
	a := sendAsync(t, e, in, g.open)
	waitClosed(t, g.entered, "请求进入供应商")
	if err := e.rdb.Del(ctx, idemRedisKey).Err(); err != nil {
		t.Fatal(err)
	}

	g.open()
	if err := a.wait(t); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := idemValue(e); got != "2" {
		t.Fatalf("成功收尾后键 = %q, want 2", got)
	}
	if err := notifySend(e, in); err != nil {
		t.Fatalf("重试: %v", err)
	}
	if got := e.fake("a").callCount(); got != 1 {
		t.Fatalf("a 的调用数 = %d, want 1（重试不再发送）", got)
	}
}

// ---------------------------------------------------------------------------
// 调用方的截止时间与取消
// ---------------------------------------------------------------------------

// a 优先、b 其次，两家都挂在 login_sms 下；洗牌不打乱顺序。
func twoSMSProvidersAThenB(t *testing.T) *notifyEnv {
	t.Helper()
	e := newNotifyEnv(t, service.WithNotifyShuffle(func(int, func(i, j int)) {}))
	e.smsTemplate(t, "login_sms")
	e.link(t, "login_sms", e.provider(t, "fake_sms", "a"), "SMS_A", 10)
	e.link(t, "login_sms", e.provider(t, "fake_sms", "b"), "SMS_B", 0)
	return e
}

// 调用方的截止时间要平分给还没试的每一家：优先的那家挂住时只能吃掉自己那一份，降级才轮得到下一家。
// 否则 SDK 的截止时间随 gRPC 传过来，第一家挂到截止，第二家拿到的是已经过期的 ctx。
func TestNotifySendSplitsCallerDeadlineAcrossCandidates(t *testing.T) {
	e := twoSMSProvidersAThenB(t)
	// a 挂到这次尝试的截止时间为止；b 像真的供应商一样，拿到已经过期的 ctx 就立刻失败。
	e.fake("a").setHook(func(ctx context.Context, _ int) error {
		<-ctx.Done()
		return ctx.Err()
	})
	e.fake("b").setHook(func(ctx context.Context, _ int) error { return ctx.Err() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := e.svc.Send(ctx, notifySMSInput("login_sms")); err != nil {
		t.Fatalf("Send: %v（a 挂住时应降级到 b）", err)
	}
	if got := e.fake("b").callCount(); got != 1 {
		t.Fatalf("b 的调用数 = %d, want 1", got)
	}
	logs, _, err := e.svc.ListLogs(context.Background(), service.NotifyLogFilter{Code: "login_sms"})
	if err != nil || len(logs) != 2 {
		t.Fatalf("logs = %+v, err = %v", logs, err)
	}
	// 最新的在前：b 成功、a 超时失败。
	if !logs[0].Success || logs[1].Success || !strings.Contains(logs[1].Error, "deadline") {
		t.Fatalf("应是 a 超时失败、b 成功: %+v", logs)
	}
}

// 调用方放弃之后剩下的供应商不再尝试：每家都只会立刻失败、白写一行记录；
// 阿里云这类不认 ctx 的，甚至会在调用方已经收到错误之后真把短信发出去。
func TestNotifySendStopsFailingOverOnceCallerGivesUp(t *testing.T) {
	e := twoSMSProvidersAThenB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.fake("a").setHook(func(context.Context, int) error {
		cancel() // 调用方在 a 发送途中断开
		return errors.New("a down")
	})

	in := notifySMSInput("login_sms")
	in.IdempotencyKey = "evt-1"
	if err := e.svc.Send(ctx, in); notifyErrCode(err) != domain.CodeNotifySendFailed {
		t.Fatalf("err = %v, want NOTIFY_SEND_FAILED", err)
	}
	if got := e.fake("b").callCount(); got != 0 {
		t.Fatalf("调用方已放弃，b 不该再被尝试: calls = %d", got)
	}
	if _, total, _ := e.svc.ListLogs(context.Background(), service.NotifyLogFilter{}); total != 1 {
		t.Fatalf("发送记录 = %d 条, want 1（只有 a 的那一行）", total)
	}
	if got := idemValue(e); got != "" {
		t.Fatalf("按失败收尾，幂等键应已释放, got %q", got)
	}
}

// 调用方在供应商已经发出之后才断开：收尾与记录都脱离了调用方的取消，"已完成"与成功记录照样要落下。
// 否则同键的重试会把已发出的消息再发一遍，控制台也看不到这次发送。
func TestNotifySendSettlesAndLogsAfterCallerCancels(t *testing.T) {
	e := newNotifyEnv(t)
	e.smsTemplate(t, "login_sms")
	e.link(t, "login_sms", e.provider(t, "fake_sms", "a"), "SMS_A", 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.fake("a").setHook(func(context.Context, int) error {
		cancel()
		return nil
	})

	in := notifySMSInput("login_sms")
	in.IdempotencyKey = "evt-1"
	if err := e.svc.Send(ctx, in); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := idemValue(e); got != "2" {
		t.Fatalf("幂等键 = %q, want 2", got)
	}
	logs, total, err := e.svc.ListLogs(context.Background(), service.NotifyLogFilter{Code: "login_sms"})
	if err != nil || total != 1 || !logs[0].Success {
		t.Fatalf("logs = %+v, total = %d, err = %v, want 一行成功记录", logs, total, err)
	}
}

// ---------------------------------------------------------------------------
// 入参上限与校验顺序
// ---------------------------------------------------------------------------

func TestNotifySendCapsIdempotencyKeyAndRecipientLength(t *testing.T) {
	e := newNotifyEnv(t)
	threeSMSProviders(t, e, 10, 0, 0)

	in := notifySMSInput("login_sms")
	in.IdempotencyKey = strings.Repeat("k", 129)
	if err := notifySend(e, in); !errors.Is(err, domain.ErrInvalidArgument) || notifyErrCode(err) != domain.CodeInvalidArgument {
		t.Errorf("129 字节的幂等键: err = %v (code %q), want INVALID_ARGUMENT", err, notifyErrCode(err))
	}
	in = notifySMSInput("login_sms")
	in.To = strings.Repeat("1", 255)
	if err := notifySend(e, in); !errors.Is(err, domain.ErrInvalidArgument) || notifyErrCode(err) != domain.CodeNotifyRecipientInvalid {
		t.Errorf("255 字节的收件人: err = %v (code %q), want NOTIFY_RECIPIENT_INVALID", err, notifyErrCode(err))
	}
	if got := e.fake("a").callCount(); got != 0 {
		t.Fatalf("超长的请求不该到达供应商: calls = %d", got)
	}

	// 边界上的值照常发送。
	in = notifySMSInput("login_sms")
	in.IdempotencyKey, in.To = strings.Repeat("k", 128), strings.Repeat("1", 254)
	if err := notifySend(e, in); err != nil {
		t.Fatalf("128 字节的键、254 字节的收件人应能发送: %v", err)
	}
}

// 校验失败的请求从不占键：它若留下"处理中"，同一个键改正之后的请求会被挡成 NOTIFY_IN_PROGRESS。
func TestNotifySendValidationFailuresNeverHoldTheIdempotencyKey(t *testing.T) {
	e := newNotifyEnv(t)
	threeSMSProviders(t, e, 10, 0, 0)

	bad := notifySMSInput("login_sms")
	bad.IdempotencyKey, bad.Params = "evt-1", map[string]string{"wrong": "1"}
	if err := notifySend(e, bad); notifyErrCode(err) != domain.CodeNotifyParamsInvalid {
		t.Fatalf("变量不匹配: err = %v", err)
	}
	bad = notifySMSInput("login_sms")
	bad.IdempotencyKey, bad.To = "evt-1", strings.Repeat("1", 255)
	if err := notifySend(e, bad); notifyErrCode(err) != domain.CodeNotifyRecipientInvalid {
		t.Fatalf("收件人超长: err = %v", err)
	}

	good := notifySMSInput("login_sms")
	good.IdempotencyKey = "evt-1"
	if err := notifySend(e, good); err != nil {
		t.Fatalf("改正之后的同键请求应真正发送: %v", err)
	}
	if got := e.fake("a").callCount(); got != 1 {
		t.Fatalf("a 的调用数 = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// DevOnly 的供应商类型在 prod 环境
// ---------------------------------------------------------------------------

// 已经存进库里的 DevOnly 实例（比如开发环境留下的 log）在 prod 环境不能再被关联。
func TestNotifySetTemplateProviderRefusesDevOnlyTypeInProd(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	e.smsTemplate(t, "login_sms")
	// 只有非 prod 的服务建得出 DevOnly 实例；这里用它模拟 prod 库里已有的历史数据。
	dev := e.provider(t, "fake_dev", "dev")
	in := service.SetNotifyLinkInput{ProviderTemplateID: "SMS_D", Enabled: true}

	prod := e.service(service.WithNotifyProd(true))
	err := prod.SetTemplateProvider(ctx, "login_sms", dev, in)
	if !errors.Is(err, domain.ErrInvalidArgument) || notifyErrCode(err) != domain.CodeNotifyLinkInvalid {
		t.Fatalf("err = %v (code %q), want NOTIFY_LINK_INVALID", err, notifyErrCode(err))
	}
	d, err := e.svc.GetTemplate(ctx, "login_sms")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Links) != 0 {
		t.Fatalf("被拒绝的关联不该落库: %+v", d.Links)
	}

	// 对照：同一份数据，非 prod 的服务照常关联——上面被拒绝只因为 prod，不是别的校验。
	if err := e.svc.SetTemplateProvider(ctx, "login_sms", dev, in); err != nil {
		t.Fatalf("非 prod 应能关联: %v", err)
	}
}

// DevOnly 实例可能已经躺在 prod 库里（开发环境建的，或手工插进去的）：prod 的发送路径必须当它不存在。
// 它会把变量取值打进日志，还会"发送成功"而挡住真正的降级。
func TestNotifySendNeverUsesDevOnlyProvidersInProd(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	e.smsTemplate(t, "login_sms")
	dev := e.provider(t, "fake_dev", "dev")
	e.link(t, "login_sms", dev, "SMS_D", 10)
	prod := e.service(service.WithNotifyProd(true))
	send := func(svc *service.NotifyService) error { return svc.Send(ctx, notifySMSInput("login_sms")) }

	// 只有 DevOnly 实例可用：失败，只回通用错误，供应商一次也没被调用。
	if err := send(prod); notifyErrCode(err) != domain.CodeNotifySendFailed {
		t.Fatalf("err = %v (code %q), want NOTIFY_SEND_FAILED", err, notifyErrCode(err))
	}
	if got := e.fake("dev").callCount(); got != 0 {
		t.Fatalf("prod 调用了 DevOnly 供应商 %d 次", got)
	}

	// 还有别的可用实例时，DevOnly 那次只算一次失败的尝试，照常降级。
	e.link(t, "login_sms", e.provider(t, "fake_sms", "real"), "SMS_R", 0)
	if err := send(prod); err != nil {
		t.Fatalf("prod 应降级到 real: %v", err)
	}
	if e.fake("dev").callCount() != 0 || e.fake("real").callCount() != 1 {
		t.Fatalf("calls dev/real = %d/%d, want 0/1", e.fake("dev").callCount(), e.fake("real").callCount())
	}

	// 对照：同样的数据在非 prod 的服务里照常使用 DevOnly 实例（它的优先级最高）。
	if err := send(e.svc); err != nil {
		t.Fatalf("非 prod: %v", err)
	}
	if e.fake("dev").callCount() != 1 || e.fake("real").callCount() != 1 {
		t.Fatalf("calls dev/real = %d/%d, want 1/1", e.fake("dev").callCount(), e.fake("real").callCount())
	}
}
