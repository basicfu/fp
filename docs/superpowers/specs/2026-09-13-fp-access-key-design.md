# fp 访问密钥（AccessKey）设计

**日期：** 2026-09-13
**状态：** 已确认，待实现

## 一、目标与范围

第三方公司的程序调用业务方接口，不走账号登录，改用 AccessKey/Secret：控制台创建一对 key，备注是哪个合作方，绑定一个角色，角色上授权要开放的接口。

同期一并完成：

- 匿名请求按内置 GUEST 角色鉴权。现状是没带凭据的请求一律 401。
- 补发 `PolicyChanged`。现状是服务端从未发送，控制台改授权后，运行中的 SDK 要到重启或断线重连才生效。

## 二、核心规则

| | 普通用户 | 匿名请求 | 访问密钥 |
|---|---|---|---|
| 鉴权用的角色 | 全局角色 ∪ 应用默认角色 ∪ GUEST | GUEST | 只有绑定的角色（可以不绑） |
| 鉴权被拒 | 403 | 401 | 403 |

- key 是**全局**的，不属于应用。能调哪个应用的哪个接口，由绑定角色在各应用的权限点决定。
- 只有签名调用，没有直接传 SK 的方式。
- 带了凭据（token 或访问密钥）但校验不通过，直接拒绝（401/403，见第七节），**不降级成匿名**。否则签错的 key、过期的 token 会拿到 GUEST 的接口，用户也不会被提示重新登录。
- 约定：业务方所有路由都挂鉴权。

## 三、签名协议

| 请求头 | 值 |
|---|---|
| `X-Fp-Access-Key` | AK |
| `X-Fp-Timestamp` | Unix 秒，与服务器时间相差不超过 15 分钟 |
| `X-Fp-Nonce` | 每次请求不同，`[A-Za-z0-9_-]{1,64}`，建议 UUID |
| `X-Fp-Signature` | 签名，小写十六进制（校验时不区分大小写） |

```
StringToSign = 方法 + "\n"
             + 原样路径 + "\n"
             + 原样 query + "\n"      // 没有 query 为空串
             + 时间戳 + "\n"
             + nonce + "\n"
             + hex(SHA256(body))      // 没有 body 为 e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
Signature    = hex(HMAC-SHA256(SK, StringToSign))
```

- 路径和 query 取请求行里的原始字节（Go 中为 `r.RequestURI` 的 path 与 query 部分），不解码、不重新编码、不排序。
- HMAC 的密钥是 SK 字符串本身的字节，不做 base64 解码。
- 请求头不参与签名。
- AK：`FPAK` + 20 位 `[A-Z0-9]`。SK：32 字节随机数的 base64url 编码（43 位）。

## 四、数据模型

迁移 `00011_access_key.sql`：

```sql
CREATE TABLE access_key (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    access_key_id text NOT NULL UNIQUE,
    secret        text NOT NULL,                  -- 明文，见「已接受的风险」
    remark        text NOT NULL,
    role_key      text REFERENCES role(key),  -- 可为空；默认的 NO ACTION 挡住删除，不做 SET NULL/CASCADE
    allowed_ips   cidr[] NOT NULL DEFAULT '{}',   -- 空表示不校验 IP，最多 50 条
    status        text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'DISABLED')),
    expires_at    timestamptz,                    -- NULL 表示永不过期
    last_used_at  timestamptz,                    -- 精确到分钟
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

INSERT INTO role (key, name) VALUES ('GUEST', '访客') ON CONFLICT (key) DO NOTHING;
```

- 展示状态由 `status` 和 `expires_at` 算出：已停用（优先）/ 已过期 / 正常。**过期不改 status**，不需要定时任务；恢复靠重新设置有效期。
- 有效期按天输入：0 存 `NULL`，N 存 `now() + N 天`。编辑时留空表示不修改。
- IP 白名单输入单个 IP 或网段（IPv4/IPv6），存储前规范化（单个 IP 存为 /32 或 /128，网段去掉主机位）。
- 删除被 key 绑定的角色：由外键拦下，返回 `ROLE_IN_USE`（带绑定数量）。如果静默摘掉，合作方的调用会突然全部 403。
- key 不能绑定 GUEST。
- 不提供重置 SK。轮换流程：新建同角色 key → 合作方切换 → 停用并删除旧 key。删除是真删。

## 五、GUEST

- 内置全局角色，key 固定为 `GUEST`；库里已有同名角色时直接沿用。常量 `GuestRoleKey` 放在 `sdk/authzcore`，服务端与 SDK 共用。
- GUEST 在 SDK 判定时并入：`Authz.Allow` 对非访问密钥的身份一律追加 GUEST。不写进会话，已登录用户立即生效。`AllowRoles` 仍然只用调用方显式传入的角色。
- 限制：不能删除；不能设父角色；授权只能是「允许」。所有登录用户都拥有 GUEST，给它配「拒绝」会连带拒掉所有人。
- `AllowGuest` 含义不变：只决定是否采信 `X-Guest-Id` 并填入 `Identity.GuestID`。带不带访客头都按匿名处理；开启 `AllowGuest` 时访客头格式非法仍回 401（现有行为）。

## 六、fp 服务端

### RPC

加在 `AuthService`，沿用应用凭据认证；拒绝 IM 网关调用；调用方应用必须是启用状态。

```protobuf
rpc GetAccessKey(GetAccessKeyRequest) returns (GetAccessKeyResponse);
rpc ReportAccessKeyUsage(ReportAccessKeyUsageRequest) returns (ReportAccessKeyUsageResponse);

message GetAccessKeyRequest  { string access_key_id = 1; }
message GetAccessKeyResponse {
  string secret = 1;
  string remark = 2;
  repeated string roles = 3;        // 0 或 1 个
  repeated string allowed_ips = 4;  // CIDR
  int64 cache_ttl_ms = 5;           // min(应用的 token_cache_ttl, 到期剩余时间)，0 表示不缓存
}
message ReportAccessKeyUsageRequest { repeated AccessKeyUsage usages = 1; }
message AccessKeyUsage { string access_key_id = 1; int64 last_used_at_ms = 2; }
```

- `GetAccessKey`：key 不存在、已停用、已过期分别返回对应错误码，不返回 SK。
- `ReportAccessKeyUsage`：只写比库里新的时间；晚于当前时间的按当前时间记。最后使用时间由 SDK 上报，因为只有 SDK 知道签名是否通过。

### 推送

复用 `fp:revoke` 频道和 `RevokeHub`，尽力送达。Redis 订阅重建时，现有的 Purge 兜底同样覆盖这两种事件。

| 事件 | 触发 | 发给 |
|---|---|---|
| `AccessKeyChanged{access_key_id}`（`WatchResponse` 新增字段 8） | key 的修改、停用、删除（新建不推：SDK 还没有它的缓存） | 所有应用 |
| `PolicyChanged`（已有字段 4，补上发送） | 改角色授权、删除或修改权限点 | 权限点所属应用 |
| `PolicyChanged` | 改角色继承、删除角色 | 所有应用 |

### 控制台接口（`/admin/api`）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/access-keys` | 列表，可按角色筛选，不含 SK |
| POST | `/access-keys` | 新建（备注、角色、有效期天数、IP 白名单），返回 AK 与 SK；SK 只在这里出现一次 |
| GET | `/access-keys/{id}` | 详情，不含 SK |
| GET | `/access-keys/{id}/permissions` | 当前能调用的接口，按应用分组，角色继承已展开 |
| PATCH | `/access-keys/{id}` | 改备注、角色、IP 白名单、有效期；省略的字段不修改 |
| PATCH | `/access-keys/{id}/status` | 启用 / 停用 |
| DELETE | `/access-keys/{id}` | 删除 |

### 错误码

| 码 | 情况 | 哨兵 |
|---|---|---|
| `ACCESS_KEY_INVALID` | AK 不存在 | Unauthorized |
| `ACCESS_KEY_DISABLED` | key 已停用 | Forbidden |
| `ACCESS_KEY_EXPIRED` | key 已过期 | Forbidden |
| `ROLE_IN_USE` | 删除被 key 绑定的角色 | Conflict |
| `ROLE_BUILTIN` | 删除 GUEST、给 GUEST 设父角色或配「拒绝」、key 绑定 GUEST | InvalidArgument |

## 七、业务方 SDK

### 中间件

不新增开关。凭据按以下顺序判定：

1. 请求带 `X-Fp-Access-Key`：只走访问密钥校验。
2. 否则带 token：走 token 校验。
3. 都没有：匿名身份。

访问密钥的校验顺序与失败响应：

| 步骤 | 失败情况 | 状态码 | 错误码 |
|---|---|---|---|
| 1. 四个请求头齐全、格式正确 | 缺失或格式错误 | 401 | `SIGNATURE_INVALID` |
| 2. 时间戳在 ±15 分钟内 | 超出窗口 | 401 | `TIMESTAMP_EXPIRED` |
| 3. 取 key 信息（本地缓存或 `GetAccessKey`） | 不存在 / 已停用 / 已过期 | 401 / 403 / 403 | `ACCESS_KEY_*` |
| | fp 不可达且没有可用缓存 | 503 | — |
| 4. 读取 body 并放回给 handler | 超过上限 | 413 | `BODY_TOO_LARGE` |
| 5. 常量时间比对签名 | 不匹配 | 401 | `SIGNATURE_MISMATCH`，响应体附 SDK 算出的 StringToSign |
| 6. nonce 在本机没出现过 | 重复 | 401 | `NONCE_USED` |
| | 容量已满 | 503 | — |
| 7. 来源 IP 在白名单内（白名单非空时才检查） | 不在白名单 | 403 | `IP_DENIED` |

- key 信息缓存与 token 同一套规则：TTL 由 fp 下发；推送流断开时收紧到 `DegradedCacheTTL`；同一 AK 的并发回源合并；`AllowStaleOnOutage` 兜底；失败不缓存。收到 `AccessKeyChanged` 丢弃该 key 的缓存，收到 Purge 全部丢弃。
- nonce：每个 `Client` 一份内存表，键为 AK + nonce，保留到时间戳超出窗口为止。签名通过后才记录。满了就拒绝新请求，不挤掉旧记录（挤掉等于关掉防重放）。
- body 上限默认 10MB，nonce 容量默认 20 万条，都可以在 `Options` 里调整。
- 来源 IP 取 `X-Forwarded-For` 的第一个地址，没有这个头时取 `RemoteAddr`。
- 响应体只写错误码和固定文案，`OnError` 可以整个覆盖。

### 身份与鉴权

- `Identity` 新增 `AccessKeyID`、`AccessKeyRemark`，与 `UserID` 互斥。新增 `IsAccessKey()`，以及 `IsAnonymous()`（`UserID` 和 `AccessKeyID` 都为空）。匿名请求的 `IdentityFrom` 也返回身份。
- fpchi：鉴权被拒时，匿名请求回 401，其余回 403。

### 后台任务

- 访问密钥请求全部检查通过后，按分钟截断记录使用时间；每 60 秒批量调用一次 `ReportAccessKeyUsage`，失败的并入下一批，`Close` 时再报一次。
- 每 5 分钟重拉一次策略，兜底丢失的 `PolicyChanged`。

### 签名包

`sdk/aksign`：只依赖标准库的签名实现，给使用 Go 的第三方直接引用，集成测试也用它。

## 八、控制台

- 菜单「访问密钥」放在「用户管理」之后。页面：`/access-keys`、`/access-keys/:id`。
- 列表列：AK（可复制）、备注、角色、IP 白名单（不限制 / N 条）、状态、到期时间、最后使用（到分钟 / 从未使用）、操作。支持关键字筛选和 `?role=` 筛选，不分页。
- 新建：备注（必填）、角色（可不绑，下拉里没有 GUEST）、有效期天数（默认 0）、IP 白名单（每行一个）。成功后弹出一次性展示框：AK、SK 各带复制按钮；点遮罩或按 Esc 不关闭，只能点「我已保存」；附对接文档链接。
- 详情：基本信息与操作按钮；「可调用的接口」按应用分组展示；编辑时有效期留空不修改；停用、删除要二次确认，确认框里显示最后使用时间。
- 角色列表：GUEST 标「内置」，删除按钮和「继承自」不可用。
- 角色详情：绑定了 key 时，顶部提示绑定数量，并链接到筛选后的密钥列表。GUEST 的授权只有「未授权 / 允许」两档，并说明它的含义。
- 用户详情的角色卡片补一行「GUEST（所有人）」。

## 九、文档与部署

- 新增 `docs/access-key.md`：签名规则、测试向量、错误码、部署要求。SDK README 补「访问密钥」「GUEST」两节。`examples/demo` 补一个第三方调用示例。
- 部署要求：
  - 最外层代理用真实客户端地址**覆盖** `X-Forwarded-For`（nginx：`proxy_set_header X-Forwarded-For $remote_addr`）。
  - 所有路由挂鉴权。
- 发布顺序：先发 fp，再升级 SDK。

## 十、测试

标【辨别力】的用例，实现时要把代码改坏跑一次，确认会失败。

- **签名：** 测试向量（文档里的向量由测试校验）；`aksign` 生成的签名能通过 SDK 校验；【辨别力】六个字段逐个篡改都会失败；覆盖 `%2F` 路径、query 保持原顺序、空 query、空 body、SK 不做解码。
- **SDK 校验：** 第七节表格每一行一条用例；【辨别力】先用 nonce N 发一个签名错误的请求，再用 N 发正确的请求，后者必须成功；nonce 按时间戳过期（可控时钟）；容量满回 503；`X-Forwarded-For` 取值；body 放回后 handler 读到原字节。
- **GUEST：** 匿名调 GUEST 授权的接口放行、调未授权接口回 401、登录用户被拒回 403；【辨别力】会话角色里没有 GUEST 时，登录用户仍能调 GUEST 授权的接口；【辨别力】接口授权给 GUEST 后，没绑角色的 key 调用回 403，签错的 key 和失效的 token 调用回 401；内置 GUEST：迁移沿用已有同名角色、不能删除、不能设父角色、不能配「拒绝」、key 不能绑定。
- **服务端：** `GetAccessKey` 三种错误与 `cache_ttl` 取值；IM 网关、停用应用调用被拒；使用时间只增不减、未来时间按当前时间记；`ROLE_IN_USE` 由外键拦下；【辨别力】只改备注时到期时间不变。
- **推送（测到 SDK 出口）：** 【辨别力】把 `token_cache_ttl` 调长后停用 key，SDK 立即拒绝；【辨别力】改角色授权后 SDK 的 `Allow` 结果随之变化，不用重启；事件丢失时靠 5 分钟重拉追上；Purge 覆盖两种新事件；上报后 `last_used_at` 更新。
- **控制台：** 一次性展示框关不掉；有效期留空时请求不带该字段；GUEST 没有「拒绝」选项；删除角色的提示带绑定数量。
- **验收：** 建角色 → 授权接口 → 建 key（配 IP 白名单）→ 用 `aksign` 调 demo 得到 200 → 收回授权得到 403 → 停用 key 得到 403 → 控制台出现最后使用时间 → 匿名请求能调 GUEST 授权的接口。

## 十一、不做的事与已接受的风险

**本期不做：** 按 key 限流（交给网关）、SK 加密存储、跨实例 nonce 去重、请求头签名、一把 key 绑定多个角色、重置 SK、非 Go 语言的签名 SDK、调用明细与管理操作审计。

**已接受的风险：**

1. SK 缓存在业务方 SDK 的进程内存里。
2. nonce 只在单个实例内去重，同一个请求重放到另一个实例能通过。
3. SK 明文存库，数据库内容泄露即所有 SK 泄露。以后改成加密存储时 SK 的值不变，合作方无感知。
4. 来源 IP 信任 `X-Forwarded-For` 的第一个地址，由部署保证其可信。
5. 请求头不在签名范围内。

**相关缺口（不在本设计内）：** 用户改角色后要重新登录才生效。服务端从未发送 `UserRoleChanged`，而且角色写在会话里。
