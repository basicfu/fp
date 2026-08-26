package service_test

import (
	"context"
	"testing"
	"unicode/utf8"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

func TestMaskSubject(t *testing.T) {
	tests := []struct {
		typ, in, want string
	}{
		{domain.IdentityTypePhone, "13800138000", "138****8000"},
		{domain.IdentityTypeEmail, "alice@example.com", "a****e@example.com"},
		{domain.IdentityTypeEmail, "a@example.com", "*@example.com"},
		{domain.IdentityTypeUsername, "alice", "al***"},
		{domain.IdentityTypeUsername, "ab", "**"},
		{domain.IdentityTypePhone, "", ""},
		{domain.IdentityTypeWechatMP, "openid-xyz", "op********"},
		// 多字节输入：按字节切会劈开汉字、产出非法 UTF-8，
		// PostgreSQL 的 text 列直接拒收，整条审计记录写不进去。
		{domain.IdentityTypeEmail, "张三@example.com", "张****三@example.com"},
		{domain.IdentityTypeUsername, "张三丰", "张三*"},
		// 与 brief 字面值的偏离见 task-12-report.md「偏离」一节：11-rune 分支是
		// r[:3]+"****"+r[7:]，对这个输入实算是 "138****800中"，不是 "13****800中"——
		// 用 go run 对着 brief 给出的 MaskSubject 实现原样验证过。
		{domain.IdentityTypePhone, "1380013800中", "138****800中"},
	}
	for _, tt := range tests {
		got := service.MaskSubject(tt.typ, tt.in)
		if got != tt.want {
			t.Errorf("MaskSubject(%q, %q) = %q, want %q", tt.typ, tt.in, got, tt.want)
		}
		// 无论走哪个分支，输出都必须是合法 UTF-8，否则落库时整行会被丢掉
		if !utf8.ValidString(got) {
			t.Errorf("MaskSubject(%q, %q) 产出非法 UTF-8: %q", tt.typ, tt.in, got)
		}
	}
}

// 脱敏结果必须能真正写进 PostgreSQL。
//
// 非法 UTF-8 会让 Write 报错，而 writeLog 按设计吞掉错误——
// 于是审计记录静默消失，成功登录也一样。更糟的是可被利用：
// 攻击者在账号前加一个非 ASCII 字符，自己的失败登录记录就全写不进去。
func TestMaskedSubjectIsPersistable(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	logs := service.NewLoginLogService(pool)
	ctx := context.Background()

	subjects := []struct{ typ, subject string }{
		{domain.IdentityTypeEmail, "张三@example.com"},
		{domain.IdentityTypeUsername, "用户名"},
		{domain.IdentityTypePhone, "1380013800中"},
		{domain.IdentityTypeWechatMP, "微信openid"},
	}
	for _, s := range subjects {
		if err := logs.Write(ctx, domain.LoginLog{
			IdentityType: s.typ,
			Subject:      service.MaskSubject(s.typ, s.subject),
			Event:        domain.LoginEventLogin,
			Success:      false,
			Reason:       "test",
		}); err != nil {
			t.Fatalf("写入 %q 的脱敏结果失败（多半是非法 UTF-8）: %v", s.subject, err)
		}
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM login_log`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != len(subjects) {
		t.Fatalf("落库条数 = %d, want %d", n, len(subjects))
	}
}

func TestLoginLogWriteAndList(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	logs := service.NewLoginLogService(pool)
	users := service.NewUserService(pool)
	ctx := context.Background()

	u, _, _, err := users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := logs.Write(ctx, domain.LoginLog{
			UserID:       &u.ID,
			IdentityType: domain.IdentityTypePhone,
			Subject:      "138****8000",
			Event:        domain.LoginEventLogin,
			Success:      true,
			IP:           "1.2.3.4",
			UA:           "go-test",
		}); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}

	list, err := logs.ListByUser(ctx, u.ID, 10)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len = %d, want 3", len(list))
	}
	if list[0].Subject != "138****8000" || !list[0].Success {
		t.Fatalf("log = %+v", list[0])
	}
}

// 失败的登录没有 user_id，也必须能写进去。
func TestLoginLogWriteWithoutUser(t *testing.T) {
	logs := service.NewLoginLogService(testsupport.NewTestDB(t))
	if err := logs.Write(context.Background(), domain.LoginLog{
		IdentityType: domain.IdentityTypePhone,
		Subject:      "138****8000",
		Event:        domain.LoginEventLogin,
		Success:      false,
		Reason:       "账号或密码不正确",
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
}
