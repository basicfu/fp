package integration_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

// TestPhase1EndToEnd 按验收清单从头走一遍。
func TestPhase1EndToEnd(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// ① 管理员登录控制台
	if token := e.adminLogin(); token == "" {
		t.Fatal("管理员登录未拿到 token")
	}
	var me struct {
		Username string `json:"username"`
	}
	e.request(http.MethodGet, "/admin/api/me", "", http.StatusOK, &me)
	if me.Username != adminUser {
		t.Fatalf("me.username = %q, want %q", me.Username, adminUser)
	}

	// ② 建应用，拿到 appId 与仅此一次可见的 appSecret
	internalID, appID, secret := e.createApp("新项目前台", "newproj-web")
	if appID == "" || secret == "" {
		t.Fatal("appId 或 appSecret 为空")
	}

	// ③ 两种登录方式已启用，且配置元数据可供 UI 渲染表单
	var schemas []struct {
		Type   string `json:"type"`
		Fields []struct {
			Key string `json:"key"`
		} `json:"fields"`
	}
	e.request(http.MethodGet, "/admin/api/connectors", "", http.StatusOK, &schemas)
	if len(schemas) != 2 {
		t.Fatalf("登录方式数 = %d, want 2", len(schemas))
	}
	for _, s := range schemas {
		if len(s.Fields) == 0 {
			t.Fatalf("%s 的配置字段为空，管理 UI 渲染不出表单", s.Type)
		}
	}

	// ④ appSecret 可用于 SDK 身份校验
	if _, err := e.apps.VerifySecret(ctx, appID, secret); err != nil {
		t.Fatalf("VerifySecret: %v", err)
	}

	// ⑤ 短信验证码首次登录即注册
	first := e.smsLogin(appID, "13800138000")
	if first.User.Status != domain.UserStatusActive {
		t.Fatalf("新用户状态 = %q", first.User.Status)
	}

	// ⑥ 带 token 校验通过，且拿到 cache_ttl
	vr, err := e.auth.ValidateToken(ctx, appID, first.Session.Token)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if vr.CacheTTL <= 0 {
		t.Fatalf("CacheTTL = %v, 必须为正", vr.CacheTTL)
	}
	if vr.Session.UserID != first.User.ID {
		t.Fatal("token 指向的用户不对")
	}

	// ⑦ 设密码后用手机号+密码登录，必须是同一个人
	if err := e.users.SetPassword(ctx, first.User.ID, "hunter2hunter2"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	second, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         appID,
		ConnectorType: connector.TypePassword,
		Credentials:   connector.Credentials{"account": "13800138000", "password": "hunter2hunter2"},
	})
	if err != nil {
		t.Fatalf("Login(password): %v", err)
	}
	if second.User.ID != first.User.ID {
		t.Fatalf("账号归并失败: %v vs %v", second.User.ID, first.User.ID)
	}

	// ⑧ 管理端能看到这个用户与他的两个在线会话
	var list struct {
		Items []struct {
			ID       string `json:"id"`
			Nickname string `json:"nickname"`
		} `json:"items"`
		Total int `json:"total"`
	}
	e.request(http.MethodGet, "/admin/api/users?keyword=13800138000", "", http.StatusOK, &list)
	if list.Total != 1 {
		t.Fatalf("用户搜索 total = %d, want 1", list.Total)
	}
	userPath := "/admin/api/users/" + first.User.ID.String()

	var sessions []struct {
		ID string `json:"id"`
		IP string `json:"ip"`
	}
	e.request(http.MethodGet, userPath+"/sessions", "", http.StatusOK, &sessions)
	if len(sessions) != 2 {
		t.Fatalf("在线会话数 = %d, want 2", len(sessions))
	}

	// ⑨ 审计日志记录了两次成功登录，手机号已脱敏
	var auditLogs []struct {
		Subject string `json:"subject"`
		Success bool   `json:"success"`
		IP      string `json:"ip"`
	}
	e.request(http.MethodGet, userPath+"/login-logs", "", http.StatusOK, &auditLogs)
	if len(auditLogs) < 2 {
		t.Fatalf("审计记录数 = %d, want >= 2", len(auditLogs))
	}
	for _, l := range auditLogs {
		if l.Subject == "13800138000" {
			t.Fatal("审计日志里出现了未脱敏的手机号")
		}
	}

	// ⑩ 管理端踢下线，两个会话全部失效
	var revoked struct {
		Revoked int `json:"revoked"`
	}
	e.request(http.MethodDelete, userPath+"/sessions", "", http.StatusOK, &revoked)
	if revoked.Revoked != 2 {
		t.Fatalf("撤销数 = %d, want 2", revoked.Revoked)
	}
	for name, tok := range map[string]string{"短信登录": first.Session.Token, "密码登录": second.Session.Token} {
		if _, err := e.auth.ValidateToken(ctx, appID, tok); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("%s 的 token 仍有效, err = %v", name, err)
		}
	}

	_ = internalID
}

func TestBothLoginMethodsResolveToSameUser(t *testing.T) {
	e := newEnv(t)
	e.adminLogin()
	_, appID, _ := e.createApp("A", "a")
	ctx := context.Background()

	first := e.smsLogin(appID, "13800138000")
	if err := e.users.SetPassword(ctx, first.User.ID, "hunter2hunter2"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	// 手机号 + 密码
	byPhone, err := e.auth.Login(ctx, service.LoginInput{
		AppID: appID, ConnectorType: connector.TypePassword,
		Credentials: connector.Credentials{"account": "13800138000", "password": "hunter2hunter2"},
	})
	if err != nil {
		t.Fatalf("手机号密码登录: %v", err)
	}

	// 再给他加一个用户名，用用户名 + 同一个密码登录
	if _, err := e.users.AttachIdentity(ctx, first.User.ID, service.EnsureIdentityInput{
		Type: domain.IdentityTypeUsername, Subject: "alice",
	}); err != nil {
		t.Fatalf("AttachIdentity: %v", err)
	}
	byUsername, err := e.auth.Login(ctx, service.LoginInput{
		AppID: appID, ConnectorType: connector.TypePassword,
		Credentials: connector.Credentials{"account": "alice", "password": "hunter2hunter2"},
	})
	if err != nil {
		t.Fatalf("用户名密码登录: %v", err)
	}

	if byPhone.User.ID != first.User.ID || byUsername.User.ID != first.User.ID {
		t.Fatal("三种登录路径没有落到同一个用户")
	}
}

func TestTokenValidationAndRejection(t *testing.T) {
	e := newEnv(t)
	e.adminLogin()
	_, appID, _ := e.createApp("A", "a")
	ctx := context.Background()

	res := e.smsLogin(appID, "13800138000")

	if _, err := e.auth.ValidateToken(ctx, appID, res.Session.Token); err != nil {
		t.Fatalf("有效 token 应通过: %v", err)
	}
	for name, tok := range map[string]string{
		"空 token":   "",
		"伪造 token":  "definitely-not-a-real-token",
		"截断的 token": res.Session.Token[:len(res.Session.Token)-1],
	} {
		if _, err := e.auth.ValidateToken(ctx, appID, tok); !errors.Is(err, domain.ErrUnauthorized) {
			t.Errorf("%s 应被拒绝, err = %v", name, err)
		}
	}

	// 另一个应用的 appId 校验同一个 token 也必须失败
	_, otherAppID, _ := e.createApp("B", "b")
	if _, err := e.auth.ValidateToken(ctx, otherAppID, res.Session.Token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("跨应用校验应被拒绝, err = %v", err)
	}
}

func TestAppSecretVerification(t *testing.T) {
	e := newEnv(t)
	e.adminLogin()
	_, appID, secret := e.createApp("A", "a")
	ctx := context.Background()

	if _, err := e.apps.VerifySecret(ctx, appID, secret); err != nil {
		t.Fatalf("正确凭据: %v", err)
	}
	if _, err := e.apps.VerifySecret(ctx, appID, "wrong"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("错误 secret err = %v, want ErrInvalidCredential", err)
	}
	// 未知 appId 与错误 secret 返回同一错误，避免 appId 枚举
	if _, err := e.apps.VerifySecret(ctx, "nope", secret); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("未知 appId err = %v, want ErrInvalidCredential", err)
	}
}

func TestKickFromAdminInvalidatesToken(t *testing.T) {
	e := newEnv(t)
	e.adminLogin()
	_, appID, _ := e.createApp("A", "a")
	ctx := context.Background()

	res := e.smsLogin(appID, "13800138000")
	userPath := "/admin/api/users/" + res.User.ID.String()

	var sessions []struct {
		ID string `json:"id"`
	}
	e.request(http.MethodGet, userPath+"/sessions", "", http.StatusOK, &sessions)
	if len(sessions) != 1 {
		t.Fatalf("会话数 = %d, want 1", len(sessions))
	}

	var revoked struct {
		Revoked int `json:"revoked"`
	}
	e.request(http.MethodDelete, userPath+"/sessions/"+sessions[0].ID, "", http.StatusOK, &revoked)
	if revoked.Revoked != 1 {
		t.Fatalf("撤销数 = %d, want 1", revoked.Revoked)
	}
	if _, err := e.auth.ValidateToken(ctx, appID, res.Session.Token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("踢下线后 token 仍有效, err = %v", err)
	}
}

// 撤销必须广播事件——计划二的 gRPC 双向流靠它把撤销实时推给 SDK。
func TestRevocationIsBroadcast(t *testing.T) {
	e := newEnv(t)
	e.adminLogin()
	_, appID, _ := e.createApp("A", "a")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, closeFn, err := e.revoke.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer closeFn()

	res := e.smsLogin(appID, "13800138000")
	userPath := "/admin/api/users/" + res.User.ID.String()
	e.request(http.MethodDelete, userPath+"/sessions", "", http.StatusOK, nil)

	select {
	case ev := <-events:
		if len(ev.UserIDs) != 1 || ev.UserIDs[0] != res.User.ID {
			t.Fatalf("事件 UserIDs = %v, want [%v]", ev.UserIDs, res.User.ID)
		}
		if len(ev.Tokens) == 0 {
			t.Fatal("事件未携带 token 列表，SDK 无从知道该清哪些缓存条目")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("3 秒内未收到撤销广播")
	}
}

func TestFrozenUserCannotLogin(t *testing.T) {
	e := newEnv(t)
	e.adminLogin()
	_, appID, _ := e.createApp("A", "a")
	ctx := context.Background()

	res := e.smsLogin(appID, "13800138000")
	userPath := "/admin/api/users/" + res.User.ID.String()

	e.request(http.MethodPatch, userPath+"/status", `{"status":"FROZEN"}`, http.StatusOK, nil)

	// 冻结连带踢掉已登录设备
	if _, err := e.auth.ValidateToken(ctx, appID, res.Session.Token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("冻结后旧 token 仍有效, err = %v", err)
	}
	// 也无法重新登录
	if err := e.auth.SendLoginCode(ctx, appID, "13800138000"); err != nil {
		t.Fatalf("SendLoginCode: %v", err)
	}
	if _, err := e.auth.Login(ctx, service.LoginInput{
		AppID: appID, ConnectorType: connector.TypeSMSCode,
		Credentials: connector.Credentials{"phone": "13800138000", "code": e.sms.LastParam("code")},
	}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("冻结用户登录 err = %v, want ErrForbidden", err)
	}

	// 解冻后恢复
	e.request(http.MethodPatch, userPath+"/status", `{"status":"ACTIVE"}`, http.StatusOK, nil)
	if again := e.smsLogin(appID, "13800138000"); again.User.ID != res.User.ID {
		t.Fatal("解冻后登录不是同一用户")
	}
}

// cache_ttl 是 fp 与 SDK 之间的核心契约：SDK 只按它缓存判定结果，
// 永远不自己推断 token 是否过期。这里验证 fp 侧的下发规则正确。
func TestCacheTTLGuidesSDKBehaviour(t *testing.T) {
	e := newEnv(t)
	e.adminLogin()
	internalID, appID, _ := e.createApp("A", "a")
	ctx := context.Background()

	// 把缓存窗口调成 60 秒、空闲超时调成 120 秒
	e.request(http.MethodPut, "/admin/api/applications/"+internalID+"/session",
		`{"idleTimeoutSeconds":120,"idleTimeoutMobileSeconds":120,"maxLifetimeSeconds":3600,`+
			`"rotateIntervalSeconds":3600,"extendIntervalSeconds":30,"tokenCacheTtlSeconds":60}`,
		http.StatusOK, nil)

	res := e.smsLogin(appID, "13800138000")
	vr, err := e.auth.ValidateToken(ctx, appID, res.Session.Token)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if vr.CacheTTL != 60*time.Second {
		t.Fatalf("CacheTTL = %v, want 60s", vr.CacheTTL)
	}
	// cache_ttl 绝不能超过 token 剩余有效期
	remaining := vr.Session.RemainingAt(time.Now().UnixMilli(), domain.SessionPolicy{
		IdleTimeoutSeconds: 120, MaxLifetimeSeconds: 3600, TokenCacheTTLSeconds: 60,
		RotateIntervalSeconds: 3600, ExtendIntervalSeconds: 30,
	})
	if vr.CacheTTL > remaining {
		t.Fatalf("CacheTTL(%v) 超过了剩余有效期(%v)", vr.CacheTTL, remaining)
	}
}

// 未启用的登录方式必须被拒绝，且拒绝发生在凭据校验之前。
func TestDisabledConnectorIsRejected(t *testing.T) {
	e := newEnv(t)
	e.adminLogin()
	internalID, appID, _ := e.createApp("A", "a")
	ctx := context.Background()

	res := e.smsLogin(appID, "13800138000")
	if err := e.users.SetPassword(ctx, res.User.ID, "hunter2hunter2"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	e.request(http.MethodPut, "/admin/api/applications/"+internalID+"/connectors/password",
		`{"enabled":false,"config":{}}`, http.StatusNoContent, nil)

	if _, err := e.auth.Login(ctx, service.LoginInput{
		AppID: appID, ConnectorType: connector.TypePassword,
		Credentials: connector.Credentials{"account": "13800138000", "password": "hunter2hunter2"},
	}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden（即使密码正确也应拒绝）", err)
	}
}

// 多个应用挂在同一个 fp 部署下即共享用户体系——这是"多平台共享用户"的落点。
func TestApplicationsShareUserPool(t *testing.T) {
	e := newEnv(t)
	e.adminLogin()
	_, appA, _ := e.createApp("平台A", "plat-a")
	_, appB, _ := e.createApp("平台B", "plat-b")

	inA := e.smsLogin(appA, "13800138000")
	inB := e.smsLogin(appB, "13800138000")

	if inA.User.ID != inB.User.ID {
		t.Fatalf("同一手机号在两个应用下应是同一个用户: %v vs %v", inA.User.ID, inB.User.ID)
	}
	// 但会话彼此独立
	if inA.Session.ID == inB.Session.ID {
		t.Fatal("两个应用的会话不应是同一个")
	}
	ctx := context.Background()
	if _, err := e.auth.ValidateToken(ctx, appA, inB.Session.Token); err == nil {
		t.Fatal("B 的 token 不应能在 A 上通过")
	}
}
