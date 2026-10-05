package service_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/notify"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

// fakeProvider 记录收到的每一次发送，并可以被设成失败。
type fakeProvider struct {
	mu    sync.Mutex
	calls []notify.Delivery
	fail  error
	// hook 非空时在记录调用之后执行（不持有 mu），参数是本次调用的序号（从 1 起），返回值取代 fail。
	// 并发测试靠它把某一次调用卡在供应商里，或在发送途中观察外部状态。
	hook func(ctx context.Context, n int) error
}

func (f *fakeProvider) Send(ctx context.Context, d notify.Delivery) error {
	f.mu.Lock()
	f.calls = append(f.calls, d)
	n, fail, hook := len(f.calls), f.fail, f.hook
	f.mu.Unlock()
	if hook != nil {
		return hook(ctx, n)
	}
	return fail
}

func (f *fakeProvider) setHook(h func(ctx context.Context, n int) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hook = h
}

func (f *fakeProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeProvider) setFail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = err
}

func (f *fakeProvider) lastDelivery() notify.Delivery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

type notifyEnv struct {
	svc  *service.NotifyService
	pool *pgxpool.Pool
	rdb  *redis.Client
	reg  *notify.Registry

	mu    sync.Mutex
	fakes map[string]*fakeProvider // 按实例配置里的 name 取
}

// service 在同一份数据（库、Redis、类型注册表）上再起一个 NotifyService，
// 用来对比不同选项（比如是否 prod）下同样的数据会有什么不同的表现。
// 不能为此再调 newNotifyEnv：它会清空库。
func (e *notifyEnv) service(opts ...service.NotifyOption) *service.NotifyService {
	return service.NewNotifyService(e.pool, e.rdb, e.reg, opts...)
}

// fake 取（或创建）名为 name 的假供应商。构造函数每次都返回同一个对象，
// 测试因此能在供应商被缓存或重建之后依然数到调用次数。
func (e *notifyEnv) fake(name string) *fakeProvider {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fakes[name] == nil {
		e.fakes[name] = &fakeProvider{}
	}
	return e.fakes[name]
}

func newNotifyEnv(t *testing.T, opts ...service.NotifyOption) *notifyEnv {
	t.Helper()
	e := &notifyEnv{pool: testsupport.NewTestDB(t), rdb: testsupport.NewTestRedis(t), fakes: map[string]*fakeProvider{}}
	reg := notify.NewRegistry()
	e.reg = reg
	schema := []domain.Field{
		{Key: "name", Label: "名称", Type: domain.FieldTypeString, Required: true},
		{Key: "token", Label: "令牌", Type: domain.FieldTypeSecret},
	}
	newFake := func(cfg notify.Config) (notify.Provider, error) { return e.fake(cfg.String("name")), nil }
	for _, s := range []notify.TypeSpec{
		{Type: "fake_sms", Channel: domain.NotifyChannelSMS, ConfigSchema: schema, New: newFake},
		{Type: "fake_tg", Channel: domain.NotifyChannelTelegram, ConfigSchema: schema, New: newFake},
		{Type: "fake_dev", Channel: domain.NotifyChannelSMS, ConfigSchema: schema, DevOnly: true, New: newFake},
	} {
		if err := reg.Register(s); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}
	e.svc = service.NewNotifyService(e.pool, e.rdb, reg, opts...)
	return e
}

func notifyErrCode(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func (e *notifyEnv) provider(t *testing.T, typ, name string) uuid.UUID {
	t.Helper()
	p, err := e.svc.CreateProvider(context.Background(), service.CreateNotifyProviderInput{
		Type: typ, Description: name, Enabled: true, Config: map[string]any{"name": name},
	})
	if err != nil {
		t.Fatalf("CreateProvider(%s): %v", name, err)
	}
	return p.ID
}

// smsTemplate 建一个 vendor 模式的短信模板，变量是 code。
func (e *notifyEnv) smsTemplate(t *testing.T, code string) {
	t.Helper()
	_, err := e.svc.CreateTemplate(context.Background(), service.CreateNotifyTemplateInput{
		Code: code, Channel: domain.NotifyChannelSMS, Mode: domain.NotifyModeVendor, Enabled: true,
		Content: domain.NotifyContent{Content: "您的验证码是${code}", Variables: []string{"code"}},
	})
	if err != nil {
		t.Fatalf("CreateTemplate(%s): %v", code, err)
	}
}

// tgTemplate 建一个 custom 模式的 telegram 模板，变量是 id。
func (e *notifyEnv) tgTemplate(t *testing.T, code string) {
	t.Helper()
	_, err := e.svc.CreateTemplate(context.Background(), service.CreateNotifyTemplateInput{
		Code: code, Channel: domain.NotifyChannelTelegram, Mode: domain.NotifyModeCustom, Enabled: true,
		Content: domain.NotifyContent{Content: "订单 {id} 已发货", Variables: []string{"id"}},
	})
	if err != nil {
		t.Fatalf("CreateTemplate(%s): %v", code, err)
	}
}

func (e *notifyEnv) link(t *testing.T, code string, pid uuid.UUID, providerTemplateID string, priority int) {
	t.Helper()
	err := e.svc.SetTemplateProvider(context.Background(), code, pid, service.SetNotifyLinkInput{
		ProviderTemplateID: providerTemplateID, Enabled: true, Priority: priority,
	})
	if err != nil {
		t.Fatalf("SetTemplateProvider(%s): %v", code, err)
	}
}

func TestNotifyCreateProviderValidatesTypeAndConfig(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()

	cases := []struct {
		name string
		in   service.CreateNotifyProviderInput
	}{
		{"未知类型", service.CreateNotifyProviderInput{Type: "nope", Config: map[string]any{"name": "a"}}},
		{"缺必填项", service.CreateNotifyProviderInput{Type: "fake_sms", Config: map[string]any{}}},
		{"未知配置项", service.CreateNotifyProviderInput{Type: "fake_sms", Config: map[string]any{"name": "a", "typo": "x"}}},
	}
	for _, c := range cases {
		_, err := e.svc.CreateProvider(ctx, c.in)
		if !errors.Is(err, domain.ErrInvalidArgument) || notifyErrCode(err) != domain.CodeNotifyProviderInvalid {
			t.Errorf("%s: err = %v (code %q)", c.name, err, notifyErrCode(err))
		}
	}
	if _, err := e.svc.CreateProvider(ctx, service.CreateNotifyProviderInput{Type: "fake_sms", Config: map[string]any{}}); err != nil {
		var de *domain.Error
		if !errors.As(err, &de) || de.Detail["field"] != "name" {
			t.Errorf("detail.field 应指出出问题的配置项: %v", err)
		}
	}
}

func TestNotifyDevOnlyProviderTypesAreHiddenInProd(t *testing.T) {
	dev := newNotifyEnv(t)
	if _, err := dev.svc.CreateProvider(context.Background(), service.CreateNotifyProviderInput{Type: "fake_dev", Config: map[string]any{"name": "d"}}); err != nil {
		t.Fatalf("非 prod 应能建 DevOnly 类型: %v", err)
	}

	prod := newNotifyEnv(t, service.WithNotifyProd(true))
	for _, ty := range prod.svc.ProviderTypes() {
		if ty.Type == "fake_dev" {
			t.Fatal("prod 环境不该暴露 DevOnly 类型")
		}
	}
	_, err := prod.svc.CreateProvider(context.Background(), service.CreateNotifyProviderInput{Type: "fake_dev", Config: map[string]any{"name": "d"}})
	if notifyErrCode(err) != domain.CodeNotifyProviderInvalid {
		t.Fatalf("prod 不可建 DevOnly 类型, err = %v", err)
	}
}

func TestNotifyProviderSecretsAreMaskedAndPreservedOnUpdate(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	p, err := e.svc.CreateProvider(ctx, service.CreateNotifyProviderInput{
		Type: "fake_sms", Enabled: true, Config: map[string]any{"name": "a", "token": "tok-1"},
	})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if got := e.svc.MaskConfig(*p)["token"]; got != domain.SecretMask {
		t.Fatalf("控制台读到的 secret = %v, want 掩码", got)
	}
	if e.svc.MaskConfig(*p)["name"] != "a" {
		t.Fatal("非 secret 字段应原样返回")
	}

	// 把掩码原样传回来：保持原值，其他字段照常修改。
	up, err := e.svc.UpdateProvider(ctx, p.ID, service.UpdateNotifyProviderInput{
		Config: map[string]any{"name": "a2", "token": domain.SecretMask},
	})
	if err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	if up.Config["token"] != "tok-1" || up.Config["name"] != "a2" {
		t.Fatalf("config = %v", up.Config)
	}
	// 传新值：覆盖。
	up, err = e.svc.UpdateProvider(ctx, p.ID, service.UpdateNotifyProviderInput{
		Config: map[string]any{"name": "a2", "token": "tok-2"},
	})
	if err != nil || up.Config["token"] != "tok-2" {
		t.Fatalf("up = %+v, err = %v", up, err)
	}
	// Config 为 nil：只改备注与启停，配置原样。
	desc, off := "备注", false
	up, err = e.svc.UpdateProvider(ctx, p.ID, service.UpdateNotifyProviderInput{Description: &desc, Enabled: &off})
	if err != nil || up.Description != "备注" || up.Enabled || up.Config["token"] != "tok-2" {
		t.Fatalf("up = %+v, err = %v", up, err)
	}
}

func TestNotifyDeleteProviderRefusesWhileReferenced(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	pid := e.provider(t, "fake_sms", "a")
	e.smsTemplate(t, "login_sms")
	e.link(t, "login_sms", pid, "SMS_1", 0)

	err := e.svc.DeleteProvider(ctx, pid)
	if !errors.Is(err, domain.ErrConflict) || notifyErrCode(err) != domain.CodeNotifyProviderInUse {
		t.Fatalf("err = %v (code %q), want NOTIFY_PROVIDER_IN_USE", err, notifyErrCode(err))
	}
	if err := e.svc.RemoveTemplateProvider(ctx, "login_sms", pid); err != nil {
		t.Fatalf("RemoveTemplateProvider: %v", err)
	}
	if err := e.svc.DeleteProvider(ctx, pid); err != nil {
		t.Fatalf("解除关联后应能删除: %v", err)
	}
	if err := e.svc.DeleteProvider(ctx, pid); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("重复删除 err = %v, want ErrNotFound", err)
	}
}

func TestNotifyCreateTemplateValidatesCodeAndContent(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	good := domain.NotifyContent{Content: "x", Variables: []string{}}

	for _, code := range []string{"", "中文", "-x", "a b", strings.Repeat("a", 65)} {
		_, err := e.svc.CreateTemplate(ctx, service.CreateNotifyTemplateInput{
			Code: code, Channel: domain.NotifyChannelSMS, Mode: domain.NotifyModeVendor, Content: good,
		})
		if notifyErrCode(err) != domain.CodeNotifyTemplateInvalid {
			t.Errorf("code %q: err = %v, want NOTIFY_TEMPLATE_INVALID", code, err)
		}
	}
	_, err := e.svc.CreateTemplate(ctx, service.CreateNotifyTemplateInput{
		Code: "ok.code-1_x", Channel: domain.NotifyChannelSMS, Mode: domain.NotifyModeCustom, Content: good,
	})
	if notifyErrCode(err) != domain.CodeNotifyTemplateInvalid {
		t.Errorf("短信不支持 custom, err = %v", err)
	}

	e.smsTemplate(t, "login_sms")
	_, err = e.svc.CreateTemplate(ctx, service.CreateNotifyTemplateInput{
		Code: "login_sms", Channel: domain.NotifyChannelSMS, Mode: domain.NotifyModeVendor, Content: good,
	})
	if !errors.Is(err, domain.ErrConflict) || notifyErrCode(err) != domain.CodeNotifyTemplateCodeTaken {
		t.Errorf("重复 code, err = %v", err)
	}
}

func TestNotifyUpdateTemplate(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	e.tgTemplate(t, "shipped")

	content := domain.NotifyContent{Content: "订单 {id} 已签收", Variables: []string{"id"}}
	off := false
	got, err := e.svc.UpdateTemplate(ctx, "shipped", service.UpdateNotifyTemplateInput{Content: &content, Enabled: &off})
	if err != nil {
		t.Fatalf("UpdateTemplate: %v", err)
	}
	if got.Content.Content != "订单 {id} 已签收" || got.Enabled {
		t.Fatalf("got = %+v", got)
	}
	if got.Channel != domain.NotifyChannelTelegram || got.Mode != domain.NotifyModeCustom {
		t.Fatal("channel 与 mode 创建后不可改")
	}

	bad := domain.NotifyContent{Content: "{a}", Variables: []string{}}
	if _, err := e.svc.UpdateTemplate(ctx, "shipped", service.UpdateNotifyTemplateInput{Content: &bad}); notifyErrCode(err) != domain.CodeNotifyTemplateInvalid {
		t.Fatalf("更新也要校验内容, err = %v", err)
	}
	if _, err := e.svc.UpdateTemplate(ctx, "nope", service.UpdateNotifyTemplateInput{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestNotifySetTemplateProviderRules(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	smsA := e.provider(t, "fake_sms", "a")
	smsB := e.provider(t, "fake_sms", "b")
	tg1 := e.provider(t, "fake_tg", "t1")
	tg2 := e.provider(t, "fake_tg", "t2")
	e.smsTemplate(t, "login_sms")
	e.tgTemplate(t, "shipped")

	linkErr := func(code string, pid uuid.UUID, in service.SetNotifyLinkInput) error {
		err := e.svc.SetTemplateProvider(ctx, code, pid, in)
		if err != nil && notifyErrCode(err) != domain.CodeNotifyLinkInvalid {
			t.Errorf("err = %v, want NOTIFY_LINK_INVALID", err)
		}
		return err
	}
	if linkErr("login_sms", tg1, service.SetNotifyLinkInput{ProviderTemplateID: "x", Enabled: true}) == nil {
		t.Error("渠道不一致应被拒绝")
	}
	if linkErr("login_sms", smsA, service.SetNotifyLinkInput{Enabled: true}) == nil {
		t.Error("vendor 模板必须填供应商侧模板 ID")
	}
	if linkErr("shipped", tg1, service.SetNotifyLinkInput{ProviderTemplateID: "x", Enabled: true}) == nil {
		t.Error("custom 模板不该填供应商侧模板 ID")
	}

	// sms 可以挂多个；重复提交是更新。
	e.link(t, "login_sms", smsA, "SMS_A", 0)
	e.link(t, "login_sms", smsB, "SMS_B", 5)
	if err := e.svc.SetTemplateProvider(ctx, "login_sms", smsA, service.SetNotifyLinkInput{ProviderTemplateID: "SMS_A2", Enabled: false, Priority: 9}); err != nil {
		t.Fatalf("更新关联: %v", err)
	}
	d, err := e.svc.GetTemplate(ctx, "login_sms")
	if err != nil || len(d.Links) != 2 {
		t.Fatalf("detail = %+v, err = %v", d, err)
	}
	if d.Links[0].ProviderID != smsA || d.Links[0].Priority != 9 || d.Links[0].Enabled || d.Links[0].ProviderTemplateID != "SMS_A2" {
		t.Fatalf("按 priority 降序，第一条应是更新后的 a: %+v", d.Links[0])
	}
	if d.Links[0].ProviderType != "fake_sms" || d.Links[0].ProviderDescription != "a" || !d.Links[0].ProviderEnabled {
		t.Fatalf("关联里应带供应商摘要: %+v", d.Links[0])
	}

	// IM 渠道一个模板只挂一个实例。
	e.link(t, "shipped", tg1, "", 0)
	if linkErr("shipped", tg2, service.SetNotifyLinkInput{Enabled: true}) == nil {
		t.Error("IM 渠道只能关联一个供应商实例")
	}
	e.link(t, "shipped", tg1, "", 3) // 对同一个实例重复提交仍然允许

	if err := e.svc.RemoveTemplateProvider(ctx, "shipped", tg2); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("解除不存在的关联 err = %v, want ErrNotFound", err)
	}
}

func TestNotifyDeleteTemplateCascadesLinksAndProviderUsages(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	pid := e.provider(t, "fake_sms", "a")
	e.smsTemplate(t, "login_sms")
	e.smsTemplate(t, "order_sms")
	e.link(t, "login_sms", pid, "SMS_1", 0)
	e.link(t, "order_sms", pid, "SMS_2", 7)

	usages, err := e.svc.ProviderUsages(ctx, pid)
	if err != nil || len(usages) != 2 || usages[0].Code != "login_sms" || usages[1].Priority != 7 || usages[1].ProviderTemplateID != "SMS_2" {
		t.Fatalf("usages = %+v, err = %v", usages, err)
	}
	views, err := e.svc.ListProviders(ctx)
	if err != nil || len(views) != 1 || views[0].TemplateCount != 2 {
		t.Fatalf("views = %+v, err = %v", views, err)
	}
	tpls, err := e.svc.ListTemplates(ctx)
	if err != nil || len(tpls) != 2 || tpls[0].ProviderCount != 1 {
		t.Fatalf("tpls = %+v, err = %v", tpls, err)
	}

	if err := e.svc.DeleteTemplate(ctx, "login_sms"); err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}
	if usages, _ := e.svc.ProviderUsages(ctx, pid); len(usages) != 1 {
		t.Fatalf("删模板应级联删掉关联, usages = %+v", usages)
	}
	if err := e.svc.DeleteTemplate(ctx, "login_sms"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("重复删除 err = %v, want ErrNotFound", err)
	}
	if _, err := e.svc.ProviderUsages(ctx, uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("未知供应商 err = %v, want ErrNotFound", err)
	}
}

func TestNotifyListLogsFiltersAndPaginates(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	for i, row := range []struct {
		code    string
		success bool
	}{{"a", true}, {"a", false}, {"b", true}, {"a", true}} {
		if _, err := e.pool.Exec(ctx, `
			INSERT INTO notify_log (channel, target, code, provider, success, error)
			VALUES ('sms', '138', $1, 'fake_sms', $2, $3)`, row.code, row.success, strings.Repeat("e", i)); err != nil {
			t.Fatalf("插入日志: %v", err)
		}
	}
	all, total, err := e.svc.ListLogs(ctx, service.NotifyLogFilter{})
	if err != nil || total != 4 || len(all) != 4 {
		t.Fatalf("all = %d, total = %d, err = %v", len(all), total, err)
	}
	ok := false
	byCode, total, _ := e.svc.ListLogs(ctx, service.NotifyLogFilter{Code: "a", Success: &ok})
	if total != 1 || len(byCode) != 1 || byCode[0].Code != "a" || byCode[0].Success {
		t.Fatalf("byCode = %+v, total = %d", byCode, total)
	}
	page, total, _ := e.svc.ListLogs(ctx, service.NotifyLogFilter{Limit: 2, Offset: 3})
	if total != 4 || len(page) != 1 {
		t.Fatalf("分页：total = %d（应是过滤后的总数）, 本页 %d 条（应是最后 1 条）", total, len(page))
	}
}
