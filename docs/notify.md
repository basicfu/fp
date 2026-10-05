# 通知中心接入文档

业务方只传「模板 code + 收件人 + 变量」；走哪家供应商、文案是什么，全部在 fp 控制台里配置，改配置不用改代码、不用发版。

## 1. 三个概念

- **供应商实例**：一份凭据就是一个实例（一个阿里云账号、一个 telegram bot、一个企业微信机器人 Key……）。同一类型可以建多个。
- **模板**：业务代码引用的一条通知，`code` 由你指定、全局唯一、创建后不可改。创建时选定渠道和模板模式。
- **关联**：模板挂哪些供应商实例，每个实例在这个模板下登记自己的供应商侧模板 ID、优先级和启停。

| 渠道 | 模板模式 | 收件人 | 说明 |
|---|---|---|---|
| `sms` | 供应商模板 | 手机号 | 文案在供应商后台审核，fp 只存原文供核对，发送时传供应商模板 ID + 变量 |
| `email` | 供应商模板或自定义 | 邮箱 | 云厂商邮件要模板；自建 SMTP 直接在 fp 里写内容。目前能真正发邮件的内置类型只有 `smtp`，它只能发自定义模板（见第 5 节） |
| `telegram` | 自定义 | 无 | 发到供应商实例里配置好的 chat |
| `wecom_bot` | 自定义 | 无 | 企业微信群机器人；`@` 成员配置属于模板 |
| `dingtalk_bot` | 自定义 | 无 | 钉钉群机器人；支持加签；`@` 配置属于模板 |
| `webhook` | 自定义 | 无 | 自定义 HTTP 调用，支持 GET / POST |

**模板变量**：自定义模板里写 `{name}`，发送时按调用方传的变量替换。变量值会按目标格式转义（JSON、HTML、URL），不用自己处理。调用方传的变量必须与模板声明的变量**完全一致**，多了少了都会被拒绝。

## 2. 控制台里怎么配

控制台左侧「通知中心」，三个标签：

1. **供应商**：新建 → 选类型 → 填凭据。各类型要填什么见第 5 节。
2. **模板**：新建 → 选渠道 → 填 code → 写内容。短信与云厂商邮件要把供应商后台审核通过的模板原文粘贴进来，变量按原文里的占位符填。
3. 进入模板详情页，**关联供应商**：短信 / 邮件可以关联多个，发送时按优先级从高到低尝试，同优先级的随机排序，失败自动降级到下一个；IM 与 webhook 只能关联一个，不降级。
4. 点「测试发送」，走与业务方完全相同的路径，真的会发出去。结果在「发送记录」。

临时不用某家供应商：在模板详情页把那一行的开关关掉（只影响这个模板），或者在供应商列表里停用整个实例（影响所有引用它的模板）。

## 3. 业务方调用

```go
client, _ := fpsdk.New(fpsdk.Options{Addr: addr, AppID: appID, AppSecret: secret})

// 短信：收件人是手机号
err := client.Notify().Send(ctx, "login_sms", "13800138000", map[string]string{"code": "123456"})

// IM / webhook：没有收件人，传空串
err = client.Notify().Send(ctx, "order_alert", "", map[string]string{"orderNo": "A1001"})
```

**幂等与重试**：SDK 默认每次调用自动生成一个幂等键。fp 不可达、超时（单次尝试 30 秒）、或上一次相同请求还在处理（`NOTIFY_IN_PROGRESS`）这类瞬时失败，SDK 最多重试两次（指数退避，200ms 起），**整个调用复用同一个键**，服务端据此避免同一条通知因为重试而发出两次。想让业务自己的重试（重跑任务、重放消息）也不重复发送，用业务事件的 ID 当键：

```go
err := client.Notify().Send(ctx, "order_alert", "", params, fpsdk.WithIdempotencyKey("order-A1001-shipped"))
```

相同 `code + 键` 发送成功后，24 小时内的重复请求直接返回成功、不再发送；上一次发送失败时键会被释放，重试能真的重发。键只和模板 `code` 拼在一起，**不含应用、收件人和变量**：不同应用、不同收件人用了同一个键，会被当成同一条通知，所以业务键要能唯一标识「这一条通知」（如上面的 `order-A1001-shipped`）。

## 4. 错误码

这些错误码由 `*fpsdk.Error` 携带：用 `errors.As` 取出，按 `Code` 分支；`Detail` 是 JSON 字符串，可能为空（取法见 [SDK 文档](../sdk/README.md#错误处理)）。

| code | 含义 | 处理 |
|---|---|---|
| `NOTIFY_TEMPLATE_NOT_FOUND` | 模板 code 不存在 | 检查 code |
| `NOTIFY_TEMPLATE_DISABLED` | 模板已停用 | 在控制台启用 |
| `NOTIFY_PARAMS_INVALID` | 变量与模板声明的不一致 | `Detail` 里的 `missing` / `unexpected` 列出缺的与多的 |
| `NOTIFY_RECIPIENT_INVALID` | sms / email 缺收件人，或 IM / webhook 传了收件人 | 按渠道传 |
| `NOTIFY_PROVIDER_MISSING` | 模板没有可用的供应商（没关联，或全被禁用） | 去模板详情页检查 |
| `NOTIFY_IN_PROGRESS` | 相同幂等键的请求还在处理 | SDK 已自动重试两次仍遇到才会返回；上一次的结果未知，稍后用同一个键重试（见第 3 节） |
| `NOTIFY_SEND_FAILED` | 全部供应商都失败 | 具体原因在控制台「发送记录」，不会回给调用方 |

## 5. 供应商类型与配置

| 类型 | 渠道 | 配置项（🔒 = 密钥，控制台读取时显示为 `********`） |
|---|---|---|
| `aliyun` | sms | `accessKeyId`、`accessKeySecret`🔒、`signName`、`endpoint`（默认 `dysmsapi.aliyuncs.com`） |
| `smtp` | email | `host`、`port`（默认 465）、`username`、`password`🔒、`from`、`tls`（默认 `ssl`；`ssl` / `starttls` / `none`）。只能发自定义（fp 渲染）模板：关联时不校验模板模式，关联了供应商模板的 smtp 实例，发送时会报错并按优先级降级到下一个 |
| `telegram` | telegram | `botToken`🔒、`chatId` |
| `wecom_bot` | wecom_bot | `key`🔒（Webhook 地址里 `key=` 后面那段） |
| `dingtalk_bot` | dingtalk_bot | `accessToken`🔒、`secret`🔒（机器人安全设置选了「加签」才填） |
| `webhook` | webhook | `url`、`secret`🔒（可选） |
| `log` | 任意 | `channel`；只写服务端日志、不真发，仅供本地开发，`FP_ENV` 为 `prod` 时不可用（见下） |

**`log` 与 `FP_ENV`**：`log` 会把收件人、变量和渲染后的内容都写进服务端日志，绝不能出现在有真实数据的环境里。`FP_ENV` 等于 `prod`（不分大小写）时，它在类型列表里不出现、不能新建；库里已有的 `log` 实例不能再关联，已经关联的在发送时按失败的一次尝试处理、不会被调用。`FP_ENV` 不设时缺省是 `DEV`，写成 `production`、`staging` 之类也算非 prod——生产类部署必须显式设 `FP_ENV=prod`。

**webhook 的请求形状**由模板定义：POST 时 body 是渲染后的内容，`Content-Type` 取模板设置；GET 时渲染出的内容作为 query 追加到 `url` 后面。配了 `secret` 时请求头带 `X-Fp-Signature: sha256=<hex>`，值是 `HMAC-SHA256(secret, 被签内容)`，POST 签 body、GET 签 query。2xx 视为成功，超时 10 秒，不跟随重定向。

## 6. 已知限制

- 没有频率限制：调用方是已认证的服务端；模板上暂无限流配置，需要时再加。
- 没有回执回调与后台重试队列：发送是同步的，失败即返回错误。SDK 只自动重试瞬时失败（见第 3 节），`NOTIFY_SEND_FAILED` 不会被重试，要不要重发由业务方决定。
- 幂等不是严格的恰好一次：fp 进程在发送中途崩溃时，幂等键的「处理中」标记最多 60 秒后过期，之后同键重试可能让消息重复发出。
- 供应商密钥明文存在数据库里（与访问密钥 SK 同一先例），只在控制台读取时脱敏。
- webhook 的 `url` 由控制台管理员配置，不做内网地址过滤；telegram / 企业微信 / 钉钉只发往各自固定的官方接口域名。
- 供应商、模板与幂等键全局共享，不按应用隔离；发送记录里的「调用方」只记录是哪个应用调的。
