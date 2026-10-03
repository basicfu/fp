# fp 管理端账号设计

**日期**：2026-10-03
**状态**：设计已定；实施计划：`docs/superpowers/plans/2026-10-03-fp-admin-account.md`
**上游**：`2026-09-21-fp-system-config-design.md`（`bootstrap_admin` 兜底 `admin/admin`）

---

## 一、要做什么

控制台只有一个超级管理员（`admin` 表至今没有任何增删接口，这点不变），登录方式只有账号密码。本次补三件事：默认密码的提示与自助改账号、忘记密码的恢复路径、一个"改名后必现"的后门漏洞。

## 二、决策

| 项 | 决策 | 理由 |
|---|---|---|
| 默认账号 | 首次启动（`admin` 表为空）固定创建 `admin` / `admin`，现状已是（`resolveBootstrapAdmin` 不动）。用户名与 bcrypt 密码哈希存数据库 `admin` 表；`bootstrap_admin` 系统配置只决定空表时的初始值，不是账号本身 | 首次部署要能直接登录，初始值固定、不随机 |
| 默认密码提示 | **不强制**。登录成功且密码恰为 `admin` 时，登录响应带 `defaultPassword: true`，前端弹一条可关闭的 toast（带"去修改"） | 判断依据是登录时拿到的明文，不需要库里加标志位，也不会过期失真 |
| 自助改账号 | 右上角用户菜单新增「修改密码」，对话框：登录名（预填，可改）、旧密码、新密码、确认新密码；直接提交，无二次确认 | 登录名并进同一个对话框；新密码留空表示只改登录名 |
| 忘记密码 | **只有** `./fp reset-password` 一条路，不提供邮件/短信/界面重置，配了通知渠道也不用。重置后用户名固定 `admin`、**密码随机** | 不依赖通知模块是否配好（鸡生蛋）；能在服务器上执行命令的人本来就拥有该实例；重置不回落到众所周知的 `admin` 密码，恢复账号的瞬间不会暴露一个人人知道的密码 |
| 密码规则 | 不做复杂度规则，只拒绝空值和超过 72 字节（沿用 `maxPasswordBytes`） | bcrypt 上限 |

## 三、先修一个会被"改名"放大的漏洞

`AdminService.EnsureBootstrap` 现在是 `ON CONFLICT (username) DO NOTHING`——靠"用户名冲突"判断管理员已存在。支持改登录名后，管理员把 `admin` 改成 `root`，下次重启 `admin` 不再冲突，会**再插入一个已知密码的 `admin/admin`**。

改成"整张 `admin` 表为空才创建"：

```sql
INSERT INTO admin (username, password_hash, display_name)
SELECT $1, $2, $1 WHERE NOT EXISTS (SELECT 1 FROM admin)
ON CONFLICT (username) DO NOTHING
```

末尾的 `ON CONFLICT` 兜住两个实例同时首次启动的竞态。

## 四、会话作废：一个 epoch 计数器

改账号和 `reset-password` 都要作废该管理员的**全部**会话。管理端会话是 Redis 里的 `fp:admin:tok:{token}`，没有按管理员的索引；`SCAN` 前缀要遍历整个共享 Redis（终端用户会话也在里面），不取。

改成单个计数器 `fp:admin:epoch`：token payload 从 `id|username` 变为 `id|username|epoch`，`Authenticate` 多一次 `GET` 比对，不一致即失效；作废全部会话 = `INCR`。升级后旧 token 因缺 epoch 字段失效一次，管理员重新登录。

## 五、改账号接口

`PUT /admin/api/me`，请求体 `{ username, oldPassword, newPassword }`：

- `oldPassword` 必填且必须匹配；`username` 去空白后 1–64 字符；`newPassword` 非空时 ≤ 72 字节，空表示不改密码。
- 同一条 `UPDATE` 写 `username`、`password_hash`、`display_name`（跟随 `username`）、`updated_at`。
- 成功后 `INCR` epoch，并为**当前请求**重新签发 token（写 cookie）——其他会话全部失效，当前浏览器保持登录。
- 旧密码错误返回新增的 `ADMIN_OLD_PASSWORD_WRONG`，映射 `ErrInvalidArgument`（400）。**不能复用** `ADMIN_CREDENTIAL_INVALID`：它映射 401，前端 `api.ts` 对任何 401 都会清登录态并跳登录页，输错旧密码会被踢出去。
- 新错误码登记在 `internal/domain/codes.go` 的常量与 `codeSentinels` 里，HTTP 与 gRPC 的状态都从哨兵推导，不用另写映射；`TestTransportsAgreeOnEveryCode` 校验两个传输层对每个码一致。

## 六、`reset-password` 子命令

`cmd/fp/main.go` 的 `main()` 在 `run()` 之前看 `os.Args[1]`：`reset-password` 走 `runResetPassword()`；其他参数打印用法、退出码 2（现在多余参数被静默忽略，敲错子命令会直接把服务起起来，改成显式报错）。

`runResetPassword`：

1. 读 `FP_POSTGRES_URL` / `FP_REDIS_URL`（与服务进程同一组环境变量），不读系统配置——系统配置 YAML 坏了也不影响恢复账号。
2. 先 `store.Migrate`（幂等），新版本二进制对旧库也能跑。
3. 把唯一管理员（`created_at` 最早的一行）置为用户名 `admin`、**随机密码**、`status=ACTIVE`；表为空则创建。随机密码 16 位，来自 `crypto/rand`，字符集去掉易混字符（`0 O 1 l I`），只以 bcrypt 哈希入库。用户名是内置常量，不读 `bootstrap_admin`。
4. `INCR` epoch，作废全部会话。
5. 打印（密码只会在这里出现这一次）：

```
管理员账号已重置，所有管理端会话已作废：
  用户名：admin
  密码：k7Qd3mXz9RtVw2Pb
```

同时打印用户名是因为登录名可改，忘记密码的人往往也忘了登录名；重置会把用户名一并恢复成 `admin`。表为空时先跑 `reset-password` 再启动服务也成立：服务启动发现表非空，不会再造 `admin/admin`。

**容器内**：镜像里二进制叫 `bootstrap`（`scripts/build.sh` 把 `./cmd/fp` 编译成 `bootstrap`，`Dockerfile` 是 `CMD ["./bootstrap"]`），所以是：

```bash
docker exec <容器名> ./bootstrap reset-password
```

`docker exec` 继承容器创建时的环境变量，`FP_POSTGRES_URL` / `FP_REDIS_URL` 现成可用。本机直接 `./fp reset-password`。

## 七、前端

- `Layout.tsx` 右上角下拉菜单在"退出"上方加「修改密码」，打开 `ChangeAccountDialog`（shadcn `Dialog`，仓库里已有）；新密码两次一致由前端校验。
- `auth.tsx` 的 `login()` 返回 `{ defaultPassword }`；`Login.tsx` 成功后若为 true，弹 sonner toast（"当前仍在使用默认密码，建议修改"，带"去修改"，关闭即忽略），"去修改"打开同一个对话框。
- 改成功：toast 提示，对话框关闭，用户菜单里的登录名立即更新。

## 八、明确不做

| 项 | 一句话理由 |
|---|---|
| 强制首次改密 | 明确要求非强制，可忽略 |
| 邮件/短信/界面找回密码 | 明确要求只有 `reset-password` 一条路 |
| 多管理员、角色 | 单超管 |
| 管理端登录失败限流 | 已知缺口：固定默认密码暴露在公网是真实风险，部署上需放在内网或由反向代理限制；本期不做 |
| 去掉 `bootstrap_admin` 系统配置项 | 它仍决定"空库首次创建谁"，本期不动，只是 `reset-password` 不读它 |

## 九、改动面

`internal/service/admin.go`（`EnsureBootstrap`、epoch、改账号、重置）、`internal/httpapi/admin.go` 与 `router.go`（`PUT /me`、登录响应）、`internal/domain/codes.go`、`cmd/fp/main.go`、`web/src/components/Layout.tsx`、`web/src/pages/Login.tsx`、`web/src/lib/auth.tsx`、新增 `ChangeAccountDialog`、`docs/console.md`（"得直接改库"那句改为指向 `reset-password`）。
