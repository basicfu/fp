# fp 接入示例

一个真的能跑起来的最小业务服务，接入 fp（Foundation Platform，账号与
会话中心）。它同时是三样东西：

1. **接入文档**——业务方照着 `main.go` 抄，就是接入 fp 需要写的全部代码。
2. **手工验收工具**——尤其是下面第 4 步："杀掉 fp 之后业务仍然可用"，
   这是 fp 第二阶段（gRPC 长连接 + 本地缓存 + 推送式撤销）整个存在的理由。
3. **"接入 fp 到底要写多少代码"的诚实答案**——`main.go` 只做三件事：读
   环境变量、`fpsdk.New`、注册三个路由。如果它比这更长，说明是 SDK 的
   API 设计有问题，该去改 SDK，不该让每个业务方重复写胶水代码。

## 路由

| 路由 | 说明 |
|---|---|
| `POST /api/login/code` | 给手机号发登录验证码。body: `{"phone":"13800138000"}` |
| `POST /api/login` | 用手机号 + 验证码换 token。body: `{"phone":"...","code":"..."}`。成功后响应体带 `token`，并顺手把它写进一个 cookie |
| `GET /api/me` | 受保护路由，`auth.Middleware` 一行接入。返回 `{"userId","sessionId","stale"}` |
| `GET /healthz` | 暴露 `client.StreamHealthy()`——撤销推送流是否健康，手工验收第 4 步要看它 |

`GET /api/me` 接受 `Authorization: Bearer <token>`，也接受名为 `fp_token`
的 cookie（`fpsdk.DefaultCookieName`）。下面的手工验收统一用 Bearer 头，
因为 curl 用它比维护 cookie jar 省事；浏览器接入时用 cookie 更自然。

## 前置条件

- fp 能跑起来：局域网 PostgreSQL / Redis 可达，仓库根目录有 `config.yaml`
  （从 `config.example.yaml` 复制、按注释填好 PG/Redis 那两条连接串）。
- 本机没有 Docker、没有 make：全部用 bash 脚本 + `go run`，`./scripts/*.sh`
  在 Git Bash 里执行。

## 环境变量

`main.go` 只读这四个：

| 变量 | 说明 |
|---|---|
| `FP_ADDR` | fp 的 gRPC 地址，如 `127.0.0.1:9090` |
| `FP_APP_ID` / `FP_APP_SECRET` | 应用凭据，来自下面手工验收第 1 步 |
| `FP_INSECURE` | 设为 `1` 时用明文连接。**只能用于本地开发** |

`FP_APP_ID` / `FP_APP_SECRET` 是凭据，和 PG/Redis 密码一样只能进
git-ignored 的 `.env.local`，不要提交到 git。`scripts/demo.sh` 先
`. scripts/env.sh` 从 `.env.local` 载入这两个变量再起 demo（`scripts/run.sh`
不再走这条路——fp 自己的配置已经全在 `config.yaml` 里）；`FP_ADDR` / `FP_INSECURE` 在
`scripts/demo.sh` 里给了本地开发的默认值（`127.0.0.1:9090` /`1`），不用
额外配置。

**`FP_INSECURE=1` 只能用于本地开发。** 生产环境绝不能开：appSecret 会
随每个 RPC 以明文发送在网络上，明文连接等于把它直接印在网线上。生产
环境要给 fp 配真实证书，SDK 侧不要设这个环境变量（`Insecure` 零值就是
`false`）。

## 手工验收四步

按顺序做，每一步都写清楚了**该看到什么**。

### 第 1 步：起 fp，建应用，启用 sms_code

> 从第三阶段起，下面这些准备步骤都可以在管理控制台里点完，不必用 curl：
> 先 `./scripts/build-web.sh` 构建前端，再照下面一样 `./scripts/run.sh` 起服务，
> 浏览器打开 http://localhost:8080/ 即是控制台（账号取自 `config.yaml` 的
> `bootstrap_admin`，`config.example.yaml` 给的是 `admin` / `admin`）。
> 详见 [docs/console.md](../../docs/console.md)。
> curl 的写法保留在这里，供脚本化和排障使用。

```bash
./scripts/run.sh
```

看到日志里的 `"fp 启动"`（带 `http`/`grpc` 两个监听地址）即成功。
首次启动建的平台管理员来自 `config.yaml` 的 `bootstrap_admin.user` /
`bootstrap_admin.password`；`config.example.yaml` 给的是 `admin` / `admin`。

另开一个终端，用管理员账号登录管理 API、建一个应用、启用 `sms_code`：

```bash
# 1a. 管理员登录，拿到管理端 token。
curl -s -X POST http://localhost:8080/admin/api/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin"}'
```

**该看到**：`200`，响应体形如
`{"token":"<管理端 token>","username":"admin"}`。把 `token` 存进变量：

```bash
ADMIN_TOKEN=<上面拿到的 token>
```

```bash
# 1b. 建应用。
curl -s -X POST http://localhost:8080/admin/api/applications \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Demo","slug":"demo"}'
```

**该看到**：`201`，响应体形如：

```json
{
  "application": {"id": "<uuid>", "appId": "<appId>", "status": "ACTIVE", "...": "..."},
  "appSecret": "<appSecret，只在这一次响应里出现，之后无法读回>"
}
```

记下 `application.id`（下一步要用）、`application.appId` 与
`appSecret`（写进 `.env.local` 要用）。

```bash
# 1c. 启用 sms_code 登录方式（把 <id> 换成上一步的 application.id）。
curl -s -o /dev/null -w '%{http_code}\n' \
  -X PUT "http://localhost:8080/admin/api/applications/<id>/connectors/sms_code" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"enabled":true,"config":{}}'
```

**该看到**：`204`。

最后，把 `appId` / `appSecret` 追加到仓库根目录的 `.env.local`：

```bash
FP_APP_ID=<application.appId>
FP_APP_SECRET=<appSecret>
```

### 第 2 步：起 demo，登录

```bash
./scripts/demo.sh
```

**该看到**：`demo 监听 :8090，fp 地址 127.0.0.1:9090`。

发验证码：

```bash
curl -s -X POST http://localhost:8090/api/login/code \
  -H 'Content-Type: application/json' \
  -d '{"phone":"13800138000"}'
```

**该看到**：`{"ok":true}`。

本机没配阿里云短信凭据，fp 会用内存假供应商——验证码不会真的发到手机
上，而是以 **WARN** 级别打在 **fp 自己的进程日志**里（不是 demo 的日志），
形如：

（如果你的 `.env.local` 里 `FP_ALIYUN_*` 四项凑巧填了值——比如复用了别的
手工验证场景留下的占位凭据——fp 会走真实阿里云供应商分支，不会打这条
日志。这一步只是想验证"接入 fp 能登录"，不是在测阿里云通道，把这四项
先注释掉或清空、重启 fp，就会回到假供应商路径。）

```json
{"time":"...","level":"WARN","msg":"notify: 假供应商收到一条消息——这不是真短信，不会真的送达，仅用于本地开发/手工验收；验证码就在 params 里","channel":"sms","to":"13800138000","template":"login_code","params":{"code":"123456"}}
```

这条 WARN 日志专门为这一步的手工验收而加：`FakeProvider` 本身只把消息
留在内存里，进程外读不到，验证码除了这条日志没有别的地方能看到。
门控条件是"用的是假供应商"而不是"非生产环境"——真配了阿里云凭据的部署
（哪怕不是生产环境）永远不会走到这条日志，不存在真验证码被打进日志的
风险；反过来，如果拿"非生产环境"当门控，一个非生产但配齐了阿里云凭据、
走真实短信通道的部署也会命中，那就是真实验证码泄露到日志里了。

把 `params` 里的 6 位 `code` 抄出来，登录：

```bash
curl -is -X POST http://localhost:8090/api/login \
  -H 'Content-Type: application/json' \
  -d '{"phone":"13800138000","code":"<上面抄的验证码>"}'
```

**该看到**：`200`，响应头里有一个 `Set-Cookie: fp_token=...`，响应体形如
`{"token":"...","sessionId":"...","userId":"..."}`。把 `token` 存进变量，
后面几步要用：

```bash
TOKEN=<上面拿到的 token>
```

### 第 3 步：带 token 与不带 token 访问受保护路由

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8090/api/me \
  -H "Authorization: Bearer $TOKEN"
```

**该看到**：`200`。

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8090/api/me
```

**该看到**：`401`——没带 token，`auth.Middleware` 直接拒绝，这一步根本
不会有任何请求打到 fp。

### 第 4 步：杀掉 fp，业务仍然可用

**这一步是 fp 第二阶段的价值所在。** fp 是校验 token 的旁路服务，不应该
成为业务方每条请求路径上的强依赖——SDK 的本地缓存 + gRPC 长连接上的
推送式撤销，就是为了让 fp 的短暂不可用不至于让所有已登录用户瞬间掉线。

先找到 fp 的 PID（另开一个终端）：

```bash
tasklist //FI "IMAGENAME eq fp.exe"
```

**先刷新一次缓存，紧接着立刻杀掉 fp。** 缓存条目的"新鲜度"是从它被写入
的那一刻算起，不是从 fp 挂掉的那一刻算起——如果你在第 3 步和这一步之间
停下来读了一会儿文档，缓存条目可能已经缓存了不止 `DegradedCacheTTL`
（默认 5 秒）那么久，杀掉 fp 后第一次请求就可能直接看到 503，而不是下面
期望的 200。把刷新和杀进程写成一条命令，中间不要停顿：

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8090/api/me \
  -H "Authorization: Bearer $TOKEN" \
  && taskkill //F //PID <fp 的 PID> \
  && curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8090/api/me \
       -H "Authorization: Bearer $TOKEN"
```

本仓库的开发环境是 Windows + Git Bash：Git Bash 的 `kill` 只认识它自己
（MSYS）跟踪的进程，对着 `tasklist`/任务管理器给出的原生 Windows PID 会
报 "No such process"——这不是权限问题，得用 `taskkill //F //PID` 才能杀掉
一个原生 Windows PID。真正的 POSIX 环境（Linux/macOS）用 `kill -9 <pid>`
即可，PID 直接来自 `ps`/`pgrep`，没有这层 PID 命名空间不一致的问题。

（如果 `run.sh` 是前台跑的，找不到 PID 就去那个终端 `Ctrl+C`，
效果一样，只是没法和上面这条命令拼在一起，得手动尽快切回来重新请求。）

**该看到**：两次请求都是 `200`。第二次这个身份是从 demo 进程本地的缓存
里直接读出来的——`Validate` 命中缓存时是同步返回，不产生任何网络调用，
此刻 fp 已经不在监听了，任何真正的回源都不可能成功。

再看一眼推送流状态：

```bash
curl -s http://localhost:8090/healthz
```

**该看到**：`{"streamHealthy":false}`。杀掉进程通常会让 TCP 连接立刻收到
RST，demo 这边几乎瞬间（实测在同一秒内）就能感知到推送流断开，一般不需要
专门等——如果你的环境上稍有延迟，隔一两秒再看一次。
fp 死后，demo 与它之间那条常驻的撤销推送流会断开；SDK 检测到断开后，
会把本地缓存的有效期主动收紧到 `DegradedCacheTTL`（默认 5 秒，对**已经
缓存的条目也生效**，不是只对之后新写入的条目生效）——因为推送断了，
撤销事件再也送不到本地，这时候只能靠更短的 TTL 兜底，不能继续信任
登录时下发的完整窗口（本例默认应用策略下是 30 秒）。

等 `streamHealthy` 变成 `false` 之后，再等超过 `DegradedCacheTTL`（默认
5 秒，保险起见等 10 秒）：

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8090/api/me \
  -H "Authorization: Bearer $TOKEN"
```

**该看到**：`503`，**不是** `401`。

这一点是故意的，也是整个降级设计里最容易被做错、错了却不报错、只出
怪事的地方：fp 不可达是一次基础设施故障，不是"这个 token 无效"。如果
这里回的是 401，业务方前端常见的处理方式就是清掉本地保存的 token、
把用户踢回登录页——一次几秒钟的 fp 抖动，会被放大成全体在线用户被迫
重新登录一次。回 503，前端该做的是提示"服务暂不可用，请稍后重试"，
`token` 原样留着不动，fp 恢复后请求会自动重新成功，用户全程无感。

**如果这一步最后拿到的是 `401` 而不是 `503`，说明 SDK 的降级路径有
问题**——回去查 `sdk/cache.go`（`DegradedCacheTTL` 有没有在读取时对存量
条目生效）和 `sdk/middleware.go`（`ErrUnavailable` 有没有被正确映射成
503），不是这个 demo 的问题。

验收完成后，把 fp 重新跑起来（`./scripts/run.sh`），demo 会自动重连、
`streamHealthy` 会自己变回 `true`——不需要重启 demo 进程。重连不一定很快：
demo 侧的重试退避封顶在 30 秒，但底层 gRPC 连接自己也有一层独立的、
可能封顶在一两分钟的重连退避，两层叠加，实测重新变回 `true` 可能要等
将近一到两分钟，不是卡住了。赶时间的话重启 demo 进程（`./scripts/demo.sh`）
比等更快——一次全新的 `fpsdk.New` 不背负这层退避历史。
