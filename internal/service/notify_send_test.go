package service_test

import (
	"context"
	"errors"
	"strings"
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

func TestNotifySendIdempotencyInProgress(t *testing.T) {
	e := newNotifyEnv(t)
	threeSMSProviders(t, e, 0, 0, 0)
	// 模拟"另一个请求正在处理同一个幂等键"。
	if err := e.rdb.Set(context.Background(), "fp:notify:idem:login_sms:evt-1", "1", time.Minute).Err(); err != nil {
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
