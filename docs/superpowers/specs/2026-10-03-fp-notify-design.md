# fp 通知模块设计

**日期**：2026-10-03
**状态**：设计已定；实施计划：`docs/superpowers/plans/2026-10-03-fp-notify.md`（前置 `2026-10-03-fp-remove-sms-login.md`）
**上游**：`2026-08-24-fp-foundation-platform-design.md` 第七章"通知中心"

---

## 一、要做什么

现有 `internal/notify` 只服务登录验证码：两个通道（短信/邮件）、凭据来自系统配置、业务方调不到。改成**配置驱动的通知分发层**：控制台维护供应商与模板，业务方只传 `code + 收件人 + 变量`。同时**移除验证码体系**——它是旧 notify 唯一的使用者。

渠道（`channel`）：`sms`、`email`、`telegram`、`wecom_bot`、`dingtalk_bot`、`webhook`。

## 二、明确不做

| 项 | 一句话理由 |
|---|---|
| 编排层（workflow / digest / 订阅偏好） | 属于业务逻辑，与"fp 又小又稳"冲突 |
| 站内信、App Push | 暂不需要；日后按 `Provider` 接口追加，不影响本设计 |
| 回执回调、异步重试队列 | 重基础设施；失败即返回 error，SDK 靠幂等 key 安全重试 |
| 频率限制 | 旧的 30s/1h/1d 按号码限流服务于验证码防刷，该场景已移除，调用方是已认证的服务端；需要时在模板上加 `rate_limits`，复用 `store.RateLimiter` |
| 按应用隔离 code / 供应商 | 全局共享，一个 fp 部署一套，与"不做多租户"一致 |
| 一个 code 同时发多个目标 | IM / webhook 先一对一，要多目标就建多个 code |
| IM 的 markdown / 卡片消息 | 本期只发 text |
| 供应商 secret 落库加密 | 与访问密钥 SK 同一先例：明文落库、读取脱敏；不引入新的必填环境变量 |
| webhook / IM 目标地址的 SSRF 过滤 | 目标由唯一的超级管理员配置，不是终端用户输入 |
| 腾讯云 / 助通短信、三方模板型邮件供应商 | 按同一 `Provider` 接口各加一个文件，不影响本设计 |

## 三、核心模型

- **供应商实例** `notify_provider`：一份凭据 = 一个实例。`type` 决定用代码里哪个实现，同一 `type` 可建多个（两个阿里云账号、多个 telegram bot）。`id` 自动生成，`description` 可选备注。固定不变的东西（接入点、签名算法、URL 前缀）写在代码里，可变的（AK、botToken、key）进 `config`。
- **模板** `notify_template`：业务代码引用的一个通知。`code` 人工指定、全局唯一、**创建后不可改**（业务代码硬引用它）；创建时选定 `channel`。
- **关联** `notify_template_provider`：模板挂哪些供应商实例；每个实例在该模板下登记自己的 `provider_template_id`、启停、优先级。一个供应商侧模板 ID 只属于一个 code，所以这张表是模板的从属数据，控制台挂在模板详情页下管理；供应商详情页反向列出"被哪些模板引用"。

**两种模板模式**，由渠道限定：`sms` → vendor；`email` → vendor 或 custom；其余 → custom。创建时校验。

| 模式 | `content` 的角色 | 发送时 |
|---|---|---|
| vendor（供应商侧已审核的模板） | 供应商模板原文，**仅供查看核对，不参与渲染** | 传 `provider_template_id` + params，由供应商替换变量 |
| custom（自建邮件、IM、webhook） | fp 自己的模板 | fp 渲染后发送 |

**变量校验**：`Send` 的 params 键集合必须与模板 `variables` **完全一致**——缺了、多了都拒绝，拼错变量名当场报错，而不是发出一条缺内容的短信。

**占位符**：custom 模式写 `{name}`，`name` 限 `[A-Za-z_][A-Za-z0-9_]*`。JSON 模板里的 `{"k": ...}` 花括号后面是引号，不匹配这个形状，不会被误认成占位符。保存时要求 content 里的占位符集合等于 `variables`；vendor 模式的 `variables` 按供应商模板填写（界面从 content 里按 `${x}` / `{{x}}` / `{x}` 预填）。

**替换值转义**随目标格式走：`application/json` → JSON 字符串转义；`text/html` → HTML 转义；webhook GET 的 query → URL 转义；其余原样。

## 四、数据模型

迁移 `00016_notify_templates.sql`（`00015` 是 §八 清理 `sms_code` 启用记录的那一份）：

```sql
-- 供应商实例。type 对应代码里注册的实现；config 字段结构由该 type 的 ConfigSchema 定义。
CREATE TABLE notify_provider (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    type        text NOT NULL,
    description text NOT NULL DEFAULT '',
    enabled     boolean NOT NULL DEFAULT true,
    config      jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notify_provider_type_idx ON notify_provider (type);

-- 模板。code 直接做主键：Send 的唯一入口就是按它查。
CREATE TABLE notify_template (
    code          text PRIMARY KEY,
    channel       text NOT NULL,
    template_mode text NOT NULL,                       -- vendor / custom
    template      jsonb NOT NULL DEFAULT '{}'::jsonb,  -- 结构见下表
    description   text NOT NULL DEFAULT '',
    enabled       boolean NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- 模板 ↔ 供应商实例，所有渠道通用。sms / email 可挂多个（随机 + 降级）；
-- IM / webhook 约定只挂一个（应用层限制，不是库约束，日后放开不用改表）。
CREATE TABLE notify_template_provider (
    code                 text NOT NULL REFERENCES notify_template(code) ON DELETE CASCADE,
    provider_id          uuid NOT NULL REFERENCES notify_provider(id),
    provider_template_id text NOT NULL DEFAULT '',     -- 仅 vendor 模式
    enabled              boolean NOT NULL DEFAULT true,
    priority             int NOT NULL DEFAULT 0,       -- 越大越优先；0 = 无偏好，参与随机
    PRIMARY KEY (code, provider_id)
);
-- 复合主键里 provider_id 在第二位，"供应商被哪些模板引用"要单独的索引。
CREATE INDEX notify_template_provider_provider_idx ON notify_template_provider (provider_id);

-- 发送记录沿用现有表：原 template 列就是模板 key，改名为 code；
-- provider 列继续存 type，provider_id 记具体实例，app_id 记调用方应用（控制台测试发送为空）。
ALTER TABLE notify_log RENAME COLUMN template TO code;
ALTER TABLE notify_log ADD COLUMN provider_id uuid;
ALTER TABLE notify_log ADD COLUMN app_id text NOT NULL DEFAULT '';
CREATE INDEX notify_log_code_idx ON notify_log (code, created_at DESC);
```

**`template` 的 JSON 结构**（`variables` 为变量名数组）：

| 渠道 / 模式 | 字段 |
|---|---|
| sms / vendor、email / vendor | `content`、`variables` |
| email / custom | `subject`、`content`、`contentType`（`text/plain` \| `text/html`）、`variables` |
| telegram | `content`、`variables` |
| wecom_bot | `content`、`variables`、`mentionedList`、`mentionedMobileList`（静态，@ 属于模板，由用户配置） |
| dingtalk_bot | `content`、`variables`、`atMobiles`、`isAtAll` |
| webhook | `method`（`GET` \| `POST`）、`content`（POST 是 body 模板，GET 是 query 模板）、`contentType`（仅 POST：`application/json` \| `text/plain`）、`variables` |

**供应商类型**（代码里的注册表 `type → 构造函数`；控制台"新建供应商"按 `ConfigSchema` 动态渲染表单，新增类型不改前端）：

| type | channel | config 字段 | 固定在代码里 |
|---|---|---|---|
| `aliyun` | sms | `accessKeyId`、`accessKeySecret`🔒、`signName`、`endpoint`（默认 `dysmsapi.aliyuncs.com`） | 官方 SDK 的签名与调用（现有 `AliyunSMS` 改造） |
| `smtp` | email | `host`、`port`、`username`、`password`🔒、`from`、`tls`（none / starttls / ssl） | — |
| `telegram` | telegram | `botToken`🔒、`chatId` | `https://api.telegram.org/bot{token}/sendMessage` |
| `wecom_bot` | wecom_bot | `key`🔒 | `https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=` |
| `dingtalk_bot` | dingtalk_bot | `accessToken`🔒、`secret`🔒（加签用） | `https://oapi.dingtalk.com/robot/send?access_token=` 与 HMAC-SHA256 加签 |
| `webhook` | webhook | `url`、`secret`🔒（可选） | 方法与报文由模板定义。POST：body = 渲染后的 content，`Content-Type` 取模板的 `contentType`；GET：渲染后的 content 作为 query 追加到 `url`（`url` 已带 `?` 则用 `&` 连接）。配了 `secret` 时加 `X-Fp-Signature: sha256=hex(HMAC-SHA256(secret, 被签内容))`，POST 签 body、GET 签渲染后的 query；2xx 为成功，超时 10s |
| `log` | 任意（config 里选） | `channel` | 只打日志不真发；仅 `FP_ENV` 非 prod 时可建，替代今天 `main.go` 里"凭据不全就退化成 fake" |

🔒 = secret 字段，处理见下。

**secret 字段**（`ConfigSchema` 里 `FieldTypeSecret`）：

- 控制台读取时脱敏为 `********`；更新（`PATCH`）时值为 `********` 表示保持原值。脱敏放在公共层，不绑死 notify。
- **落库不加密**：secret 明文存在 `config` 里，与访问密钥 SK 同一先例（见 `docs/access-key.md`"已接受的限制"），不引入 `FP_SECRET_KEY` 这类新的必填环境变量。
- `domain/field.go` 终审注释里"脱敏与落库加密尚未实现"的债务：脱敏由本模块还清（此前没有任何 connector 声明 secret 字段，本模块要声明一大批，不脱敏就会在列表接口里明文返回）；落库加密按上一条不做，注释同步改成"已接受的限制"。

## 五、发送流程

`Send(ctx, NotifySendInput{AppID, Code, To, Params, IdempotencyKey})`：

1. **取模板**：不存在 → `NOTIFY_TEMPLATE_NOT_FOUND`；`enabled=false` → `NOTIFY_TEMPLATE_DISABLED`。
2. **校验**：params 键集合与 `variables` 完全一致，否则 `NOTIFY_PARAMS_INVALID`（detail 列出缺失与多余的键）；`sms` / `email` 必须有 `to`，IM / webhook 必须没有，否则 `NOTIFY_RECIPIENT_INVALID`。
3. **幂等**（`idempotencyKey` 非空时），Redis 键 `fp:notify:idem:{appId}:{code}:{key}`：
   - `SET NX EX 60` 抢占为"处理中"，标记带归属令牌。抢不到：已完成 → 直接返回成功、不再发送；处理中 → `NOTIFY_IN_PROGRESS`（`ErrConflict`，SDK 当可重试处理）。
   - 每次尝试供应商前续期"处理中"标记；发送成功 → 写 `"2"`（已完成），TTL 24h；发送失败 → 只删自己的标记，让重试真的能重发。
   - 进程在发送中途崩掉，60s 后键过期，重试会再发一次——这个窗口里是至少一次，接受。
4. **选供应商并发送**：
   - `sms` / `email`：候选 = 关联行 `enabled` 且实例 `enabled` 的；按 `priority` 降序分组，同组内随机洗牌；依次尝试，首个成功即返回，失败降级到下一个。指定优先（`priority` 高）的失败后同样会降级到其他未禁用的。没有候选 → `NOTIFY_PROVIDER_MISSING`。
   - IM / webhook：只有一个关联实例，失败直接返回，不降级。
   - 每次尝试写一行 `notify_log`（成败 + 错误文本），**不写 params，也不写渲染后的内容**——沿用旧规则，params 里可能是验证码一类敏感值。
5. 全部失败 → 通用内部错误（具体原因只进 `notify_log` 与服务端日志，不回给调用方，避免泄露供应商侧信息）。

已构造的 `Provider` 按 `id@updated_at` 缓存：改配置后 `updated_at` 变化，天然换新，不需要跨实例的失效广播。

一种类型由 `TypeSpec` 描述（`Type`、`Channel`、`ConfigSchema`、`DevOnly`、构造函数 `New`），登记在 `Registry`；`Provider` 接口只有 `Send(ctx, Delivery)`，实例由 id 区分（多实例后写死类型名没法区分账号）。请求构造抽成纯函数单测，沿用旧 `BuildAliyunRequest` 的做法。

## 六、对外接口

新增 `proto/fp/v1/notify.proto`：

```proto
service NotifyService {
  rpc Send(SendRequest) returns (SendResponse);
}
message SendRequest {
  string code = 1;
  string to = 2;                    // 仅 sms / email
  map<string, string> params = 3;
  string idempotency_key = 4;       // 可选；SDK 自动生成
}
message SendResponse {}
```

- **鉴权**：与 `Login` 同一条拦截器链——调用方是用 app_id / secret 认证的**应用**；沿用 `requireNotIM`，fp-im 网关凭据不能调用。`notify_log.app_id` 记录调用方。
- **SDK**：`client.Notify().Send(ctx, code, to, params, opts...)`，`WithIdempotencyKey(k)` 覆盖默认。默认每次调用生成随机幂等 key；对 `Unavailable` / `DeadlineExceeded` / `NOTIFY_IN_PROGRESS` 最多重试 2 次（指数退避，200ms 起），**整个调用复用同一个 key**。现有 SDK 的 unary RPC 没有重试，这是 Notify 独有的。

## 七、控制台

侧边栏新增「通知中心」，页内三个 tab：

- **模板** `/notify/templates`：列表；新建流程"选 channel → 填 code → 选模式 → 写模板"；详情页上半是模板内容，下半是关联供应商子列表（每行：供应商备注、`provider_template_id`、优先级、启停开关），另有「测试发送」。
- **供应商** `/notify/providers`：列表（类型 / 备注 / 启停 / 被引用数）；新建先选 type 再按 `ConfigSchema` 渲染表单；详情页反向列出被哪些模板引用。
- **发送记录** `/notify/logs`：按 code、成败过滤，分页。

Admin API（`/admin/api/notify`）：

```
GET  /provider-types                           注册表：type、channel、configSchema
GET/POST /providers
GET/PATCH/DELETE /providers/{id}               PATCH 局部更新；被模板引用时 DELETE 返回 409
GET  /providers/{id}/templates                 被哪些模板引用
GET/POST /templates
GET/PATCH/DELETE /templates/{code}             PATCH 局部更新；code、channel、mode 不可改
PUT/DELETE /templates/{code}/providers/{providerId}   关联的增改（全量替换）/ 删
POST /templates/{code}/test                    测试发送 {to, params}，走同一条 Send 路径，app_id 为空
GET  /logs
```

局部更新用 `PATCH`，与仓库里其他资源一致；关联是全量替换，所以用 `PUT`。

`notify_template_provider.provider_id` 的外键不写显式 `ON DELETE RESTRICT`：PG18 下它报 SQLSTATE `23001`，而 service 层的 `isForeignKeyViolation` 只认 `23503`；默认的 NO ACTION 报的就是 `23503`。

## 八、移除验证码体系

- **删除**：`internal/notify/code.go`、`internal/connector/smscode.go`、`AuthService.SendLoginCode` 与 `AuthDeps.Codes`、`LoginCodeTemplate`、gRPC `SendLoginCode`（`proto/fp/v1/auth.proto`、`grpcapi/auth_service.go`、生成代码）、SDK `Auth.SendLoginCode`、错误码 `CODE_INVALID` / `SMS_TEMPLATE_MISSING`（确认无其他引用后）。
- **`config.SMS` 不能直接删**：`Parse` 用 `KnownFields(true)`，存量系统配置版本里的 `sms:` 段会让启动直接失败，而版本快照不可改写。保留一个被忽略的 `sms` 字段（`ToYAML` 不再输出），注释写明原因。
- **`cmd/fp/main.go`**：删 `codeSvc`、`sms_code` 注册、阿里云装配块，换成通知服务的装配。
- **迁移**：`DELETE FROM application_connector WHERE connector_type = 'sms_code'`；用户记录不动。
- **示例与文档**：`examples/demo`、`examples/im-demo` 的登录方式从 `sms_code` 改为 `password`；`docs/console.md` 等引用同步。
- **保留**：`notify_log` 表、`store.RateLimiter`。阿里云的调用方式在新框架下重建为 `aliyun` type（旧的发送器随 `internal/notify` 整个删除）。

## 九、测试

- 路由：优先级分组、同组随机、禁用跳过（模板 / 关联 / 实例三层）、全部失败、IM 不降级。
- 幂等状态机：抢占、完成后重复调用、失败释放、处理中冲突。
- 校验：params 缺 / 多、`to` 规则、模式 × 渠道的合法组合、custom 占位符与 `variables` 一致；占位符识别不误伤 JSON 花括号；替换值转义（JSON / HTML / URL）。
- 各 provider 的请求构造纯函数；HTTP 类用 `httptest`；`dingtalk_bot` 加签、`webhook` 签名头（POST 签 body、GET 签 query）各一条固定向量；webhook GET 的 query 拼接（`url` 带或不带 `?`）。
- secret：读取脱敏、`PUT` 带 `********` 保持原值。
- 回归：`notify_log` 里没有 params 与渲染内容。
