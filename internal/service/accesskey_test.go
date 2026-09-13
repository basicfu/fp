package service_test

import (
	"context"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
	"github.com/basicfu/fp/sdk/authzcore"
)

type akEnv struct {
	svc   *service.AccessKeyService
	authz *service.AuthzService
	app   *domain.Application
	pub   *recordingPublisher
	now   time.Time
}

func newAKEnv(t *testing.T) *akEnv {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	app, _, err := newAppServiceWith(t, pool).Create(context.Background(), "商城", "mall")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	e := &akEnv{authz: service.NewAuthzService(pool), app: app, pub: &recordingPublisher{},
		now: time.UnixMilli(1_800_000_000_000)}
	e.svc = service.NewAccessKeyServiceWithClock(pool, e.pub, func() time.Time { return e.now })
	return e
}

func TestCreateAccessKey(t *testing.T) {
	e := newAKEnv(t)
	ctx := context.Background()
	role, err := e.authz.CreateRole(ctx, "合作方A", "合作方A", nil)
	if err != nil {
		t.Fatal(err)
	}
	k, err := e.svc.Create(ctx, service.CreateAccessKeyInput{
		Remark: " 顺丰 ", RoleKey: role.Key, ValidDays: 7,
		AllowedIPs: []string{"10.0.0.5/24", "1.2.3.4", "", "::1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^FPAK[A-Z0-9]{20}$`).MatchString(k.AccessKeyID) || len(k.Secret) != 43 {
		t.Fatalf("凭据格式不对: %q / %q", k.AccessKeyID, k.Secret)
	}
	if k.Remark != "顺丰" || k.RoleKey != role.Key {
		t.Fatalf("字段不对: %+v", k)
	}
	want := []string{"10.0.0.0/24", "1.2.3.4/32", "::1/128"}
	if len(k.AllowedIPs) != len(want) {
		t.Fatalf("IP 白名单 = %v, want %v", k.AllowedIPs, want)
	}
	for i, p := range k.AllowedIPs {
		if p.String() != want[i] {
			t.Fatalf("IP 白名单 = %v, want %v", k.AllowedIPs, want)
		}
	}
	if w := e.now.Add(7 * 24 * time.Hour).UnixMilli(); k.ExpiresAt != w {
		t.Fatalf("ExpiresAt = %d, want %d", k.ExpiresAt, w)
	}
	if list, _ := e.svc.List(ctx, ""); len(list) != 1 || list[0].Secret != "" {
		t.Fatalf("列表不应带 SK: %+v", list)
	}
}

func TestCreateAccessKeyValidation(t *testing.T) {
	e := newAKEnv(t)
	ctx := context.Background()
	tooMany := make([]string, domain.MaxAllowedIPs+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("10.0.0.%d", i)
	}
	cases := []struct {
		name string
		in   service.CreateAccessKeyInput
		code string
	}{
		{"备注为空", service.CreateAccessKeyInput{Remark: "  "}, domain.CodeInvalidArgument},
		{"天数为负", service.CreateAccessKeyInput{Remark: "r", ValidDays: -1}, domain.CodeInvalidArgument},
		{"绑定 GUEST", service.CreateAccessKeyInput{Remark: "r", RoleKey: authzcore.GuestRoleKey}, domain.CodeRoleBuiltin},
		{"角色不存在", service.CreateAccessKeyInput{Remark: "r", RoleKey: "不存在"}, domain.CodeRoleNotFound},
		{"IP 不合法", service.CreateAccessKeyInput{Remark: "r", AllowedIPs: []string{"1.2.3"}}, domain.CodeInvalidArgument},
		{"IP 超过上限", service.CreateAccessKeyInput{Remark: "r", AllowedIPs: tooMany}, domain.CodeInvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := e.svc.Create(ctx, c.in)
			wantDomainCode(t, err, c.code)
		})
	}
}

func TestResolveAccessKey(t *testing.T) {
	e := newAKEnv(t)
	ctx := context.Background()
	k, err := e.svc.Create(ctx, service.CreateAccessKeyInput{Remark: "r", ValidDays: 1})
	if err != nil {
		t.Fatal(err)
	}

	m, err := e.svc.Resolve(ctx, e.app, k.AccessKeyID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Key.Secret == "" || m.CacheTTL != time.Duration(e.app.Session.TokenCacheTTLSeconds)*time.Second {
		t.Fatalf("材料不对: secret=%q ttl=%v", m.Key.Secret, m.CacheTTL)
	}

	e.now = e.now.Add(24*time.Hour - 10*time.Second)
	if m, err := e.svc.Resolve(ctx, e.app, k.AccessKeyID); err != nil || m.CacheTTL != 10*time.Second {
		t.Fatalf("快到期时缓存时长应取剩余时间: ttl=%v err=%v", m.CacheTTL, err)
	}

	e.now = e.now.Add(10 * time.Second)
	_, err = e.svc.Resolve(ctx, e.app, k.AccessKeyID)
	wantDomainCode(t, err, domain.CodeAccessKeyExpired)

	if _, err := e.svc.SetStatus(ctx, k.ID, domain.AccessKeyStatusDisabled); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Resolve(ctx, e.app, k.AccessKeyID)
	wantDomainCode(t, err, domain.CodeAccessKeyDisabled)

	_, err = e.svc.Resolve(ctx, e.app, "FPAKNOTEXIST000000000000")
	wantDomainCode(t, err, domain.CodeAccessKeyInvalid)
}

// 【辨别力】只改备注时，到期时间与白名单都不能被顺手改掉。
func TestUpdateAccessKeyOnlyTouchesGivenFields(t *testing.T) {
	e := newAKEnv(t)
	ctx := context.Background()
	k, err := e.svc.Create(ctx, service.CreateAccessKeyInput{Remark: "旧", ValidDays: 7, AllowedIPs: []string{"1.2.3.4"}})
	if err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Hour)
	remark := "新"
	got, err := e.svc.Update(ctx, k.ID, service.UpdateAccessKeyInput{Remark: &remark})
	if err != nil {
		t.Fatal(err)
	}
	if got.Remark != "新" || got.ExpiresAt != k.ExpiresAt || len(got.AllowedIPs) != 1 || got.Secret != "" {
		t.Fatalf("只改备注却动了别的字段: before=%+v after=%+v", k, got)
	}
	zero := 0
	if got, _ = e.svc.Update(ctx, k.ID, service.UpdateAccessKeyInput{ValidDays: &zero}); got.ExpiresAt != 0 {
		t.Fatalf("ValidDays=0 应改为永不过期，got %d", got.ExpiresAt)
	}
	_, err = e.svc.Update(ctx, uuid.New(), service.UpdateAccessKeyInput{Remark: &remark})
	wantDomainCode(t, err, domain.CodeAccessKeyNotFound)
}

func TestRecordUsage(t *testing.T) {
	e := newAKEnv(t)
	ctx := context.Background()
	k, err := e.svc.Create(ctx, service.CreateAccessKeyInput{Remark: "r"})
	if err != nil {
		t.Fatal(err)
	}
	t1 := e.now.Add(-2 * time.Minute).Truncate(time.Minute)
	if err := e.svc.RecordUsage(ctx, map[string]time.Time{k.AccessKeyID: t1}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RecordUsage(ctx, map[string]time.Time{k.AccessKeyID: t1.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.Get(ctx, k.ID); got.LastUsedAt != t1.UnixMilli() {
		t.Fatalf("更旧的时间不应覆盖: got %d want %d", got.LastUsedAt, t1.UnixMilli())
	}
	if err := e.svc.RecordUsage(ctx, map[string]time.Time{k.AccessKeyID: e.now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.Get(ctx, k.ID); got.LastUsedAt != e.now.UnixMilli() {
		t.Fatalf("未来时间应按当前时间记: got %d want %d", got.LastUsedAt, e.now.UnixMilli())
	}
}

func TestAccessKeyWritesPublishChanged(t *testing.T) {
	e := newAKEnv(t)
	ctx := context.Background()
	k, err := e.svc.Create(ctx, service.CreateAccessKeyInput{Remark: "r"})
	if err != nil {
		t.Fatal(err)
	}
	remark := "x"
	steps := []struct {
		name string
		run  func() error
	}{
		{"改备注", func() error { _, err := e.svc.Update(ctx, k.ID, service.UpdateAccessKeyInput{Remark: &remark}); return err }},
		{"停用", func() error { _, err := e.svc.SetStatus(ctx, k.ID, domain.AccessKeyStatusDisabled); return err }},
		{"删除", func() error { return e.svc.Delete(ctx, k.ID) }},
	}
	for _, s := range steps {
		if err := s.run(); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if ev := e.pub.last(t); ev.Kind != domain.EventKindAccessKeyChanged || ev.AccessKeyID != k.AccessKeyID || ev.AppID != uuid.Nil {
			t.Fatalf("%s: 事件 = %+v", s.name, ev)
		}
	}
}
