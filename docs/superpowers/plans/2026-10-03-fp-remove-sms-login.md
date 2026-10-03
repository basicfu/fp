# 移除验证码登录体系 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 删掉短信验证码登录（`sms_code` connector、`CodeService`、`SendLoginCode` 整条链路）和旧的 `internal/notify` 发送器，让 `internal/notify` 包腾空，给通知模块（`2026-10-03-fp-notify.md`）让路。

**Architecture:** 先让所有测试脱离短信登录（生产代码不动，每个包独立保持全绿），再一次性删生产代码、proto 与示例。拆成"先测试、后生产"是为了每个 Task 结束时 `./scripts/test.sh` 都是绿的，且每个 Task 都能被单独审查。

**Tech Stack:** Go 1.25、buf/protoc-gen-go（`./scripts/gen.sh`）、React（仅文案）。

**Spec:** `docs/superpowers/specs/2026-10-03-fp-notify-design.md` 第八节"移除验证码体系"。

## Global Constraints

- Go 测试一律用 `./scripts/test.sh [./pkg] [-run Name]`，不要直接 `go test`（需要 `.env.local` 里的 `FP_TEST_POSTGRES_URL` / `FP_TEST_REDIS_URL`，脚本带 `-p 1`）。**不要并行跑两个测试进程**：测试库共用，会互相 `TRUNCATE` / `FLUSHDB`。
- **不要跑 `go mod tidy`**：阿里云 SDK 三个模块在 `internal/integration/dependency_whitelist_test.go` 的白名单里，通知模块的 `aliyun` 供应商还要用它们；tidy 会把它们摘掉，白名单测试随即失败。`go.mod` 保持不动。
- 密码登录不会自动建号：测试里要先 `UserService.EnsureUserWithIdentity` 建号再 `SetPassword`。`SetPassword` 只更新哈希，**不会**撤销会话或递增纪元，可以安全地反复调用。
- 终端用户登录从此只剩 `password` 一种；管理端账号体系（`internal/service/admin.go`）与这件事无关，不要动。
- `notify_log` 表保留（通知模块继续用）；`store.RateLimiter` 保留（删掉后生产代码里暂无使用者，但别删）。
- 注释少而精，只写 Why。
- 每个 Task 结束 commit 并 push（用户全局规则）。提交信息用中文 conventional commits，末尾按当前会话的署名要求加 `Co-Authored-By` 行。本机 git 报 "dubious ownership" 时用 `git -c safe.directory=D:/fp ...` 单次覆盖，不改全局配置。
- 批量改写一律用 `sed` / `awk`，不要用 Windows 上的 `python`（它是空壳 stub：退出码 0、零输出、文件没变，看着像成功）。

---

### Task 1: service 包测试脱离短信登录

**Files:**
- Modify: `internal/service/auth_test.go`、`internal/service/account_test.go`、`internal/service/application_test.go`、`internal/service/user_test.go`

**Interfaces:**
- Produces（本包内其他测试用）：`func (e *authEnv) passwordInput(t *testing.T, phone string) service.LoginInput`、`func (e *authEnv) passwordLogin(t *testing.T, phone string) *service.LoginResult`；`authEnv` 不再有 `sms` / `codes` 字段。

- [ ] **Step 1: 改 `auth_test.go` 的环境与登录辅助函数**

import 块删掉 `"github.com/basicfu/fp/internal/notify"`。`authEnv` 删掉 `sms`、`codes` 两个字段。`newAuthEnvWithClock` 函数体整体换成：

```go
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)

	users := service.NewUserService(pool)
	epochs := store.NewEpochStore(rdb)
	sessions := service.NewSessionServiceWithClock(
		store.NewSessionStore(rdb), store.NewRevokePublisher(rdb), epochs, now)
	logs := service.NewLoginLogService(pool)

	reg := connector.NewRegistry()
	if err := reg.Register(connector.NewPassword(users)); err != nil {
		t.Fatalf("注册 password: %v", err)
	}
	apps := service.NewApplicationService(pool, reg)

	ctx := context.Background()
	app, _, err := apps.Create(ctx, "测试应用", "test-app")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	if err := apps.SetConnector(ctx, app.ID, connector.TypePassword, true, nil); err != nil {
		t.Fatalf("启用 password: %v", err)
	}

	return &authEnv{
		auth: service.NewAuthService(service.AuthDeps{
			Apps: apps, Users: users, Sessions: sessions, Logs: logs, Registry: reg,
		}),
		apps: apps, users: users, sess: sessions, logs: logs,
		accounts: service.NewAccountService(users, sessions, epochs, logs),
		app:      app, pool: pool,
	}
```

把整个 `smsLogin` 方法替换为下面两个（`authTestPassword` 是本文件新增的常量）：

```go
const authTestPassword = "hunter2hunter2"

// passwordInput 保证手机号对应的用户存在并带固定密码，返回一份 password 登录的入参。
//
// 密码登录不会自动建号，所以先建；同一个手机号多次调用拿到的是同一个用户。
// 需要在登录之前改状态、挂钩子的用例拿这份入参自己调 e.auth.Login，不要调 passwordLogin。
func (e *authEnv) passwordInput(t *testing.T, phone string) service.LoginInput {
	t.Helper()
	ctx := context.Background()
	user, _, _, err := e.users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: phone,
	})
	if err != nil {
		t.Fatalf("EnsureUserWithIdentity: %v", err)
	}
	if err := e.users.SetPassword(ctx, user.ID, authTestPassword); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	return service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypePassword,
		Credentials:   connector.Credentials{"account": phone, "password": authTestPassword},
		IP:            "1.2.3.4", UA: "go-test",
	}
}

func (e *authEnv) passwordLogin(t *testing.T, phone string) *service.LoginResult {
	t.Helper()
	res, err := e.auth.Login(context.Background(), e.passwordInput(t, phone))
	if err != nil {
		t.Fatalf("Login(password): %v", err)
	}
	return res
}
```

IP `1.2.3.4` 与 UA `go-test` 要保持：`TestLoginWritesAuditLog` 断言了它们。

- [ ] **Step 2: 删掉只测验证码体系的用例**

在 `auth_test.go` 里按函数名整个删除：`TestSMSCodeLoginCreatesUser`、`TestPasswordAndSMSLoginResolveToSameUser`、`TestDisabledConnectorRejectsBeforeConsumingCode`、`TestSendLoginCodeRequiresEnabledConnector`、`TestSendLoginCodeRejectsMalformedPhone`、`TestRateLimitedResendDoesNotInvalidateDeliveredCode`、`TestSendLoginCodeIsRateLimitedPerPhone`。

`TestPasswordLoginDoesNotCreateUser`、`TestLoginRejectsDisabledConnector` 与账号归并无关，保留。

- [ ] **Step 3: 把其余 `smsLogin` 调用改名，再改写内联的短信登录**

先批量改名（只改调用点，方法定义已在 Step 1 里换掉）：

Run: `sed -i 's/e\.smsLogin(/e.passwordLogin(/g' internal/service/auth_test.go`

然后逐个改写"内联 `SendLoginCode` + sms_code `Login`"的用例，规则统一：把 `SendLoginCode(...)` 和用 `e.sms.LastParam("code")` 拼出来的 sms `LoginInput` 整段，换成 `e.passwordInput(t, phone)` 取得的入参；原来 `LoginInput` 里单独设过的 `IP` / `UA` 保留，写成 `in := e.passwordInput(t, phone); in.IP = "7.7.7.7"`。具体：

- `TestLoginRejectsFrozenUser`：冻结之后 `in := e.passwordInput(t, "13800138000")`，`_, err := e.auth.Login(ctx, in)`，仍断言 `domain.ErrForbidden`。
- `TestFrozenUserLoginWritesAuditLog`：同上，`in.IP = "7.7.7.7"`；后面按 `Subject == "138****8000"` 找审计记录的断言不变。
- `TestLoginRejectsFreezeCommittedBetweenCheckAndSessionWrite`：**钩子设置之前**先 `in := e.passwordInput(t, "13800138000")`（它不碰会话时钟，不会提前触发钩子），原来的 `SendLoginCode` + `Login(sms)` 换成 `in.IP = "5.5.5.5"; _, err = e.auth.Login(ctx, in)`；其余断言（在线会话数、`ErrForbidden`）不变。
- `TestLoginRejectsPasswordResetCommittedBetweenCheckAndSessionWrite`：表驱动里删掉"短信登录_压根没碰密码"这一项，只留"密码登录_校验的是旧密码"；把用例上方注释里"两种登录方式都要覆盖……sms_code"那段删掉。
- `TestDisabledApplicationBlocksAllAuthEntryPoints`：删掉 `SendLoginCode` 那条断言；`Login` 那条改成 `in := e.passwordInput(t, ...)` 后在**停用应用之前**取好，停用后 `e.auth.Login(ctx, in)` 断言 `ErrForbidden`；用例上方注释里"登录、发码、token 校验三条入口"改成"登录、token 校验两条入口"。

- [ ] **Step 4: 清掉另外三个文件里的 `sms_code` 注册**

`account_test.go`（`newAccountEnv`）、`application_test.go`（`newAppService`、`newAppServiceWith`）、`user_test.go`（`TestEnsureRegistrationIsIdempotent`）里，把

```go
	if err := reg.Register(connector.NewSMSCode(nil)); err != nil {
		t.Fatalf("注册 sms_code: %v", err)
	}
```

整块删掉；`application_test.go` 里两处写着"两个 connector"的注释（约第 20、270 行）改成"password"。

- [ ] **Step 5: 确认没有残留，并跑测试**

Run: `grep -n "SMSCode\|sms_code\|SendLoginCode\|notify\.\|smsLogin\|\.sms\b\|\.codes\b" internal/service/*_test.go`
Expected: 无输出。

Run: `./scripts/test.sh ./internal/service`
Expected: PASS。若 `go vet` 报 import 未使用，按提示删掉对应 import（`notify`、可能还有 `errors` 以外的）。

- [ ] **Step 6: Commit**

```bash
git add internal/service
git commit -m "test(service): 测试环境脱离短信登录，登录辅助函数改为密码登录"
git push
```

---

### Task 2: httpapi 与 grpcapi 包测试脱离短信登录

**Files:**
- Modify: `internal/httpapi/env_test.go`、`internal/httpapi/application_test.go`
- Modify: `internal/grpcapi/env_test.go`、`internal/grpcapi/auth_service_test.go`

**Interfaces:**
- Produces：`grpcEnv` 不再有 `sms` / `codes` 字段；`func (e *grpcEnv) loginWithPassword(t, ctx) string`（签名不变，实现改为直接建号）。

- [ ] **Step 1: httpapi**

`internal/httpapi/env_test.go`：import 删掉 `internal/notify`；`newAdminEnv` 里删掉 `codes := notify.NewCodeService(rdb)` 和 `reg.Register(connector.NewSMSCode(codes))` 整块。

`internal/httpapi/application_test.go` 里"登录方式 schema"那个用例（约第 15–35 行），把长度与顺序断言改为：

```go
	if len(schemas) != 1 {
		t.Fatalf("登录方式数 = %d, want 1", len(schemas))
	}
	if schemas[0].Type != "password" {
		t.Fatalf("登录方式 = %+v, want password", schemas)
	}
	if len(schemas[0].Fields) == 0 {
		t.Fatal("password 的 fields 为空，动态表单将渲染不出任何控件")
	}
```

- [ ] **Step 2: grpcapi 环境**

`internal/grpcapi/env_test.go`：

- import 删掉 `internal/notify`；`grpcEnv` 删掉 `sms`、`codes` 两个字段及其注释。
- `newGRPCEnv` 里删掉 `codes := notify.NewCodeService(rdb)`、`reg.Register(connector.NewSMSCode(codes))` 整块、`sms := notify.NewFakeProvider(...)` 到 `sender.AddProvider(sms)` 整段；`service.AuthDeps{...}` 去掉 `Notifier: sender, Codes: codes`；`grpcEnv{...}` 字面量去掉 `sms: sms, codes: codes`。
- `newApplication` 里启用 connector 的循环换成只启用 password：

```go
	if err := e.apps.SetConnector(ctx, app.ID, connector.TypePassword, true, nil); err != nil {
		t.Fatalf("启用 password: %v", err)
	}
```

- `loginWithPassword` 整个换成（建号与设密码没有对应的 gRPC 接口，直接调 service；登录本身仍经 gRPC，才检验得到映射层）：

```go
// loginWithPassword 走 gRPC 的完整流程：先用 service 层建一个带密码的新用户，
// 再经由 gRPC 用密码登录，返回签发的 token。
//
// ctx 须携带应用凭据（见 authed）。建号是管理端的事、没有对应的 gRPC 接口，
// 所以这两步直接调 service；登录判定路径（Login 本身）仍然全部经过 gRPC。
func (e *grpcEnv) loginWithPassword(t *testing.T, ctx context.Context) string {
	t.Helper()
	phone := randomPhone()
	const password = "hunter2hunter2"

	user, _, _, err := e.users.EnsureUserWithIdentity(context.Background(), service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: phone,
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	if err := e.users.SetPassword(context.Background(), user.ID, password); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	login, err := e.client.Login(ctx, &fpv1.LoginRequest{
		ConnectorType: connector.TypePassword,
		Credentials:   map[string]string{"account": phone, "password": password},
	})
	if err != nil {
		t.Fatalf("Login(password): %v", err)
	}
	return login.GetToken()
}
```

- [ ] **Step 2b: grpcapi 用例**

`internal/grpcapi/auth_service_test.go`：`TestSMSCodeLoginRoundTrip` 整个函数换成下面这个（后半段 `ValidateToken` 的断言原样保留；补齐 `service`、`domain` 的 import）：

```go
// TestPasswordLoginRoundTrip 走一遍登录→校验。
func TestPasswordLoginRoundTrip(t *testing.T) {
	env := newGRPCEnv(t)
	ctx := env.authed(context.Background())
	const phone = "13800138000"
	const password = "hunter2hunter2"

	user, _, _, err := env.users.EnsureUserWithIdentity(context.Background(), service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: phone,
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	if err := env.users.SetPassword(context.Background(), user.ID, password); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	login, err := env.client.Login(ctx, &fpv1.LoginRequest{
		ConnectorType: "password",
		Credentials:   map[string]string{"account": phone, "password": password},
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if login.GetToken() == "" || login.GetUser().GetId() == "" {
		t.Fatalf("登录响应缺字段: %+v", login)
	}

	res, err := env.client.ValidateToken(ctx, &fpv1.ValidateTokenRequest{Token: login.GetToken()})
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if res.GetUserId() != login.GetUser().GetId() {
		t.Fatalf("校验返回的 userId %q 与登录返回的 %q 不一致",
			res.GetUserId(), login.GetUser().GetId())
	}
	if res.GetSessionId() != login.GetSessionId() {
		t.Fatalf("校验返回的 sessionId %q 与登录返回的 %q 不一致",
			res.GetSessionId(), login.GetSessionId())
	}
}
```

`TestRPCsRequireCredentials` 与 `TestIMCallerCannotMintSessions` 里的 `SendLoginCode` 条目**这个 Task 不动**——RPC 还在，两条断言仍然成立；它们在 Task 4 随 RPC 一起删。

- [ ] **Step 3: 确认没有残留，并跑测试**

Run: `grep -n "SMSCode\|sms_code\|notify\.\|\.sms\b\|\.codes\b" internal/httpapi/*_test.go internal/grpcapi/*_test.go`
Expected: 无输出。

Run: `./scripts/test.sh ./internal/httpapi ./internal/grpcapi`
Expected: PASS。

- [ ] **Step 4: Commit**

```bash
git add internal/httpapi internal/grpcapi
git commit -m "test(httpapi,grpcapi): 测试环境脱离短信登录"
git push
```

---

### Task 3: integration 包测试脱离短信登录

**Files:**
- Modify: `internal/integration/env_test.go`、`internal/integration/phase1_test.go`、`internal/integration/phase2_env_test.go`

**Interfaces:**
- Produces：`func (e *env) passwordInput(appID, phone string) service.LoginInput`、`func (e *env) passwordLogin(appID, phone string) *service.LoginResult`（取代 `smsLogin`）；`phase2Services` 不再有 `sms` 字段；`phase2Env.loginWithPhone` 签名不变。

- [ ] **Step 1: `env_test.go`**

import 删掉 `internal/notify`；`env` 结构体删掉 `sms`；`newEnv` 里删掉 `codes := ...`、`reg.Register(connector.NewSMSCode(codes))` 整块、`sms := ...` 到 `sender.AddProvider(sms)` 整段；`AuthDeps` 去掉 `Notifier: sender, Codes: codes`；返回值字面量去掉 `sms: sms`。`createApp` 里删掉 `PUT .../connectors/sms_code` 那一行，函数注释"启用两种登录方式"改成"启用 password"。

把 `smsLogin` 整个方法换成：

```go
// passwordInput 保证手机号对应的用户存在并带固定密码，返回一份 password 登录的入参。
// 密码登录不会自动建号，所以先建；同一个手机号多次调用拿到的是同一个用户。
func (e *env) passwordInput(appID, phone string) service.LoginInput {
	e.t.Helper()
	ctx := context.Background()
	user, _, _, err := e.users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: phone,
	})
	if err != nil {
		e.t.Fatalf("EnsureUserWithIdentity: %v", err)
	}
	if err := e.users.SetPassword(ctx, user.ID, "hunter2hunter2"); err != nil {
		e.t.Fatalf("SetPassword: %v", err)
	}
	return service.LoginInput{
		AppID:         appID,
		ConnectorType: connector.TypePassword,
		Credentials:   connector.Credentials{"account": phone, "password": "hunter2hunter2"},
		IP:            "203.0.113.7", UA: "integration-test",
	}
}

func (e *env) passwordLogin(appID, phone string) *service.LoginResult {
	e.t.Helper()
	res, err := e.auth.Login(context.Background(), e.passwordInput(appID, phone))
	if err != nil {
		e.t.Fatalf("Login(password): %v", err)
	}
	return res
}
```

`domain` 若尚未 import，补上。

- [ ] **Step 2: `phase1_test.go`**

先批量改名：`sed -i 's/e\.smsLogin(/e.passwordLogin(/g' internal/integration/phase1_test.go`，然后：

- `TestPhase1EndToEnd`：
  - ③"两种登录方式已启用"改成"登录方式已启用"，`len(schemas) != 2` → `!= 1`（`want 1`）。
  - ⑤ 注释"短信验证码首次登录即注册"改成"首次登录"；`first := e.passwordLogin(appID, "13800138000")`。
  - ⑦ 整段（设密码 + 用密码登录）换成"再登录一次，必须是同一个人、不同的会话"：

```go
	// ⑦ 同一个手机号再登录一次，必须是同一个人，且签发新会话
	second := e.passwordLogin(appID, "13800138000")
	if second.User.ID != first.User.ID {
		t.Fatalf("同一手机号登录到了不同用户: %v vs %v", second.User.ID, first.User.ID)
	}
	if second.Session.Token == first.Session.Token {
		t.Fatal("两次登录应签发不同的 token")
	}
```

  - ⑩ 里 `map[string]string{"短信登录": ..., "密码登录": ...}` 的两个键改成 `"第一次登录"`、`"第二次登录"`。
  - 后面 ⑧⑨⑩ 对"两个在线会话"的断言不变（两次登录正好是两个会话）。如果 `service` 或 `connector` 的 import 因 ⑦ 被删而不再使用，按 vet 提示清理。
- 整个删除 `TestBothLoginMethodsResolveToSameUser`：只剩一种登录方式，"两种方式归并到同一用户"没有意义；跨应用共享用户由 `TestApplicationsShareUserPool` 覆盖。
- `TestFrozenUserCannotLogin`：中间"也无法重新登录"那段（`SendLoginCode` + sms `Login`）换成：

```go
	// 也无法重新登录
	if _, err := e.auth.Login(ctx, e.passwordInput(appID, "13800138000")); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("冻结用户登录 err = %v, want ErrForbidden", err)
	}
```

- `TestApplicationsShareUserPool` 不用改（改名后两次 `passwordLogin` 同手机号，`SetPassword` 幂等）。

- [ ] **Step 3: `phase2_env_test.go`**

import 删掉 `internal/notify`；`phase2Services` 删掉 `sms` 字段；`wireServices` 里删掉 `codes := ...`、`reg.Register(connector.NewSMSCode(codes))` 整块、`sms := ...` 到 `sender.AddProvider(sms)` 整段，`AuthDeps` 去掉 `Notifier: sender, Codes: codes`，返回字面量去掉 `sms: sms`；`newPhase2Env` 里启用 connector 的循环换成只启用 `connector.TypePassword`（写法同 Task 2）。

`login` 上方注释"走一次完整的短信验证码登录（发码 → 取码 → 登录）"改成"走一次完整的密码登录"。`loginWithPhone` 整个换成：

```go
// loginWithPhone 用指定手机号登录。同一个手机号多次登录得到的是**同一个
// 用户**，供需要"改完角色再登一次"的测试使用。
//
// 密码登录不会自动建号，所以先用 service 层建号并设固定密码（两步都是幂等的）；
// 登录本身仍经由 e.sdk，断言的是 SDK 到 service 层的完整链路。
func (e *phase2Env) loginWithPhone(t *testing.T, phone string) (token string, userID uuid.UUID, sessionID string) {
	t.Helper()
	ctx := context.Background()

	user, _, _, err := e.users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: phone,
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	if err := e.users.SetPassword(ctx, user.ID, "hunter2hunter2"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	res, err := e.sdk.Auth().Login(ctx, fpsdk.LoginInput{
		ConnectorType: connector.TypePassword,
		Credentials:   map[string]string{"account": phone, "password": "hunter2hunter2"},
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	uid, err := uuid.Parse(res.User.GetId())
	if err != nil {
		t.Fatalf("解析 userId %q: %v", res.User.GetId(), err)
	}
	return res.Token, uid, res.SessionID
}
```

- [ ] **Step 4: 确认没有残留，并跑测试**

Run: `grep -n "SMSCode\|sms_code\|SendLoginCode\|notify\.\|smsLogin\|\.sms\b" internal/integration/*_test.go`
Expected: 无输出（`dependency_whitelist_test.go` 里的 `dysmsapi` 白名单行是字符串，不匹配这些模式）。

Run: `./scripts/test.sh ./internal/integration`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/integration
git commit -m "test(integration): 集成测试环境脱离短信登录"
git push
```

---

### Task 4: 删除验证码体系的生产代码

**Files:**
- Delete: `internal/notify/`（整个目录，含全部 `_test.go`）、`internal/connector/smscode.go`、`internal/connector/smscode_test.go`
- Modify: `internal/connector/interface_assert_test.go`、`internal/service/auth.go`、`internal/grpcapi/auth_service.go`、`internal/grpcapi/auth_service_test.go`
- Modify: `proto/fp/v1/auth.proto`，重新生成 `sdk/gen/fp/v1/*`
- Modify: `sdk/auth.go`、`sdk/contract_test.go`
- Modify: `internal/domain/codes.go`
- Modify: `internal/config/config.go`、`internal/config/config_test.go`
- Modify: `cmd/fp/main.go`
- Modify: `examples/demo/main.go`
- Create: `internal/store/migrations/00015_remove_sms_code_connector.sql`

**Interfaces:**
- Produces（通知模块计划依赖）：`internal/notify` 包腾空（目录不存在）；`domain.CodeNotifyProviderMissing` 保留；`config.Config` 里的 `sms:` 段仍能被解析但被忽略；迁移号 `00015` 已占用，通知模块用 `00016`。

- [ ] **Step 1: 删文件，改 connector 测试**

Run:
```bash
git rm -r internal/notify internal/connector/smscode.go internal/connector/smscode_test.go
```

`internal/connector/interface_assert_test.go`：删掉 `TestCodeServiceSatisfiesCodeVerifier` 整个函数和 `internal/notify` 的 import。

- [ ] **Step 2: 去掉 `SendLoginCode`（proto、服务端、SDK）**

`proto/fp/v1/auth.proto`：删掉 service 里 `SendLoginCode` 的注释与 `rpc` 两行（含其后的空行），删掉 `SendLoginCodeRequest`、`SendLoginCodeResponse` 两个 message（含各自上方注释）；`LoginRequest.connector_type` 的注释 `例如 "password" / "sms_code"` 改成 `例如 "password"`。

Run: `./scripts/gen.sh`
Expected: 输出"生成完成。"；`git status` 里 `sdk/gen/fp/v1/auth.pb.go`、`auth_grpc.pb.go` 被改写，没有其他生成文件变化。

`internal/grpcapi/auth_service.go`：删掉 `SendLoginCode` 方法；`requireNotIM` 注释里"最关键的是 Login/SendLoginCode"改成"最关键的是 Login"。
`internal/grpcapi/auth_service_test.go`：`TestRPCsRequireCredentials` 的 `calls` 里删掉 `"SendLoginCode"` 条目；`TestIMCallerCannotMintSessions` 的用例表里删掉 `SendLoginCode` 那一项。
`sdk/auth.go`：删掉 `SendLoginCode` 方法；`ErrInvalidArgument` 注释里的"（手机号格式、验证码格式等）"改成"（参数格式不对等）"；`ErrRateLimited` 注释里的"（例如验证码发送过于频繁）"删掉；`translate` 函数注释里"这两个码只会经 SendLoginCode/Login 出现（畸形手机号/验证码格式、短信发送被限流）"改成"这两个码只会经 Login 之类带凭据的调用出现"。
`sdk/contract_test.go`：`TestAuthServiceSurface` 的 `expected` 表里删掉 `"SendLoginCode": {false, false},` 这一行（断言用 `len(expected)` 比较 RPC 个数，不用改数字），再 `gofmt -w sdk/contract_test.go` 重新对齐 map 字面量。
`sdk/middleware.go` / `sdk/middleware_test.go` / `sdk/auth_test.go` 里仅出现在注释中的 `SendLoginCode`，改成 `Login`。

- [ ] **Step 3: 去掉服务层与装配**

`internal/service/auth.go`：删掉 `internal/notify` 的 import、`LoginCodeTemplate` 常量、`AuthDeps` 里的 `Notifier`、`Codes` 两个字段、整个 `SendLoginCode` 方法；文件里提到验证码/`sms_code` 的注释（约 138、220–222、231、431 行）改成不依赖具体登录方式的表述。`connector/connector.go` 里 `Result.AllowCreate` 注释的"短信验证码登录为 true"改成"目前没有任何登录方式为 true，为将来的第三方登录保留"。

`cmd/fp/main.go`：删掉 `internal/notify` 的 import、`codeSvc := notify.NewCodeService(rdb)`、`registry.Register(connector.NewSMSCode(codeSvc))` 那一块、从 `// 短信供应商：……` 注释到 `smsSender.AddProvider(...)` 的整个 `if aliyunConfigured … else …` 块（含 `smsSender`、`aliyunConfigured`），`service.AuthDeps{...}` 里去掉 `Notifier`、`Codes` 两行。

- [ ] **Step 4: 错误码与配置**

`internal/domain/codes.go`：删掉 `CodeCodeInvalid`、`CodeSMSTemplateMissing` 两个常量（连同各自上方注释）和 `codeSentinels` 里对应的两行；`CodeNotifyProviderMissing` 保留（通知模块会复用）。

`internal/config/config.go`：删掉 `SMS` 与 `Aliyun` 两个结构体，把 `Config` 里的 `SMS SMS` 字段换成：

```go
	// LegacySMS 只为兼容：Parse 开着 KnownFields(true)，而存量系统配置的第一版是
	// seedDefaultsYAML 把默认值整份写进去的，里面带着 sms: 段。直接删字段会让这些
	// 库启动时解析失败；版本快照又不允许改写。读进来、丢掉；omitempty 保证 ToYAML
	// 不再输出它。
	LegacySMS any `yaml:"sms,omitempty"`
```

`internal/config/config_test.go`：删掉所有引用 `cfg.SMS` / `Aliyun` 的断言与用例（约第 26–32、44–48、154–156、178–180、200–210 行），并新增：

```go
// 存量库里的系统配置第一版带着 sms: 段，删掉 SMS 配置后它们仍必须能被解析。
func TestParseAcceptsLegacySMSBlock(t *testing.T) {
	yamlText := "log:\n  level: info\nsms:\n  aliyun:\n    access_key_id: \"\"\n    sign_name: x\n"
	if _, err := Parse(yamlText, "dev"); err != nil {
		t.Fatalf("Parse 应接受历史版本里的 sms 段: %v", err)
	}
}

func TestToYAMLNoLongerEmitsSMS(t *testing.T) {
	cfg, err := Parse("", "dev")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := ToYAML(cfg)
	if err != nil {
		t.Fatalf("ToYAML: %v", err)
	}
	if strings.Contains(out, "sms") {
		t.Fatalf("新生成的系统配置不该再带 sms 段:\n%s", out)
	}
}
```

（`strings` 若尚未 import，补上。）

- [ ] **Step 5: 迁移与示例**

新建 `internal/store/migrations/00015_remove_sms_code_connector.sql`：

```sql
-- +goose Up
-- 短信验证码登录方式已移除。清掉各应用里残留的启用记录，否则控制台与登录接口
-- 会遇到注册表里不存在的 connector 类型（登录返回 CONNECTOR_UNKNOWN）。用户记录不动。
DELETE FROM application_connector WHERE connector_type = 'sms_code';

-- +goose Down
-- 数据清理不可逆，没有可恢复的内容。
SELECT 1;
```

`examples/demo/main.go`：删掉整个 `POST /api/login/code` 路由；`POST /api/login` 改成密码登录：

```go
	// 公开路由：登录。
	mux.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Account  string `json:"account"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		res, err := auth.Login(r.Context(), fpsdk.LoginInput{
			ConnectorType: "password",
			Credentials:   map[string]string{"account": req.Account, "password": req.Password},
			IP:            r.RemoteAddr,
			UserAgent:     r.UserAgent(),
		})
```

（`err` 之后的 cookie 与响应体部分原样保留。）

- [ ] **Step 6: 编译、vet、全量测试**

Run: `go build ./... && go vet ./...`
Expected: 无输出。常见残留：某个文件还 import `internal/notify`、某个测试还引用 `notify.` / `CodeCodeInvalid` / `LoginCodeTemplate`——按报错逐个清掉。

Run: `grep -rn "internal/notify\|SMSCode\|SendLoginCode\|CodeService\|LoginCodeTemplate" --include=*.go . | grep -v "\.claude/"`
Expected: 无输出。

Run: `./scripts/test.sh`
Expected: 全部 PASS（含 `TestGoModDirectDependenciesAreWhitelisted`：`go.mod` 没动，阿里云三个模块仍在 require 里）。

- [ ] **Step 7: Commit**

```bash
git add -A
git status --short   # 确认只有预期的删除/修改/新增，没有 .env.local 之类
git commit -m "refactor: 移除短信验证码登录体系与旧通知发送器

删除 sms_code connector、CodeService、SendLoginCode 整条链路（proto/服务端/SDK）
和 internal/notify；system_config 里的 sms 段改为被忽略的历史字段；
迁移清掉各应用残留的 sms_code 启用记录。"
git push
```

---

### Task 5: 文档、脚本与前端文案

**Files:**
- Modify: `examples/demo/README.md`、`examples/im-demo/README.md`、`scripts/demo.sh`、`sdk/README.md`、`docs/console.md`
- Modify: `web/src/components/ConnectorsPanel.tsx`、`web/src/pages/Users.tsx`、`web/src/pages/SystemConfig.tsx`

**Interfaces:** 无（只改文案）。

- [ ] **Step 1: `examples/demo/README.md`**

- "第 1 步"标题改成"起 fp，建应用，启用 password 登录并建一个测试用户"。步骤 1c 的 curl 路径末尾 `connectors/sms_code` 改成 `connectors/password`，说明文字里的 `sms_code` 同改。1c 之后、"最后，把 `appId` / `appSecret` 追加到 `.env.local`"之前，新增 1d：

````markdown
```bash
# 1d. 建一个带密码的测试用户（密码登录不会自动建号）。
curl -s -X POST http://localhost:8080/admin/api/users \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"phone":"13800138000","password":"hunter2hunter2","nickname":"demo"}'
```

**该看到**：`201`。
````

- "第 2 步：起 demo，登录"：删掉"发验证码"的 curl、"本机没配阿里云短信凭据……"到 WARN 日志示例、"这条 WARN 日志专门为这一步……"的全部说明，登录命令换成：

````markdown
```bash
curl -is -X POST http://localhost:8090/api/login \
  -H 'Content-Type: application/json' \
  -d '{"account":"13800138000","password":"hunter2hunter2"}'
```
````

  后面"**该看到**：`200`，响应头里有一个 `Set-Cookie: fp_token=...`"那句保持不变。

- [ ] **Step 2: 其余文档与脚本**

- `examples/im-demo/README.md`：约 26、39–42 行的 `sms_code` 改成 `password`，并在该处补一句"再按 `examples/demo` 的 1d 建一个测试用户"；约 149–150 行"走法与 `examples/demo` 第 2 步相同：发验证码、从 fp 自己的 WARN 日志里抄 6 位验证码、登录换 token"改成"走法与 `examples/demo` 第 2 步相同：用测试用户的手机号和密码登录换 token"。
- `scripts/demo.sh` 顶部注释"已经启用了 sms_code"改成"已经启用了 password、建好了测试用户"。
- `sdk/README.md`：约 53 行"（含登录、发验证码、健康检查）"改成"（含登录、健康检查）"；约 95 行的接口清单里删掉 `SendLoginCode`；约 183–197 行"登录 / 登出"代码块删掉 `auth.SendLoginCode(...)`，`Login` 示例改成 `ConnectorType: "password"`、`Credentials: map[string]string{"account": account, "password": password}`，其后说明里 `sms_code 需要 phone + code` 改成 `password 需要 account + password`；约 228 行 `ErrRateLimited` 的"（验证码发送过频）"删掉。
- `docs/console.md`：约 24–27 行"（监听地址、首次管理员、阿里云短信）"里去掉"阿里云短信"，并删掉"阿里云短信四项凭据任何环境都不强制必填……后续会挪进统一的通知中心配置。"整句；约 113 行表格里"env、监听地址、首次管理员、阿里云短信"去掉"阿里云短信"。

- [ ] **Step 3: 前端文案**

- `ConnectorsPanel.tsx` 约第 14 行：删掉 `sms_code: '短信验证码登录',` 这一行（映射里没有的类型，面板按原始类型名显示）。
- `Users.tsx`：约 207 行注释"（留空只能靠验证码等其它方式登录）"改成"（留空则该用户暂时无法用密码登录）"；约 255 行 `LabelHint` 文案改成"可留空。留空的话这个用户暂时无法用密码登录，之后可在用户详情里设置。"
- `SystemConfig.tsx`：`PLACEHOLDER` 数组里删掉 `'sms:'`、`'  aliyun:'`、`'    access_key_id: ""',` 三行；约 64 行说明文字里"、阿里云短信凭据"删掉。

- [ ] **Step 4: 验证**

Run: `grep -rn "sms_code\|SendLoginCode\|验证码" --include=*.md --include=*.tsx --include=*.ts --include=*.sh . | grep -v "node_modules\|\.claude/\|docs/superpowers/"`
Expected: 无输出（`web/dist` 是被忽略的构建产物，不在此列）。

Run: `cd web && npm test && npm run build`
Expected: 全部 PASS，`tsc -b` 无报错。如果 `SystemConfig.test.tsx` 断言了占位符里的 `sms`，同步改掉。

- [ ] **Step 5: Commit**

```bash
git add examples scripts sdk/README.md docs/console.md web/src
git commit -m "docs: 示例、SDK 文档与控制台文案同步移除短信验证码登录"
git push
```

---

## Self-Review

- **Spec 覆盖**（通知设计第八节）：删除项 `notify/code.go`、`smscode.go`、`SendLoginCode`（服务/gRPC/SDK）、`LoginCodeTemplate`、`CODE_INVALID`/`SMS_TEMPLATE_MISSING`（Task 4）；`config.SMS` 不直接删而保留被忽略字段（Task 4 Step 4，含两个新测试）；`main.go` 装配（Task 4 Step 3）；迁移清 `sms_code` 启用记录（Task 4 Step 5，号 `00015`）；示例与文档（Task 4 Step 5 的 demo、Task 5）；保留项 `notify_log`、`store.RateLimiter`（全局约束）。阿里云供应商实现随 `internal/notify` 整个删除，由通知模块计划在新框架下重建。
- **类型一致**：`passwordInput` / `passwordLogin` 在 service（Task 1）与 integration（Task 3，签名多一个 `appID`）各自定义、各自使用；`loginWithPhone` 签名未变。
- **已知需要执行者现场判断的点**：各测试文件里 `go vet` 报"import 未使用"时顺手清理；`application_test.go` 里个别用例若直接引用 `connector.TypeSMSCode`（盘点时判断为不受影响），Task 1 Step 5 的 grep 会把它抓出来，按"只留 password"改。
