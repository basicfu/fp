# 微信扫码登录（网站应用）设计

## 范围

接入微信开放平台「网站应用」登录（`scope=snsapi_login`），新登录方式 `wechat_web`。

- 网页端、桌面 App 内嵌 webview：同一套流程。
- 桌面 App 不用 webview：拉起系统浏览器，结果走自定义协议跳回，或按 state 轮询。
- PC 微信「快捷登录」由微信官方登录页自己检测，fp 不做任何处理。
- 首次微信登录**必须先绑手机号**。

不做：公众号、小程序、App 登录（以后各加一个 connector，复用本设计的会话与绑手机流程）。

## 流程

```
业务后端 StartOAuth ──> fp 存 state（Redis 5 分钟）、返回 authUrl
浏览器打开 authUrl ──> 微信登录页（扫码 / 快捷登录）
微信 302 ──> fp GET /oauth/callback?code&state
fp 用 code 换 openid / unionid / 昵称头像，按 mode 交付：
  web        302 到 returnUrl?fpTicket=…（一次性票据，2 分钟）
  desktop+协议  成功页跳 scheme://oauth?fpTicket=…
  desktop+轮询  成功页提示回到 App；结果挂在 state 上等 PollOAuth
业务后端 ExchangeOAuth(fpTicket) / PollOAuth(state) ──> 结果：
  SUCCESS     找到用户且已有手机号 → 签发会话
  BIND_PHONE  否则 → bindTicket（10 分钟），附微信昵称头像
  PENDING / CANCELLED / FAILED（仅轮询）
业务后端 SendLoginCode + BindPhone(bindTicket, phone, code) ──> SUCCESS
```

会话在 Exchange / Poll / BindPhone 时才签发（不在回调里签），IP / UA 取业务后端传来的值。

## 安全决策

- **网页端不能按 state 取结果。** 防扫码钓鱼：攻击者发起登录、把链接发给受害者，受害者登录后攻击者拿 state 取走受害者的会话。网页端结果只经 302 交给完成扫码的那个浏览器。
- **returnUrl 必须命中白名单前缀**（配置项 `returnUrls`），前缀后只能接 `/ ? #` 或结束，防开放重定向与 `a.com.evil.com` 之类的前缀绕过。
- **轮询有钓鱼风险，默认关闭**（`desktopPoll`）。配了 `desktopScheme` 的应用一律走协议跳回、不允许轮询。
- 回调对同一 state 只处理一次（`SET NX` 守卫），票据与结果都是 GETDEL 一次性消费，且校验调用方应用与 state 所属应用一致。
- 直接 `Login(connector_type="wechat_web")` 拒绝：不允许绕过 state。

## 绑手机规则（`UserService.LinkPhone`，单事务）

验证码通过后，在 phone 与 unionId 的 advisory lock 下：

1. 目标账号 = 手机号的账号 ?? 该微信已挂的账号 ?? 新建（微信昵称、头像 URL 写入 `avatar_url`）。
2. 目标账号没有这个手机号就挂上。
3. **自动换绑**：目标账号上同类型的旧微信身份删除，同一旧 unionId 的其它身份一并删除（防止以后靠 unionId 归并回来）。
4. 这个微信若挂在别的账号上（同 openid 或同 unionId），从那边摘下——用户同时证明了手机号与微信的归属。
5. 挂上新微信身份。会话不动；换绑在登录日志的 reason 里留旧 openid（脱敏）。

`SendLoginCode` 放宽为：`sms_code` 或任一需要绑手机的登录方式（`wechat_web`）启用即可发码。

## 组成

| 位置 | 内容 |
|---|---|
| `domain` | `IdentityTypeWechatWeb`；错误码 `OAUTH_STATE_INVALID` `OAUTH_TICKET_INVALID` `RETURN_URL_NOT_ALLOWED` `DESKTOP_MODE_DISABLED` `WECHAT_UPSTREAM_FAILED` `PUBLIC_URL_MISSING` |
| `connector` | `OAuthConnector` 接口（`AuthorizeURL`、`Exchange`）；`wechat_web` 实现，微信 API 基址可注入（测试用 httptest） |
| `service` | `ApplicationService` 的登录方式配置接上 `MaskSecrets` / `MergeSecrets`；`AuthService` 新增 `StartOAuth` `HandleOAuthCallback` `ExchangeOAuth` `PollOAuth` `BindPhone`，`Login` 的签发段抽成 `completeLogin` 复用；Redis 键 `fp:oauth:*` |
| `config` | 系统配置新增 `public_url`（fp 公网地址，拼 `redirect_uri`），未配时 StartOAuth 报 `PUBLIC_URL_MISSING` |
| `httpapi` | 公开路由 `GET /oauth/callback`，结果页是内联 HTML |
| `proto` / `grpcapi` | `StartOAuthLogin` `ExchangeOAuthTicket` `PollOAuthLogin` `BindPhoneLogin` |
| `sdk` | `Auth.StartOAuth` `ExchangeOAuth` `PollOAuth` `BindPhone`；`fpsdk.OAuthHandler`（net/http，一行挂载 start / exchange / poll / send-code / bind，JSON 错误体 `{code,msg}`，`OnLogin` 决定 token 怎么交给前端） |
| `web` | 登录方式中文名 |
| `docs/wechat-login.md` | 接入文档：开放平台配置、各端前端示例 |

## wechat_web 配置项

`appId`（必填）、`appSecret`（必填，secret）、`returnUrls`（逗号或换行分隔）、`desktopPoll`（bool，默认 false）、`desktopScheme`（如 `myapp`）。

## 测试

- connector：假微信服务器覆盖换 token、取用户信息、errcode、用户信息失败时降级。
- service：老用户直登；新微信绑新手机建号；绑已有手机号挂到老账号；自动换绑删旧身份（含同 unionId）；微信从别的账号挪过来；票据一次性；网页模式不能轮询；桌面开关；returnUrl 白名单；回调重放。
- 配置脱敏：读出掩码，写回掩码保持原值。
- SDK：`OAuthHandler` 五个接口的 JSON 契约与错误映射。
