package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

func newUserService(t *testing.T) *service.UserService {
	t.Helper()
	return service.NewUserService(testsupport.NewTestDB(t))
}

func TestEnsureUserWithIdentityCreatesThenReuses(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	in := service.EnsureIdentityInput{
		Type:     domain.IdentityTypePhone,
		Subject:  "13800138000",
		Nickname: "用户8000",
	}

	u1, id1, created, err := svc.EnsureUserWithIdentity(ctx, in)
	if err != nil {
		t.Fatalf("首次 EnsureUserWithIdentity: %v", err)
	}
	if !created {
		t.Fatal("首次调用 created 应为 true")
	}
	if u1.Nickname != "用户8000" {
		t.Fatalf("Nickname = %q", u1.Nickname)
	}
	if id1.Type != domain.IdentityTypePhone || id1.Subject != "13800138000" {
		t.Fatalf("identity = %+v", id1)
	}

	u2, id2, created, err := svc.EnsureUserWithIdentity(ctx, in)
	if err != nil {
		t.Fatalf("再次 EnsureUserWithIdentity: %v", err)
	}
	if created {
		t.Fatal("已存在时 created 应为 false")
	}
	if u2.ID != u1.ID {
		t.Fatalf("UserID 不一致: %v vs %v", u2.ID, u1.ID)
	}
	if id2.ID != id1.ID {
		t.Fatalf("IdentityID 不一致: %v vs %v", id2.ID, id1.ID)
	}
}

// 归并规则的核心用例：同一手机号在 sms_code 与 password 两种登录方式下
// 必须落到同一个 user。两者共用 identity(type='phone')。
func TestSamePhoneAcrossConnectorsMergesToOneUser(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	// 短信验证码首次登录，创建用户
	u1, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("短信登录建号: %v", err)
	}

	// 该用户设置密码后，用手机号+密码登录，查到的必须是同一个 user
	if err := svc.SetPassword(ctx, u1.ID, "hunter2hunter2"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	u2, _, err := svc.FindByIdentity(ctx, domain.IdentityTypePhone, "13800138000")
	if err != nil {
		t.Fatalf("FindByIdentity: %v", err)
	}
	if u2.ID != u1.ID {
		t.Fatalf("同一手机号归并失败: %v vs %v", u2.ID, u1.ID)
	}
	if err := svc.VerifyPassword(ctx, u2.ID, "hunter2hunter2"); err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
}

// 不同类型的标识各自独立：同一个字符串作为 username 和 phone 是两个人。
func TestDifferentIdentityTypesDoNotMerge(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	u1, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("phone: %v", err)
	}
	u2, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypeUsername, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("username: %v", err)
	}
	if u1.ID == u2.ID {
		t.Fatal("不同 type 的同名 subject 不应归并到同一用户")
	}
}

// 微信这类第三方标识通过 union_key 归并：同一个人的多个 openid 落到同一 user。
func TestWechatMergesByUnionKey(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	u1, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypeWechatMP, Subject: "openid-mp", UnionKey: "union-1",
	})
	if err != nil {
		t.Fatalf("首个 openid: %v", err)
	}

	// 同一 unionId 下的另一个 openid（模拟小程序端）
	u2, id2, created, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: "wechat_mini", Subject: "openid-mini", UnionKey: "union-1",
	})
	if err != nil {
		t.Fatalf("第二个 openid: %v", err)
	}
	if created {
		t.Fatal("同 unionKey 不应创建新用户")
	}
	if u2.ID != u1.ID {
		t.Fatalf("unionKey 归并失败: %v vs %v", u2.ID, u1.ID)
	}
	if id2.Subject != "openid-mini" {
		t.Fatalf("应为新建的 identity 行, got %+v", id2)
	}

	ids, err := svc.ListIdentities(ctx, u1.ID)
	if err != nil {
		t.Fatalf("ListIdentities: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("identity 数量 = %d, want 2", len(ids))
	}

	// 规则 1 必须优先于规则 2。重复微信登录就是"(type,subject) 已存在 **且**
	// unionKey 也已存在"，是本任务里流量最高的一条路径：若两条规则顺序调换，
	// 每一次回头客登录都会变成 409。
	u3, id3, created, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypeWechatMP, Subject: "openid-mp", UnionKey: "union-1",
	})
	if err != nil {
		t.Fatalf("重复微信登录: %v", err)
	}
	if created {
		t.Fatal("重复登录不应创建用户")
	}
	if u3.ID != u1.ID {
		t.Fatalf("重复登录落到了别的用户: %v vs %v", u3.ID, u1.ID)
	}
	if id3.ID != id1ID(t, svc, ctx) {
		t.Fatal("重复登录应复用原 identity 行，而不是新插一条")
	}
	after, err := svc.ListIdentities(ctx, u1.ID)
	if err != nil {
		t.Fatalf("ListIdentities: %v", err)
	}
	if len(after) != 2 {
		t.Fatalf("重复登录后 identity 数量 = %d, want 仍为 2", len(after))
	}
}

// id1ID 取回 (wechat_mp, openid-mp) 那条 identity 的 ID，供上面的复用断言使用。
func id1ID(t *testing.T, svc *service.UserService, ctx context.Context) uuid.UUID {
	t.Helper()
	_, id, err := svc.FindByIdentity(ctx, domain.IdentityTypeWechatMP, "openid-mp")
	if err != nil {
		t.Fatalf("FindByIdentity: %v", err)
	}
	return id.ID
}

// 归并靠字面相等，所以同一个人的不同写法必须先收敛。
func TestSubjectNormalization(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	u1, _, created, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypeEmail, Subject: "Alice@Example.COM",
	})
	if err != nil {
		t.Fatalf("首次: %v", err)
	}
	if !created {
		t.Fatal("首次应创建用户")
	}

	u2, _, created, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypeEmail, Subject: "  alice@example.com  ",
	})
	if err != nil {
		t.Fatalf("第二次: %v", err)
	}
	if created {
		t.Fatal("大小写与空白差异不应产生新用户")
	}
	if u2.ID != u1.ID {
		t.Fatalf("邮箱未规整: %v vs %v", u2.ID, u1.ID)
	}

	// 用户名同样规整
	un1, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypeUsername, Subject: "Alice",
	})
	if err != nil {
		t.Fatalf("username 首次: %v", err)
	}
	un2, _, created, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypeUsername, Subject: "alice",
	})
	if err != nil {
		t.Fatalf("username 第二次: %v", err)
	}
	if created || un2.ID != un1.ID {
		t.Fatal("用户名未规整")
	}

	// 落库的是规整后的值
	ids, err := svc.ListIdentities(ctx, u1.ID)
	if err != nil {
		t.Fatalf("ListIdentities: %v", err)
	}
	if ids[0].Subject != "alice@example.com" {
		t.Fatalf("落库 subject = %q, want alice@example.com", ids[0].Subject)
	}
}

// 空白 subject 必须被拒绝，不能被规整成空串落库。
//
// 这是"先规整再校验"的直接后果：顺序反过来的话，"   " 通过 validate、
// 被 normalize 削成 ""，再叠加 UNIQUE (type, subject)，所有空白 subject
// 折叠成同一行——第二个调用者会拿到第一个调用者的账号。
func TestBlankSubjectIsRejectedNotCollapsed(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	for _, blank := range []string{"   ", "\t", " \n "} {
		_, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
			Type: domain.IdentityTypeUsername, Subject: blank,
		})
		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("EnsureUserWithIdentity(%q) err = %v, want ErrInvalidArgument", blank, err)
		}
	}

	u, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	if _, err := svc.AttachIdentity(ctx, u.ID, service.EnsureIdentityInput{
		Type: domain.IdentityTypeUsername, Subject: "  ",
	}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("AttachIdentity 空白 subject err = %v, want ErrInvalidArgument", err)
	}
}

// 读侧也要规整：connector 拿到的是用户原始输入，写侧存的是规整后的值。
func TestFindByIdentityNormalizesSubject(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	u, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypeEmail, Subject: "alice@example.com",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}

	for _, raw := range []string{"Alice@Example.com", "  ALICE@EXAMPLE.COM  ", "alice@example.com"} {
		found, _, err := svc.FindByIdentity(ctx, domain.IdentityTypeEmail, raw)
		if err != nil {
			t.Fatalf("FindByIdentity(%q): %v", raw, err)
		}
		if found.ID != u.ID {
			t.Fatalf("FindByIdentity(%q) 查到了别人: %v vs %v", raw, found.ID, u.ID)
		}
	}
}

// 两条写入路径必须用同一套规整，否则只规整一条等于没规整。
func TestAttachIdentityNormalizesSubject(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	u, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}

	// 用非规整形式挂一个邮箱标识
	if _, err := svc.AttachIdentity(ctx, u.ID, service.EnsureIdentityInput{
		Type: domain.IdentityTypeEmail, Subject: "  Alice@Example.COM ",
	}); err != nil {
		t.Fatalf("AttachIdentity: %v", err)
	}

	// 规整形式必须能查到同一个人，而不是被当成新用户
	found, _, err := svc.FindByIdentity(ctx, domain.IdentityTypeEmail, "alice@example.com")
	if err != nil {
		t.Fatalf("规整形式查不到，说明 AttachIdentity 未规整: %v", err)
	}
	if found.ID != u.ID {
		t.Fatalf("查到了别的用户: %v vs %v", found.ID, u.ID)
	}

	// 再走 EnsureUserWithIdentity 也必须归并到同一个人，不能新建
	same, _, created, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypeEmail, Subject: "ALICE@EXAMPLE.COM",
	})
	if err != nil {
		t.Fatalf("EnsureUserWithIdentity: %v", err)
	}
	if created {
		t.Fatal("两条写入路径规整不一致，产生了重复用户")
	}
	if same.ID != u.ID {
		t.Fatalf("归并到了别的用户: %v vs %v", same.ID, u.ID)
	}
}

// bcrypt 超过 72 字节会直接报错；必须在调用它之前拦成 400，而不是漏成 500。
func TestSetPasswordRejectsTooLong(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	u, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}

	// 73 个 ASCII 字符——密码管理器生成的长口令就是这个量级
	long := strings.Repeat("a", 73)
	if err := svc.SetPassword(ctx, u.ID, long); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("73 字节 err = %v, want ErrInvalidArgument", err)
	}
	// 25 个汉字 = 75 字节，rune 数看着不多，字节数已超限
	cjk := strings.Repeat("密", 25)
	if err := svc.SetPassword(ctx, u.ID, cjk); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("25 个汉字 err = %v, want ErrInvalidArgument", err)
	}
	// 边界内应当成功
	if err := svc.SetPassword(ctx, u.ID, strings.Repeat("a", 72)); err != nil {
		t.Fatalf("72 字节应当成功: %v", err)
	}
}

func TestAttachIdentityRejectsSubjectOwnedByAnotherUser(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	u1, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("u1: %v", err)
	}
	u2, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13900139000",
	})
	if err != nil {
		t.Fatalf("u2: %v", err)
	}

	// 把 u1 已占用的手机号绑到 u2 上必须失败，且必须是可识别的冲突错误。
	_, err = svc.AttachIdentity(ctx, u2.ID, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	_ = u1
}

// union_key 的不变式：同一个 union_key 不能横跨两个用户。
// 数据库层表达不了（同 union_key 本来就允许多行），只能靠 AttachIdentity 守。
func TestAttachIdentityRejectsUnionKeyOwnedByAnotherUser(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	u1, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypeWechatMP, Subject: "openid-a", UnionKey: "union-1",
	})
	if err != nil {
		t.Fatalf("u1: %v", err)
	}
	u2, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("u2: %v", err)
	}

	// 把 u1 名下的 unionKey 挂到 u2 上必须失败
	if _, err := svc.AttachIdentity(ctx, u2.ID, service.EnsureIdentityInput{
		Type: "wechat_mini", Subject: "openid-b", UnionKey: "union-1",
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}

	// 挂到本人名下则应成功（这正是"一个人多个 openid"的正常场景）
	if _, err := svc.AttachIdentity(ctx, u1.ID, service.EnsureIdentityInput{
		Type: "wechat_mini", Subject: "openid-b", UnionKey: "union-1",
	}); err != nil {
		t.Fatalf("同一用户追加同 unionKey 的 identity 应成功: %v", err)
	}
}

func TestFindByIdentityNotFound(t *testing.T) {
	svc := newUserService(t)
	_, _, err := svc.FindByIdentity(context.Background(), domain.IdentityTypePhone, "13800138000")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestVerifyPasswordWithoutPasswordSet(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	u, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	// 未设置密码的用户，任何密码都不应通过，且不能 panic。
	if err := svc.VerifyPassword(ctx, u.ID, ""); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("空密码 err = %v, want ErrInvalidCredential", err)
	}
	if err := svc.VerifyPassword(ctx, u.ID, "anything"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("任意密码 err = %v, want ErrInvalidCredential", err)
	}
}

// 三条失败路径的耗时必须同量级，否则响应时间会泄露账号是否存在。
//
// 判定用的是"下限"而不是"两者之差"：bcrypt cost 10 至少几十毫秒，
// 而短路掉 bcrypt 的路径是微秒级，两者相差三个数量级以上。
// 取一个远低于 bcrypt 实测耗时、又远高于纯查询耗时的阈值，
// 既能抓住"被短路了"，又不会因机器快慢而抖动。
func TestVerifyPasswordEqualizesTiming(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	const minBcrypt = 5 * time.Millisecond

	withPwd, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	if err := svc.SetPassword(ctx, withPwd.ID, "hunter2hunter2"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	noPwd, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13900139000",
	})
	if err != nil {
		t.Fatalf("建号2: %v", err)
	}

	cases := []struct {
		name   string
		userID uuid.UUID
	}{
		{"用户不存在", uuid.New()},
		{"用户存在但未设密码", noPwd.ID},
		{"用户存在但密码错误", withPwd.ID},
	}
	for _, tc := range cases {
		start := time.Now()
		err := svc.VerifyPassword(ctx, tc.userID, "definitely-wrong-password")
		elapsed := time.Since(start)

		if !errors.Is(err, domain.ErrInvalidCredential) {
			t.Errorf("%s: err = %v, want ErrInvalidCredential", tc.name, err)
		}
		if elapsed < minBcrypt {
			t.Errorf("%s: 耗时 %v < %v，说明跳过了 bcrypt，响应时间会泄露账号是否存在",
				tc.name, elapsed, minBcrypt)
		}
	}
}

func TestSetPasswordRejectsTooShort(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	u, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	if err := svc.SetPassword(ctx, u.ID, "short"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}

func TestSetStatusEnforcesStateMachine(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	u, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}

	frozen, err := svc.SetStatus(ctx, u.ID, domain.UserStatusFrozen)
	if err != nil {
		t.Fatalf("冻结: %v", err)
	}
	if frozen.Status != domain.UserStatusFrozen {
		t.Fatalf("Status = %q", frozen.Status)
	}
	if frozen.CanLogin() {
		t.Fatal("冻结用户不应可登录")
	}

	// FROZEN → PENDING_DELETE 不是合法迁移
	if _, err := svc.SetStatus(ctx, u.ID, domain.UserStatusPendingDelete); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}

	if _, err := svc.SetStatus(ctx, u.ID, domain.UserStatusActive); err != nil {
		t.Fatalf("解冻: %v", err)
	}
}

func TestEnsureRegistrationIsIdempotent(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	users := service.NewUserService(pool)
	reg := connector.NewRegistry()
	if err := reg.Register(connector.NewPassword(nil)); err != nil {
		t.Fatalf("注册 password: %v", err)
	}
	if err := reg.Register(connector.NewSMSCode(nil)); err != nil {
		t.Fatalf("注册 sms_code: %v", err)
	}
	apps := service.NewApplicationService(pool, reg)
	ctx := context.Background()

	app, _, err := apps.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create app: %v", err)
	}
	u, _, _, err := users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := users.EnsureRegistration(ctx, u.ID, app.ID); err != nil {
			t.Fatalf("第 %d 次 EnsureRegistration: %v", i+1, err)
		}
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM user_application WHERE user_id = $1 AND application_id = $2`,
		u.ID, app.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("注册关系数量 = %d, want 1", n)
	}
}

func TestGetByIDNotFound(t *testing.T) {
	svc := newUserService(t)
	if _, err := svc.GetByID(context.Background(), uuid.Nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestTouchIdentityLogin(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	_, id, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	if id.LastLoginAt != 0 {
		t.Fatalf("新建 identity 的 LastLoginAt = %d, want 0", id.LastLoginAt)
	}

	if err := svc.TouchIdentityLogin(ctx, id.ID); err != nil {
		t.Fatalf("TouchIdentityLogin: %v", err)
	}

	_, got, err := svc.FindByIdentity(ctx, domain.IdentityTypePhone, "13800138000")
	if err != nil {
		t.Fatalf("FindByIdentity: %v", err)
	}
	if got.LastLoginAt == 0 {
		t.Fatal("LastLoginAt 未更新")
	}
}
