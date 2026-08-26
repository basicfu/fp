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
}

// service.SessionService.ListByUser 故意不按会话 ID 去重（轮换过渡期内新旧
// token 各算一条，这是它的既定行为，见 session_rotate_test.go 的
// TestRotationDoesNotDuplicateDeviceEntry）；去重是 HTTP 层 listSessions 的职责。
// 这里用假时钟直接触发一次真实轮换，验证在线设备列表确实把新旧 token 合并成了一台设备。
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

	deps := httpapi.Deps{
		Admin:    admin,
		Apps:     service.NewApplicationService(pool),
		Users:    service.NewUserService(pool),
		Sessions: sessions,
		Logs:     service.NewLoginLogService(pool),
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
		ID string `json:"id"`
	}
	decode(t, rec, &list)
	if len(list) != 1 {
		t.Fatalf("设备列表返回 %d 条, want 1（新旧 token 应合并成一台设备）", len(list))
	}
	if list[0].ID != sess.ID {
		t.Fatalf("会话 ID = %q, want %q", list[0].ID, sess.ID)
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
