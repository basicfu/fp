package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/httpapi"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

func seedUser(t *testing.T, deps httpapi.Deps, phone, nickname string) domain.User {
	t.Helper()
	u, _, _, err := deps.Users.EnsureUserWithIdentity(context.Background(), service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: phone, Nickname: nickname,
	})
	if err != nil {
		t.Fatalf("建号 %s: %v", phone, err)
	}
	return *u
}

func TestUserListAndSearchOverHTTP(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	seedUser(t, deps, "13800138000", "阿里斯")
	seedUser(t, deps, "13900139000", "鲍勃")

	rec := do(t, h, token, http.MethodGet, "/admin/api/users", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Items []struct {
			ID          string `json:"id"`
			Nickname    string `json:"nickname"`
			HasPassword bool   `json:"hasPassword"`
			Identities  []struct {
				Type    string `json:"type"`
				Subject string `json:"subject"`
			} `json:"identities"`
		} `json:"items"`
		Total int `json:"total"`
	}
	decode(t, rec, &list)
	if list.Total != 2 || len(list.Items) != 2 {
		t.Fatalf("total=%d len=%d, want 2", list.Total, len(list.Items))
	}
	if len(list.Items[0].Identities) == 0 {
		t.Fatal("列表未带出登录标识")
	}

	rec = do(t, h, token, http.MethodGet, "/admin/api/users?keyword=13800", "")
	decode(t, rec, &list)
	if list.Total != 1 {
		t.Fatalf("搜索 total = %d, want 1", list.Total)
	}
}

// 响应里绝不能出现密码哈希。
func TestUserResponseNeverLeaksPasswordHash(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	u := seedUser(t, deps, "13800138000", "阿里斯")
	if err := deps.Users.SetPassword(context.Background(), u.ID, "hunter2hunter2"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	for _, path := range []string{"/admin/api/users", "/admin/api/users/" + u.ID.String()} {
		rec := do(t, h, token, http.MethodGet, path, "")
		body := rec.Body.String()
		if strings.Contains(body, "$2a$") || strings.Contains(body, "passwordHash") {
			t.Fatalf("%s 泄露了密码哈希: %s", path, body)
		}
		if !strings.Contains(body, `"hasPassword":true`) {
			t.Fatalf("%s 未返回 hasPassword: %s", path, body)
		}
	}
}

func TestSetUserStatusRevokesSessions(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	ctx := context.Background()
	u := seedUser(t, deps, "13800138000", "阿里斯")

	app, _, err := deps.Apps.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	sess, err := deps.Sessions.Issue(ctx, service.IssueInput{UserID: u.ID, App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	rec := do(t, h, token, http.MethodPatch, "/admin/api/users/"+u.ID.String()+"/status",
		`{"status":"FROZEN"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// 冻结必须连带踢掉已登录的设备
	if _, err := deps.Sessions.Validate(ctx, sess.Token, app); err == nil {
		t.Fatal("冻结后会话仍然有效")
	}
}

func TestSetUserStatusRejectsIllegalTransition(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	u := seedUser(t, deps, "13800138000", "阿里斯")

	rec := do(t, h, token, http.MethodPatch, "/admin/api/users/"+u.ID.String()+"/status",
		`{"status":"DELETED"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400（ACTIVE → DELETED 不是合法迁移）", rec.Code)
	}
}

func TestResetPasswordRevokesSessions(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	ctx := context.Background()
	u := seedUser(t, deps, "13800138000", "阿里斯")

	app, _, err := deps.Apps.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	sess, err := deps.Sessions.Issue(ctx, service.IssueInput{UserID: u.ID, App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	rec := do(t, h, token, http.MethodPut, "/admin/api/users/"+u.ID.String()+"/password",
		`{"password":"newpassword123"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// 改密不撤销会话的话，「密码泄露后改密」这个动作等于没做
	if _, err := deps.Sessions.Validate(ctx, sess.Token, app); err == nil {
		t.Fatal("改密后旧会话仍然有效")
	}
	if err := deps.Users.VerifyPassword(ctx, u.ID, "newpassword123"); err != nil {
		t.Fatalf("新密码不可用: %v", err)
	}
}

func TestResetPasswordRejectsTooShort(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	u := seedUser(t, deps, "13800138000", "阿里斯")

	rec := do(t, h, token, http.MethodPut, "/admin/api/users/"+u.ID.String()+"/password",
		`{"password":"short"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestListAndRevokeSessionsOverHTTP(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	ctx := context.Background()
	u := seedUser(t, deps, "13800138000", "阿里斯")

	app, _, err := deps.Apps.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := deps.Sessions.Issue(ctx, service.IssueInput{
			UserID: u.ID, App: app, UA: "device", IP: "1.2.3.4",
		}); err != nil {
			t.Fatalf("Issue %d: %v", i, err)
		}
	}

	base := "/admin/api/users/" + u.ID.String() + "/sessions"
	rec := do(t, h, token, http.MethodGet, base, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var sessions []struct {
		ID string `json:"id"`
		IP string `json:"ip"`
	}
	decode(t, rec, &sessions)
	if len(sessions) != 2 {
		t.Fatalf("会话数 = %d, want 2", len(sessions))
	}

	// 踢掉其中一个
	rec = do(t, h, token, http.MethodDelete, base+"/"+sessions[0].ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("单个踢下线 status = %d", rec.Code)
	}
	rec = do(t, h, token, http.MethodGet, base, "")
	decode(t, rec, &sessions)
	if len(sessions) != 1 {
		t.Fatalf("剩余会话数 = %d, want 1", len(sessions))
	}

	// 全部踢掉
	rec = do(t, h, token, http.MethodDelete, base, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("全部踢下线 status = %d", rec.Code)
	}
	rec = do(t, h, token, http.MethodGet, base, "")
	decode(t, rec, &sessions)
	if len(sessions) != 0 {
		t.Fatalf("剩余会话数 = %d, want 0", len(sessions))
	}

	// 两次踢下线都必须留下审计记录。
	//
	// 这条断言盯的是分层：handler 若绕过 AccountService 直接调 SessionService
	// （改回去只要一行），撤销照样生效、上面每一条断言都还是绿的，
	// 只有审计会静静地消失。计划二的 gRPC 管理入口正是同一个坑。
	logs, err := deps.Logs.ListByUser(ctx, u.ID, 50)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	var kicks int
	for _, l := range logs {
		if l.Event == domain.LoginEventRevoke && l.Reason == domain.RevokeReasonKick {
			kicks++
		}
	}
	if kicks != 2 {
		t.Fatalf("踢下线审计记录数 = %d, want 2（单台一条 + 全部一条）: %+v", kicks, logs)
	}
}

// service.SessionService.ListByUser 故意不按会话 ID 去重（轮换过渡期内新旧
// token 各算一条，这是它的既定行为，见 session_rotate_test.go 的
// TestRotationDoesNotDuplicateDeviceEntry）；去重是 HTTP 层 listSessions 的职责。
// 这里用假时钟直接触发一次真实轮换，验证在线设备列表确实把新旧 token 合并成了一台设备。
//
// 还必须断言 idleExpiresAt：tryRotate 把旧 token 缩短到 now + max(30s, cache_ttl)，
// 新 token 才拿到完整的空闲窗口。只断言"只有一条"测不出去重挑错了哪一条——
// 如果去重逻辑留了先出现的那条（旧 token，插入顺序在前），管理端会把一台刚刚
// 轮换过、完全健康的设备显示成"几十秒后过期"。
func TestListSessionsOverHTTPDeduplicatesRotationGraceSibling(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)
	ctx := context.Background()

	admin := service.NewAdminService(pool, rdb)
	if err := admin.EnsureBootstrap(ctx, "admin", "secret123456"); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
	adminToken, err := admin.Login(ctx, "admin", "secret123456")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	var nowMs int64 = 1_700_000_000_000
	sessions := service.NewSessionServiceWithClock(
		store.NewSessionStore(rdb), store.NewRevokePublisher(rdb), func() int64 { return nowMs })

	users := service.NewUserService(pool)
	logs := service.NewLoginLogService(pool)
	deps := httpapi.Deps{
		Admin:    admin,
		Apps:     service.NewApplicationService(pool),
		Users:    users,
		Accounts: service.NewAccountService(users, sessions, logs),
		Sessions: sessions,
		Logs:     logs,
		Registry: connector.NewRegistry(),
	}
	h := httpapi.NewRouter(deps)

	u := seedUser(t, deps, "13800138000", "阿里斯")
	created, _, err := deps.Apps.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	// rotate_interval 设得很短，配合下面推进的假时钟触发一次真实轮换。
	app, err := deps.Apps.UpdateSessionPolicy(ctx, created.ID, domain.SessionPolicy{
		IdleTimeoutSeconds: 1000, IdleTimeoutMobileSeconds: 1000, MaxLifetimeSeconds: 100000,
		RotateIntervalSeconds: 1, ExtendIntervalSeconds: 500, TokenCacheTTLSeconds: 10,
	})
	if err != nil {
		t.Fatalf("更新会话策略: %v", err)
	}

	sess, err := sessions.Issue(ctx, service.IssueInput{UserID: u.ID, App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	nowMs += 2000 // 越过 rotate_interval(1s)，下一次 Validate 会触发真实轮换
	res, err := sessions.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !res.Rotated {
		t.Fatal("未触发轮换，测试前提不成立")
	}

	// 过渡期内旧 token 仍存活，新旧两个 token 共享同一个会话 ID。
	rec := do(t, h, adminToken, http.MethodGet, "/admin/api/users/"+u.ID.String()+"/sessions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var list []struct {
		ID            string `json:"id"`
		IdleExpiresAt int64  `json:"idleExpiresAt"`
	}
	decode(t, rec, &list)
	if len(list) != 1 {
		t.Fatalf("设备列表返回 %d 条, want 1（新旧 token 应合并成一台设备）", len(list))
	}
	if list[0].ID != sess.ID {
		t.Fatalf("会话 ID = %q, want %q", list[0].ID, sess.ID)
	}
	// 必须留下新 token 完整的空闲窗口，而不是旧 token 即将在过渡期内到期的那个值。
	if list[0].IdleExpiresAt != res.Session.IdleExpiresAt {
		t.Fatalf("idleExpiresAt = %d, want %d（应保留新 token 的窗口，不能显示旧 token 即将过期的假象）",
			list[0].IdleExpiresAt, res.Session.IdleExpiresAt)
	}
}

// 审计日志接口此前零覆盖——它是唯一一条完全没有端到端验证的路由，
// 而计划三的控制台恰恰要绑定它返回的形状（脱敏后的标识、IP、UA、原因）。
func TestListLoginLogsOverHTTP(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	ctx := context.Background()
	u := seedUser(t, deps, "13800138000", "阿里斯")

	if err := deps.Logs.Write(ctx, domain.LoginLog{
		UserID:       &u.ID,
		IdentityType: domain.IdentityTypePhone,
		Subject:      "138****8000",
		Event:        domain.LoginEventLogin,
		Success:      false,
		Reason:       "验证码不正确",
		IP:           "203.0.113.9",
		UA:           "console-test",
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	rec := do(t, h, token, http.MethodGet,
		"/admin/api/users/"+u.ID.String()+"/login-logs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var logs []struct {
		ID           string `json:"id"`
		IdentityType string `json:"identityType"`
		Subject      string `json:"subject"`
		Event        string `json:"event"`
		Success      bool   `json:"success"`
		Reason       string `json:"reason"`
		IP           string `json:"ip"`
		UA           string `json:"ua"`
		CreatedAt    int64  `json:"createdAt"`
	}
	decode(t, rec, &logs)
	if len(logs) != 1 {
		t.Fatalf("记录数 = %d, want 1", len(logs))
	}
	l := logs[0]
	if l.Subject != "138****8000" {
		t.Errorf("Subject = %q, want 138****8000", l.Subject)
	}
	if l.Event != domain.LoginEventLogin || l.Success {
		t.Errorf("Event = %q Success = %v", l.Event, l.Success)
	}
	if l.Reason != "验证码不正确" || l.IP != "203.0.113.9" || l.UA != "console-test" {
		t.Errorf("字段缺失: %+v", l)
	}
	if l.ID == "" || l.CreatedAt == 0 {
		t.Errorf("ID / CreatedAt 未填充: %+v", l)
	}

	// 空结果必须是 []，不能是 null——前端会直接迭代它
	other := seedUser(t, deps, "13900139000", "鲍勃")
	rec = do(t, h, token, http.MethodGet,
		"/admin/api/users/"+other.ID.String()+"/login-logs", "")
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("空结果 body = %s, want []", rec.Body.String())
	}
}

func TestUserNotFound(t *testing.T) {
	h, token, _ := newAdminEnv(t)
	rec := do(t, h, token, http.MethodGet,
		"/admin/api/users/00000000-0000-0000-0000-000000000000", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
