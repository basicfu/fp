# 用户资料查询与头像 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给业务方四个接口——查询个人信息、更新个人信息、更新头像、下载头像——全部走 gRPC + SDK；登录返回里带上头像哈希；控制台能看头像、清除头像、看注册来源。

**Architecture:** 昵称、性别、头像哈希落在 `app_user`，图片本体单独放 `user_avatar`（一个用户至多一行，同事务写）；注册来源是 `user_extra.is_registration` 上的一个标记，只在登录流程自动建号那一刻写。校验（昵称、性别、图片大小与文件头）是 `internal/domain` 里的纯函数；`service.UserService` 增加资料读写与头像存取；gRPC `UserService`、管理端 HTTP、SDK `client.Users()` 都是它的薄壳。fp 对图片只存不处理。

**Tech Stack:** Go 1.26、pgx v5 / PostgreSQL 18、goose、gRPC + buf（`./scripts/gen.sh`）、chi；前端 React 19 + vitest + oxlint。不新增任何依赖。

**Spec:** `docs/superpowers/specs/2026-10-12-fp-user-profile-avatar-design.md`

## Global Constraints

- Go 测试一律用 `./scripts/test.sh [./pkg] [-run Name]`（需要 `.env.local` 里的 `FP_TEST_POSTGRES_URL` / `FP_TEST_REDIS_URL`，脚本带 `-p 1`）。**不要并行跑两个测试进程**：测试库共用，会互相 `TRUNCATE` / `FLUSHDB`。
- **迁移的 Down 不能在测试库的 `public` 上跑**：测试库是所有测试包与别的工作区共用的，中途少了列会让并行的任何东西一起坏掉。迁移往返测试自己开临时 schema（`search_path`），跑完删掉。
- **不要跑 `go mod tidy`**，本计划不新增依赖（哈希用标准库 `crypto/sha256`，类型识别手写文件头比较）。
- 用户**全局互通、不与应用关联**：任何读写都不带"该用户是否属于调用方应用"的检查，`app_user` 上不出现任何应用字段。
- fp **不处理图片**：不 import `image/*`，不解码、不裁剪、不压缩、不转格式。只有 1 MB 上限与文件头识别两件事。
- 密码哈希、`identity.credential` 永不出现在任何响应里；手机号不脱敏。
- 新错误码登记在 `internal/domain/codes.go` 的常量与 `codeSentinels` 里，HTTP 与 gRPC 的状态都从哨兵推导，不要另写映射；`TestTransportsAgreeOnEveryCode` 会校验。码一经发布不得更名。
- 分层约束有测试守着：`internal/service` 不得 import `net/http`、`grpc`、`httpapi`、`sdk/gen`；`sdk` 不得 import `internal/`，非测试代码不得 `panic`。SDK 与服务端各写一份的常量（错误码、性别、头像上限）由 `internal/integration` 里的测试对照。
- proto 产物一律经 `./scripts/gen.sh` 生成，不手写。`buf lint` 用的是 STANDARD 规则集，它要求每个 RPC 的响应是独占的 `XxxResponse`；设计里三个 RPC 都返回 `UserProfile`，用 `// buf:lint:ignore` 注释就地豁免，不改线上契约。
- 注释少而精，只写 Why。提交信息用中文 conventional commits，末尾加 `Co-Authored-By` 行。
- 前端命令在 `web/` 下执行；工作区里没有 `node_modules` 时先 `npm ci`。

## File Structure

| 文件 | 职责 |
|---|---|
| `internal/store/migrations/00018_user_avatar.sql` | `app_user.avatar_hash`、`user_extra.is_registration` + 部分唯一索引 + 回填、`user_avatar` 表；可用的 Down |
| `internal/store/migration_user_avatar_test.go` | 临时 schema 里的 Up / Down / Up 往返与回填断言 |
| `internal/domain/avatar.go` | `AvatarMaxBytes`、`AvatarHash`、`IsAvatarHash`、`CheckAvatar`（上限 + 文件头识别） |
| `internal/domain/user.go`（改） | `User.AvatarHash`、`GenderMale` / `GenderFemale` / `ValidGender`、`NormalizeNickname` |
| `internal/domain/codes.go`（改） | `NICKNAME_INVALID`、`AVATAR_INVALID`、`AVATAR_NOT_FOUND` |
| `internal/service/user.go`（改） | `userColumns` 带上 `avatar_hash`；`EnsureRegistration` 多一个 `registered` 参数 |
| `internal/service/user_profile.go` | `UserProfile`、`GetProfile`、`UpdateProfile`、`UpdateAvatar`、`GetAvatar` |
| `internal/service/auth.go`（改） | `resolveUser` 把"本次新建了用户"带出来，传给 `EnsureRegistration` |
| `proto/fp/v1/user.proto`、`sdk/gen/fp/v1/user*.pb.go` | gRPC `UserService` |
| `proto/fp/v1/auth.proto`（改） | `UserInfo.avatar_hash = 6` |
| `internal/grpcapi/user_service.go` | gRPC 薄壳：拒绝 fp-im 凭据、要求应用启用 |
| `internal/grpcapi/server.go`、`cmd/fp/main.go`（改） | `Deps.Users` 与装配 |
| `sdk/user.go` | `client.Users()`：`Get` / `Update` / `UpdateAvatar` / `Avatar`，错误映射，`ErrNotFound` |
| `sdk/middleware.go`（改） | `WriteError` 把 `ErrNotFound` 回成 404 |
| `internal/httpapi/user.go`、`router.go`（改） | `GET /admin/api/avatars/{hash}`、`DELETE /admin/api/users/{id}/avatar`，详情带头像哈希与注册来源 |
| `web/src/pages/UserDetail.tsx`、`web/src/lib/types.ts`（改） | 详情页显示头像、清除头像、注册来源 |
| `docs/user.md`、`docs/console.md`、`sdk/README.md` | 文档 |

---

### Task 1: 迁移 00018 与往返测试

**Files:** Create `internal/store/migrations/00018_user_avatar.sql`、`internal/store/migration_user_avatar_test.go`

- [x] **Step 1: 写迁移。** Up 按设计第四节。回填用 `DISTINCT ON (user_id) … ORDER BY user_id, first_login_at, application_id`，不用设计草稿里的 `first_login_at = min(…)`：两行时间并列时后者会标出两行，撞刚建的部分唯一索引，整个迁移失败、fp 起不来。Down 依次删表、删索引、删两列。
- [x] **Step 2: 写往返测试。** 在测试库里 `CREATE SCHEMA mig_<随机>`，连接池的 `search_path` 指过去，用 `goose.NewProvider` 升到 17 → 造存量数据（先 A 后 B、并列、只登录过 B、从未登录）→ 升到 18 断言回填 → 断言唯一索引拒绝第二个来源 → 降到 17 断言新列新表消失、原有行数不变 → 再 Up 断言回填一致。
- [x] **Step 3:** `./scripts/test.sh ./internal/store -run UserAvatarMigration`，PASS。

### Task 2: 领域校验与错误码

**Files:** Create `internal/domain/avatar.go`、`avatar_test.go`；Modify `internal/domain/user.go`、`user_test.go`、`codes.go`

**Interfaces（后续 Task 都用）:**
- `domain.AvatarMaxBytes`（`1 << 20`）、`domain.AvatarHash(image []byte) string`、`domain.IsAvatarHash(s string) bool`
- `domain.CheckAvatar(image []byte) (contentType string, err error)`——超限或认不出类型返回 `AVATAR_INVALID`
- `domain.NormalizeNickname(s string) (string, error)`——去首尾空白，1–32 个 rune，拒绝控制字符与 Unicode 行 / 段分隔符，不合规返回 `NICKNAME_INVALID`
- `domain.GenderMale`（`"MALE"`）、`domain.GenderFemale`（`"FEMALE"`）、`domain.ValidGender(g string) bool`（空串合法）
- `domain.CodeNicknameInvalid` / `CodeAvatarInvalid`（`ErrInvalidArgument`）、`CodeAvatarNotFound`（`ErrNotFound`）

- [x] **Step 1: 表驱动测试。** 类型识别：JPEG / PNG / WebP 的文件头被认出；GIF、SVG、空文件、随机字节、伪装成图片的可执行文件与 HTML、`RIFF` 但不是 `WEBP`、文件头不在开头被拒；恰好 1 MB 通过、1 MB + 1 字节被拒。哈希：32 位小写十六进制、同字节同哈希、固定向量。昵称与性别的合法 / 非法取值。
- [x] **Step 2: 实现。** 文件头：JPEG `FF D8 FF`；PNG `89 50 4E 47 0D 0A 1A 0A`；WebP 是 `RIFF` + 4 字节长度 + `WEBP`。
- [x] **Step 3:** `./scripts/test.sh ./internal/domain`，PASS。

> 性别取值：设计写的是"沿用 `app_user.gender` 现有约定"，但仓库里这一列此前没有任何代码写入、也没有任何文档定过取值。这里定为 `MALE` / `FEMALE` / 空串，与同一张表上 `status` 的大写枚举风格一致。

### Task 3: service 层

**Files:** Create `internal/service/user_profile.go`、`user_profile_test.go`；Modify `internal/service/user.go`、`auth.go`、`user_test.go`

**Interfaces:**
- `service.UserProfile{User domain.User; Identities []domain.Identity; RegisteredAppID, RegisteredAppName string}`
- `service.UserProfileUpdate{Nickname, Gender *string}`（nil 不改）
- `(*UserService).GetProfile(ctx, id) (*UserProfile, error)`
- `(*UserService).UpdateProfile(ctx, id, UserProfileUpdate) (*UserProfile, error)`
- `(*UserService).UpdateAvatar(ctx, id, image []byte) (*UserProfile, error)`——`image` 为空即清除
- `(*UserService).GetAvatar(ctx, hash) (contentType string, image []byte, err error)`
- `(*UserService).EnsureRegistration(ctx, userID, appID, registered bool) error`（签名变更）

- [x] **Step 1:** `userColumns` / `userColumnsPrefixed` / `scanUser` 加 `avatar_hash`。图片不进这组列。
- [x] **Step 2:** `EnsureRegistration` 写 `is_registration`，`ON CONFLICT DO NOTHING` 不指定目标（部分唯一索引也在兜底范围内）。`AuthService.resolveUser` 多返回一个 `created`，`Login` 把它传下去。
- [x] **Step 3:** `UpdateAvatar` 同一事务里先 `UPDATE app_user`（行锁让同一用户的并发换头像排队）再 upsert / delete `user_avatar`。`GetAvatar` 对形状不对的哈希直接 `AVATAR_NOT_FOUND`，不查库。
- [x] **Step 4: 测试（真实 Postgres）。** 更新、替换、清除后两张表一致；同图同哈希、不同用户同图各存一行；被拒的上传不动原头像；删用户级联删头像；旧哈希在替换后 `AVATAR_NOT_FOUND`；昵称与性别的非法输入；只带一个字段时另一个不变、一个字段非法时整次不生效；查询的标识齐全。注册来源：短信登录建的号是 A、登录 B 后不变、删除 A 后变空且不会被补记、手动建的号为空、并发首次登录两个应用恰好标一行、重复报 `registered` 不会标出两行。
- [x] **Step 5:** `./scripts/test.sh ./internal/service`，PASS。

### Task 4: proto、gRPC 服务与登录返回

**Files:** Create `proto/fp/v1/user.proto`、`internal/grpcapi/user_service.go`、`user_service_test.go`；Modify `proto/fp/v1/auth.proto`、`internal/grpcapi/server.go`、`auth_service.go`、`env_test.go`、`cmd/fp/main.go`；Generated `sdk/gen/fp/v1/user.pb.go`、`user_grpc.pb.go`、`auth.pb.go`

- [x] **Step 1:** 按设计第六节写 `user.proto`；`UserInfo` 追加 `string avatar_hash = 6`。`./scripts/gen.sh`。
- [x] **Step 2:** `userServer` 四个 RPC 共用 `authorize`：`requireNotIM` → `callerAppID` → `GetActiveByAppID`（拦截器不看应用状态）。`user_id` 不是 UUID 时归为 `USER_NOT_FOUND`。`UpdateUser` 把 proto3 optional 的指针原样传给 service。`userProfile` 转换里只给 `has_password`。
- [x] **Step 3:** `Deps.Users`（nil 时不注册），`cmd/fp/main.go` 装配；`userInfo` 带上 `AvatarHash`。
- [x] **Step 4: 测试（bufconn + 真实拦截器）。** 响应的线上字节里搜不到密码哈希、第三方凭据、unionKey；`has_password` 正确；应用 A 能查改从未在 A 登录过的用户，B 看到 A 改的值，且不因此写 `user_extra`；optional 字段的"没带"与"空串"；错误码到 gRPC code 的映射；1 MB 图片完整往返；`CallerTypeIM`、无凭据、停用应用对四个 RPC 全部被拒且不留改动；登录返回带 `avatar_hash`，`registered_app_id` 经 gRPC 可见。
- [x] **Step 5:** `./scripts/test.sh ./internal/grpcapi`，PASS。

### Task 5: SDK

**Files:** Create `sdk/user.go`、`user_test.go`、`internal/integration/user_e2e_test.go`；Modify `sdk/client.go`、`middleware.go`、`middleware_test.go`、`internal/integration/phase2_env_test.go`

**Interfaces:** 见 `sdk/user.go`——`(*Client).Users() *Users`；`Get` / `Update` / `UpdateAvatar` / `Avatar`；`UserProfile`、`UserIdentity`、`UserUpdate`；`ErrNotFound`；`CodeUserNotFound` / `CodeNicknameInvalid` / `CodeAvatarInvalid` / `CodeAvatarNotFound`；`GenderMale` / `GenderFemale`；`AvatarMaxBytes`。

- [x] **Step 1:** `translateUser`：NotFound → `ErrNotFound`、InvalidArgument → `ErrInvalidArgument`、Unauthenticated → `ErrUnauthorized`、PermissionDenied → `ErrForbidden`、Unavailable / DeadlineExceeded / Internal → `ErrUnavailable`。不能复用 `translate`：它把 NotFound 与 PermissionDenied 归进 `ErrUnauthorized`，业务方会因此清会话。
- [x] **Step 2:** `UpdateAvatar` 对超过 `AvatarMaxBytes` 的图在本地返回 `AVATAR_INVALID`、不发请求——超过 gRPC 4 MB 消息上限的请求会被传输层直接拒掉，拿不到 fp 的错误码。
- [x] **Step 3:** `WriteError` 增加 `ErrNotFound` → 404（否则落进 default 回 401）。
- [x] **Step 4: 测试。** 桩服务端：字段逐个映射、`LastLoginAt` 为 0 时是零值、nil 字段不上线、`UpdateAvatar(nil)` 发出空 image、超限本地拒绝、每个码对每个哨兵的 `errors.Is` 真值表。端到端（真实服务端 + 真实 SDK）：登录带出哈希、存取清除、错误码回到哨兵、IM 客户端被拒；SDK 与服务端各写一份的常量逐个对照。
- [x] **Step 5:** `./scripts/test.sh ./sdk ./internal/integration`，PASS。

### Task 6: 管理端接口与控制台

**Files:** Modify `internal/httpapi/user.go`、`router.go`、`user_test.go`、`web/src/lib/types.ts`、`web/src/pages/UserDetail.tsx`、`UserDetail.test.tsx`、`Users.test.tsx`

- [x] **Step 1:** `userDTO` 加 `avatarHash`、`gender`、`registeredApp{appId,name}`（只在详情给）。`GET /admin/api/avatars/{hash}` 回图片，带 `X-Content-Type-Options: nosniff` 与 `Cache-Control: private, max-age=31536000, immutable`；`DELETE /admin/api/users/{id}/avatar` 清除并返回详情。
- [x] **Step 2:** 详情页：有 `avatarHash` 时显示 `<img src="/admin/api/avatars/{hash}">` 与「清除头像」（二次确认），显示注册来源应用；列表不显示头像。
- [x] **Step 3: 测试。** HTTP：详情字段、下载的字节 / 类型 / 响应头、未登录 401、未知哈希 404、清除后取不到、重复清除不报错、列表不带 `registeredApp`。前端：有 / 无头像的渲染、确认后调用删除并重新拉取、取消不发请求、失败时提示后端原因。
- [x] **Step 4:** `./scripts/test.sh ./internal/httpapi`；`web/` 下 `npm run lint && npm test && npm run build`，全部通过。

### Task 7: 文档与收尾

- [x] `docs/user.md`（新）：三条规则、调用、错误码、业务方两个 HTTP 入口的示例、注册来源、已知限制。
- [x] `sdk/README.md`：新增「7. 用户资料与头像」，错误处理表加 `ErrNotFound`。
- [x] `docs/console.md`：页面表、头像接口、迁移 00018 的说明（回填规则、Down 会丢数据）。
- [x] 全量验证：`go build ./...`、`go vet ./...`、`./scripts/test.sh`、前端 lint / test / build。

## Self-Review

- 设计第九节的每一条测试都有落点：类型识别 → Task 2；service → Task 3；查询 → Task 3 / 4；互通 → Task 4；注册来源 → Task 3（并发首次登录并发的是"找到或建号 → 写注册关系"两步而不是整个 `Login`，因为验证码是一次性的，同一个号拿不到两个同时有效的码）；gRPC → Task 4；SDK → Task 5；迁移 → Task 1。
- 与设计的出入只有三处，都不改变设计的决定：回填 SQL 加了并列时的确定性取舍（Task 1）；性别取值在设计里没有落到具体值（Task 2）；SDK 多了一个本地的 1 MB 预检与 `ErrNotFound` 哨兵（Task 5）。
