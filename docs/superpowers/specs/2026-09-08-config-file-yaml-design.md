# fp / fp-im 启动配置 YAML 化设计

**日期**：2026-09-08
**状态**：设计已定，待细化为实施计划
**上游**：`2026-08-24-fp-foundation-platform-design.md`、`2026-09-03-fp-im-design.md`

---

## 一、要做什么

fp 与 fp-im 的启动配置现在全部来自环境变量（`FP_` / `FP_IM_` 前缀），本机开发靠
`.env.local` + `scripts/env.sh` 拼出连接串再 `export`。三个问题：

1. **拼错变量名无法被发现。** `FP_LOG_LVEL=debug` 不会报任何错，只是静默回落到
   `envOr` 的默认值。环境变量这个介质本身没有"这个键我不认识"的概念。
2. **一份配置散在三处。** 有哪些项要看 `.env.example` 的注释，连接串怎么拼要看
   `scripts/env.sh`，默认值是多少要看两个 config 包里的 `envOr`。
3. **两个二进制的配置边界不清。** fp-im 的 `FP_IM_*` 混在 fp 的 `.env.local` 里，
   `scripts/env.sh` 还替 fp-im 兜了 `FP_IM_REDIS_URL` 缺省复用 `FP_REDIS_URL` 这类
   逻辑——那是配置文件该表达的事，不是 shell 脚本。

改成：**每个二进制读一份自己的 YAML 文件。**

> fp 读 `./config.yaml`，fp-im 读 `./config-im.yaml`。
> 键名去掉 `FP_` / `FP_IM_` 前缀、全小写、按语义分组。

fp-im 强制依赖 fp（它必须连 fp 的 gRPC 验 token），但两份配置文件完全独立，
互不引用、互不继承默认值。

## 二、明确不做

| 项 | 一句话理由 |
|---|---|
| 环境变量覆盖 YAML | 要维护一张双向映射表，且 `FP_`/`FP_IM_` 整套前缀得留着，等于没简化 |
| `${ENV}` 插值 | 多一层机制要写要测，配置还得跨两个文件读；密钥直接写进 gitignore 的文件已经够 |
| 配置热重载 | `http.addr`、`postgres.url` 这类改了也不能热生效，为此区分"可热重载段"与"启动段"不划算。apps 文件的热重载不受影响——它本来就是独立文件 |
| 动 `apps_file` 指向的 JSON | 后续 spec 会把整个 `internal/im/appcfg` 与这一项删掉、换成 fp 下发，不值得为它做一次一次性的格式迁移 |
| 给 fp 的 gRPC 加 TLS | `internal/grpcapi/server.go` 的 `grpc.NewServer` 现在没传 `grpc.Creds`，只服务明文；生产靠反代终结 TLS。补上是独立的一件事，见第六节 |
| 动 `FP_TEST_POSTGRES_URL` / `FP_TEST_REDIS_URL` | 测试基础设施不是产品配置，`internal/testsupport` 直读 env 是 Go 的惯例 |
| 动 `examples/` 的 env 读取 | 它们是 SDK 使用方的示例，不该被 fp 自己的配置文件格式绑架 |
| 配置文件路径的环境变量兜底 | `-c` 标志已经够；再加一个 `FP_CONFIG` 就是把刚删掉的那套东西请回来 |

## 三、config.yaml（fp）

```yaml
# fp 启动配置。含密钥，不进 git；从 config.example.yaml 复制一份填。
env: dev                       # dev / prod，大小写不敏感

log:
  level: info                  # debug / info / warn / error

http:
  addr: ":8080"                # 管理控制台
grpc:
  addr: ":9090"                # SDK 接入

postgres:
  url: postgres://postgres:xxx@127.0.0.1:5432/fp?sslmode=disable
redis:
  url: redis://:xxx@127.0.0.1:6379/0

# 首次启动创建的平台管理员。两项都填才生效，已存在同名账号不覆盖
# （AdminService.EnsureBootstrap 在任一为空时直接跳过）。
bootstrap_admin:
  user: admin
  password: admin

# 阿里云短信。只在 env: prod 时前四项强制必填；非 prod 缺任一项走
# notify.NewFakeProvider 并打一条 WARN——不静默，只是不强制。
sms:
  aliyun:
    access_key_id: ""
    access_key_secret: ""
    sign_name: ""
    template_login_code: ""    # fp 模板 key "login_code" 对应的阿里云模板 ID
    endpoint: ""               # 留空用阿里云默认接入点，任何环境都不必填
```

| YAML 路径 | 原环境变量 | 默认值 |
|---|---|---|
| `env` | `FP_ENV` | `dev` |
| `log.level` | `FP_LOG_LEVEL` | `info` |
| `http.addr` | `FP_HTTP_ADDR` | `:8080` |
| `grpc.addr` | `FP_GRPC_ADDR` | `:9090` |
| `postgres.url` | `FP_POSTGRES_URL` | 无，必填 |
| `redis.url` | `FP_REDIS_URL` | 无，必填 |
| `bootstrap_admin.user` | `FP_BOOTSTRAP_ADMIN_USER` | 空 |
| `bootstrap_admin.password` | `FP_BOOTSTRAP_ADMIN_PASSWORD` | 空 |
| `sms.aliyun.access_key_id` | `FP_ALIYUN_ACCESS_KEY_ID` | 空，prod 必填 |
| `sms.aliyun.access_key_secret` | `FP_ALIYUN_ACCESS_KEY_SECRET` | 空，prod 必填 |
| `sms.aliyun.sign_name` | `FP_ALIYUN_SMS_SIGN_NAME` | 空，prod 必填 |
| `sms.aliyun.template_login_code` | `FP_ALIYUN_SMS_TEMPLATE_LOGIN_CODE` | 空，prod 必填 |
| `sms.aliyun.endpoint` | `FP_ALIYUN_ENDPOINT` | 空，任何环境都可空 |

`env` 的默认值从 `DEV` 改成小写 `dev`。`IsProd()` 继续用 `strings.EqualFold`
判定，所以 `prod` / `PROD` / `Prod` 都认——大小写这件事上宽进，写出来的默认值
严格小写。

## 四、config-im.yaml（fp-im）

```yaml
# fp-im 启动配置。fp-im 强制依赖 fp，但这份配置完全独立于 config.yaml。
env: dev

log:
  level: info

http:
  addr: ":8081"                # client 的 WebSocket 接入
  trust_proxy: false           # 前面有可信反代时才开，决定是否采信 X-Forwarded-For
grpc:
  addr: ":9091"                # 业务 server 接入

redis:
  url: redis://:xxx@127.0.0.1:6379/0

# fp SDK 的连接参数。addr 是 fp 的 gRPC 地址，不是 HTTP。
# 传输安全由 env 推导：dev 明文，prod 走 TLS（见第六节）。
fpsdk:
  addr: localhost:9090

apps_file: ./tmp/im-apps.json  # 后续 spec 会删掉这一项，换成 fp 下发

node:
  heartbeat: 3s
  dead_after: 10s              # 必须大于 heartbeat

conn:
  field_ttl: 30m               # 必须 >= 1s
  field_renew: 10m             # 必须小于 field_ttl 的一半
  idle_timeout: 60s            # 必须 >= 50s（client SDK 心跳 25s 的两倍）
  auth_timeout: 5s
  send_queue: 256

pipeline:
  flush_interval: 0s           # 0s = 不合批
  flush_size: 1                # >1 时必须同时设 flush_interval
```

| YAML 路径 | 原环境变量 | 默认值 |
|---|---|---|
| `env` | `FP_IM_ENV` | `dev` |
| `log.level` | `FP_IM_LOG_LEVEL` | `info` |
| `http.addr` | `FP_IM_HTTP_ADDR` | `:8081` |
| `http.trust_proxy` | `FP_IM_TRUST_PROXY` | `false` |
| `grpc.addr` | `FP_IM_GRPC_ADDR` | `:9091` |
| `redis.url` | `FP_IM_REDIS_URL` | 无，必填 |
| `fpsdk.addr` | `FP_IM_FP_ADDR` | 无，**不由 config 校验**，见第七节 |
| `apps_file` | `FP_IM_APPS_FILE` | 无，必填 |
| `node.heartbeat` | `FP_IM_NODE_HEARTBEAT` | `3s` |
| `node.dead_after` | `FP_IM_NODE_DEAD_AFTER` | `10s` |
| `conn.field_ttl` | `FP_IM_CONN_FIELD_TTL` | `30m` |
| `conn.field_renew` | `FP_IM_CONN_FIELD_RENEW` | `10m` |
| `conn.idle_timeout` | `FP_IM_CONN_IDLE_TIMEOUT` | `60s` |
| `conn.auth_timeout` | `FP_IM_CONN_AUTH_TIMEOUT` | `5s` |
| `conn.send_queue` | `FP_IM_CONN_SEND_QUEUE` | `256` |
| `pipeline.flush_interval` | `FP_IM_PIPELINE_FLUSH_INTERVAL` | `0s` |
| `pipeline.flush_size` | `FP_IM_PIPELINE_FLUSH_SIZE` | `1` |

**`FP_IM_FP_INSECURE` 消失**，理由见第六节。

**`trust_proxy` 挂在 `http` 下而不是顶层**：它只影响从 `X-Forwarded-For` 取真实
客户端 IP，是 HTTP 层的事。

**`redis.url` 不再有"缺省复用 fp 的 Redis"这一条。** 那条兜底现在写在
`scripts/env.sh` 里（`FP_IM_REDIS_URL="${FP_IM_REDIS_URL:-$FP_REDIS_URL}"`），
是 shell 在替配置做决定；两份配置文件独立之后，fp-im 想跟 fp 共用一个 Redis
就把同一条 URL 抄进 `config-im.yaml`，一行的事，比一条藏在 shell 里的隐式继承
好懂得多。

## 五、加载器行为

### 5.1 文件路径

`-c` 标志，默认 `./config.yaml`（fp）/ `./config-im.yaml`（fp-im）。只有 `-c`
这一个形式，不另外声明 `--config`——`cmd/fp-dbclean` 已有的 `-y` / `-no-redis`
就是这个风格，保持一致。

```
fp    -c /etc/fp/config.yaml
fp-im -c /etc/fp/config-im.yaml
```

**文件不存在直接报错退出。** 不静默用一整套默认值跑起来——`postgres.url` 反正
是必填，也走不下去，那还不如把"找不到 config.yaml"这句话直接说出来。

### 5.2 未知键当场报错

`yaml.NewDecoder(f)` + `dec.KnownFields(true)`。`log: { lvel: debug }` 启动失败，
而不是静默用 `info`。

**这是从 env 迁到文件净新增的能力**，也是这次迁移最实在的收益：环境变量那个
介质压根没有"这个键我不认识"的概念，文件有。与 `internal/httpapi` 的
`decodeJSON` 开 `DisallowUnknownFields` 是同一条纪律。

### 5.3 Go 结构体与 YAML 1:1 同构

不加中间 `file` 映射结构体。`internal/config.Config` 与 `internal/im/config.Config`
的字段树直接就是 YAML 的字段树：

```go
// internal/config
type Config struct {
	Env            string         `yaml:"env"`
	Log            Log            `yaml:"log"`
	HTTP           Listen         `yaml:"http"`
	GRPC           Listen         `yaml:"grpc"`
	Postgres       Endpoint       `yaml:"postgres"`
	Redis          Endpoint       `yaml:"redis"`
	BootstrapAdmin BootstrapAdmin `yaml:"bootstrap_admin"`
	SMS            SMS            `yaml:"sms"`
}

type Listen struct{ Addr string `yaml:"addr"` }
type Endpoint struct{ URL string `yaml:"url"` }
```

两条纪律：

- **每个字段都必须显式写 `yaml` tag。** yaml.v3 的默认规则是把字段名整个小写，
  `DeadAfter` 会变成 `deadafter` 而不是 `dead_after`——多词字段不写 tag 就会静默
  错位，而 `KnownFields(true)` 会把它报成"未知键 dead_after"，错误信息指向的是
  配置文件而不是真正出错的 struct tag。
- **用具名类型，不用匿名嵌套 struct。** `internal/im/config.Config` 现在的
  `Node struct{ Heartbeat, DeadAfter time.Duration }` 逼得 `cmd/fp-im/main_test.go`
  只能先建一个空 Config 再逐字段赋值（`c.Node.Heartbeat = ...`），没法用复合
  字面量一次写完。具名类型顺手把这个问题解决了。

调用方相应改名：`cfg.PostgresURL` → `cfg.Postgres.URL`，`cfg.FPAddr` →
`cfg.FPSDK.Addr`，`cfg.HTTPAddr` → `cfg.HTTP.Addr`。是一轮机械替换，换来的是
配置文件和代码读起来是同一张图。

### 5.4 时长

`internal/im/config` 里加一个 `Duration`：

```go
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error // → time.ParseDuration
func (d Duration) Std() time.Duration
```

**拒绝裸数字**，与 `internal/im/model.Duration` 同一条纪律：配置里写 `2` 是想表达
2 秒还是 2 纳秒，没人说得清，与其猜一个不如让配置加载直接失败。`0s` 合法
（`pipeline.flush_interval` 的"不合批"就是它）。

不跟 `model.Duration` 合并成一个类型：那个是 JSON 的（服务于 apps 文件），这个
是 YAML 的，而 `internal/im/config` 不该为了复用一个十行的类型去 import
`internal/im/model`。fp 的 `internal/config` 目前一个时长项都没有，不需要这个类型。

## 六、传输安全由 env 推导

`FP_IM_FP_INSECURE` 这个布尔旋钮删掉，改成：

| `env` | fp-im → fp 的 gRPC 传输 |
|---|---|
| `dev`（默认） | 明文（`fpsdk.Options.Insecure = true`） |
| `prod` | TLS，系统根证书（`Insecure = false`） |

理由：一个默认值为 `true` 的 `insecure` 旋钮，最可能的失效方式就是有人把它连同
整份 dev 配置抄到生产上——`appSecret` 随每个 RPC 的 metadata 明文发送，抓到包就
等于拿到一个能签发任意用户会话的凭据。删掉旋钮之后这个失效模式不存在了：要在
生产跑明文，你得把 `env` 写成 `dev`，那是个显眼到藏不住的错误。

fp 那边 `IsProd()` 已经在用同样的模式管阿里云四项必填，这里是同一条思路的延伸。

**必须同时记下的前提**：`internal/grpcapi/server.go` 的 `grpc.NewServer` 没有传
`grpc.Creds(...)`，**fp 的 gRPC 服务端目前只服务明文**。所以 `env: prod` 下这条
TLS 必须由前面的反代 / 网关终结，fp 自身还不能直接收 TLS。给 fp 的 gRPC 加
`grpc.Creds` 是独立的一件事，不在本 spec 范围内——但在补上之前，"prod 走 TLS"
这条对部署方是一个硬要求，不是可选项，必须写进 `docs/im.md`。

`fpsdk.Options.Insecure` 这个 SDK 字段本身**保留**：`examples/demo` 等 SDK 使用方
还要用它，只是 fp-im 的配置文件不再暴露它。

## 七、必填与校验的归属

### 7.1 config 包管什么

| | 必填项 | 错误信息 |
|---|---|---|
| fp | `postgres.url`、`redis.url`；`env: prod` 时另加阿里云四项 | `config: config.yaml 缺少 postgres.url` |
| fp-im | `redis.url`、`apps_file` | `config: config-im.yaml 缺少 redis.url` |

错误信息里一律用 **YAML 路径**，不用环境变量名。文件名取自实际加载的路径
（`-c` 传了什么就报什么），不是硬编码的 `config.yaml`。

`internal/im/config` 现有的跨字段校验全部原样保留，只把错误信息里的
`FP_IM_NODE_DEAD_AFTER` 换成 `node.dead_after`：

- `node.dead_after > node.heartbeat`
- `conn.field_renew * 2 < conn.field_ttl`
- `conn.idle_timeout >= MinIdleTimeout`（50s）
- `conn.field_ttl >= 1s`
- `pipeline.flush_size > 1` 时必须设 `pipeline.flush_interval`

### 7.2 `fpsdk.addr` 归谁校验

按"谁用谁校验"的原则，`fpsdk.addr` **从 config 的必填清单里拿掉**——`internal/im/config`
不知道谁会用这个地址，它不该替使用方决定这一项是不是必需。

**但空值仍要在启动时炸，不能等到第一个用户连上来。** `fpauth` 建 `fpsdk.Client`
是**懒**的（每个 app 一个，首次握手才建），光靠 `fpsdk.New` 的
`Options.validate()` 报错，会把"地址配错了"推迟到第一个真实用户握手的那一刻。
所以 `fpauth.New` 保留它现有的 `cfg.FPAddr == ""` 检查：fpauth 是 fpsdk 的直接
使用方，报错归属对了，快速失败也没丢。

这是个**过渡态**。后续 spec 把 app 清单改成由 fp 下发之后，fp-im 只建一个
`fpsdk.Client` 且在启动装配时就建，`fpsdk.New` 自己会当场报错，`fpauth.New` 里
那句检查随之删掉——那时校验才真正完全回到 SDK。

## 八、连带改动

**新建：**

| 文件 | 职责 |
|---|---|
| `config.example.yaml` | fp 配置模板，含全部项与注释；进 git |
| `config-im.example.yaml` | fp-im 配置模板；进 git |

**改：**

| 文件 | 改什么 |
|---|---|
| `internal/config/config.go` | 从 env 读改成从 YAML 读；具名嵌套类型；错误信息用 YAML 路径 |
| `internal/config/config_test.go` | `t.Setenv` 改成写临时 YAML 文件；补未知键、缺必填项、文件不存在三类用例 |
| `internal/im/config/config.go` | 同上，另加 `Duration` 类型；删 `FPInsecure` 字段，加 `Insecure()` 由 `env` 推导 |
| `internal/im/config/config_test.go` | 同上 |
| `cmd/fp/main.go` | 加 `-c` 标志；`cfg.PostgresURL` → `cfg.Postgres.URL` 等一轮改名 |
| `cmd/fp-im/main.go` | 加 `-c` 标志；同一轮改名；`cfg.FPInsecure` → `cfg.Insecure()` |
| `cmd/fp-im/main_test.go` | `testConfig` 改用具名类型的复合字面量 |
| `cmd/fp-dbclean/main.go` | 现在直读 `FP_POSTGRES_URL`/`FP_REDIS_URL`，改成走 `internal/config` 加 `-c` 标志。它清的本来就是 `config.yaml` 指向的开发库，语义没变 |
| `scripts/env.sh` | 缩成只 `. .env.local`（保留缺文件时的指引），不再拼任何连接串 |
| `scripts/run.sh` | 不再 source `env.sh`，不再 export bootstrap admin（挪进 `config.yaml`），只剩 `go run ./cmd/fp` |
| `scripts/run-im.sh` | 不再 source `env.sh`，只剩 `go run ./cmd/fp-im` |
| `scripts/db-clean.sh` | 不再 source `env.sh`，只剩 `exec go run ./cmd/fp-dbclean "$@"` |
| `.gitignore` | 加 `/config.yaml`、`/config-im.yaml` |
| `.env.local`（本机文件，gitignore） | 缩到只剩 `FP_TEST_POSTGRES_URL` / `FP_TEST_REDIS_URL` + `examples/demo` 用的 `FP_APP_ID` / `FP_APP_SECRET`；同时新建本机的 `config.yaml` / `config-im.yaml` |
| `docs/console.md`、`docs/im.md` | 配置章节重写；`docs/im.md` 补第六节那条"prod 必须有反代终结 TLS"的硬要求 |

**删：**

| 文件 | 理由 |
|---|---|
| `.env.example` | 被两份 `*.example.yaml` 取代；`.env.local` 从此只放测试与示例的环境变量，不再是 fp 的配置 |

**不动：** `internal/testsupport`（测试基础设施）、`examples/`（SDK 使用方示例）、
`internal/im/appcfg` 与 `tmp/im-apps.json`（后续 spec 会整个删掉）、
`scripts/test.sh` 与 `scripts/demo.sh`（它们 source `env.sh` 要的正是留下来的那几个
环境变量：测试库连接串与 demo 的应用凭据）、`scripts/test-all.sh`。

## 九、测试要点

两个 config 包各自要覆盖：

1. **完整文件解析正确** —— 每个字段都读到了预期值（防 5.3 里说的 yaml tag 漏写：
   漏了 tag 的多词字段会读成零值，只有逐字段断言才抓得住）
2. **未知键报错** —— `log: { lvel: debug }` 必须失败
3. **缺必填项报错** —— 错误信息里出现 YAML 路径而不是环境变量名
4. **文件不存在报错** —— 而不是回落到默认值
5. **默认值生效** —— 一份只写了必填项的最小文件，其余字段是表里那些默认值
6. **裸数字时长被拒**（fp-im）—— `heartbeat: 2` 必须失败
7. **`env` 推导传输安全**（fp-im）—— `dev` → `Insecure()` 为 true，`prod` → false，
   大小写不敏感
8. **跨字段校验原样保留**（fp-im）—— 现有那五条各一个用例，断言错误信息里是
   YAML 路径

`cmd/fp-im/main_test.go` 的 `testConfig` 是现成的端到端护栏：它构造 `config.Config`
起一个真实节点，改名漏掉任何一处都会编译失败。

## 十、给后续 spec 留的接口

本 spec 结束时，`config-im.yaml` 里与 app 清单有关的只有 `apps_file` 一项，
`internal/im/appcfg` 一行没动。后续那个 spec 要做的替换因此是局部的：删掉
`apps_file`、删掉 `appcfg` 包、在 `fpsdk` 段下加 fp-im 自己的凭据
（`fpsdk.app_id` / `fpsdk.app_secret`），`auth.AppConfigSource` 这个接口本身不变——
换的只是它的实现从"读本地文件"变成"从 fp 拉"。
