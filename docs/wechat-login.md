# 微信扫码登录接入文档

登录方式 `wechat_web`，对应微信开放平台的「网站应用」登录。网页端、桌面 App 都用它。用户的电脑上登着 PC 微信时，微信官方登录页会自动切成头像加「微信快捷登录」，否则显示二维码。这个检测由微信官方页面完成，接入方不用处理。

**首次用微信登录必须绑手机号**。绑定之后，再扫码就直接登录。

## 1. 选哪种模式

由业务方判断自己运行在什么环境：

| 运行环境 | mode | 结果怎么回来 |
|---|---|---|
| 网页 | `web` | 浏览器 302 回到 `ReturnURL?fpTicket=…`，业务后端用票据换结果 |
| 桌面 App，内嵌 webview（Electron、Tauri、WebView2、CEF 等） | `web` | 同网页 |
| 桌面 App，拉起系统浏览器，配了自定义协议 | `desktop` | 结果页跳到 `myapp://oauth?fpTicket=…`，App 用票据换结果 |
| 桌面 App，拉起系统浏览器，没配协议 | `desktop` | App 按 `state` 轮询 |

**按 state 轮询有扫码钓鱼风险**：攻击者可以发起登录，把链接发给受害者，受害者登录后，攻击者拿自己手里的 state 取走受害者的会话。所以轮询默认关闭，配了自定义协议的应用一律不允许轮询，网页模式的 state 也不能拿来轮询。

## 2. 准备

1. **微信开放平台**：创建「网站应用」并通过审核，拿到 AppID 和 AppSecret。「授权回调域」填 **fp 的公网域名**，不是业务方的域名。想和公众号、小程序打通账号，需要把这些应用绑在同一个开放平台账号下，fp 按 unionId 归并。
2. **fp 系统配置**：在控制台「系统配置」里填 `public_url: https://fp.example.com`，保存后重启 fp。没填时发起登录返回 `PUBLIC_URL_MISSING`。
3. **通知中心**：配好 `verify_code` 短信模板，绑手机时要发验证码，配法见 [通知文档](notify.md#6-登录验证码)。应用不用另外开启 `sms_code`。
4. **控制台 →「应用」→「登录方式」→「微信扫码登录」**：

| 配置项 | 说明 |
|---|---|
| AppID / AppSecret | 必填。AppSecret 保存后只显示掩码 |
| 允许的网页回跳地址 | `web` 模式回跳页地址的前缀，多个用逗号分隔，例如 `https://app.example.com/login/wechat` |
| 桌面 App 自定义协议 | 例如 `myapp`。配了之后，`desktop` 模式只走协议跳回 |
| 允许桌面端轮询 | 默认关闭。只给没有自定义协议的桌面客户端打开 |

## 3. 业务后端（Go）

挂一个 Handler 就够了：

```go
mux.Handle("/auth/wechat/", client.Auth().OAuthHandler(fpsdk.OAuthHandlerOptions{
    ReturnURL:    "https://app.example.com/login/wechat", // 必须在回跳白名单里
    CookieSecure: true,                                   // 生产必须
}))
```

挂载后得到下面这些接口，请求和响应都是 JSON：

| 接口 | 请求 | 响应 |
|---|---|---|
| `POST /auth/wechat/start` | `{mode}` | `{state, authUrl, appId, redirectUri, expiresIn}` |
| `POST /auth/wechat/exchange` | `{ticket}` | 结论 |
| `GET /auth/wechat/poll?state=` | | 结论 |
| `POST /auth/wechat/send-code` | `{phone}` | 204 |
| `POST /auth/wechat/bind` | `{bindTicket, phone, code}` | 登录成功 |

- **结论**：`{status}`，`status` 取以下值：
  - `SUCCESS`：登录成功，响应格式见下一条。
  - `BIND_PHONE`：首次登录要绑手机号，响应额外带 `bindTicket`、`nickname`、`avatarUrl`。
  - `PENDING`：还在等用户扫码，只有轮询会出现。
  - `CANCELLED`：用户取消了授权。
  - `FAILED`：登录失败。
- **登录成功**：默认写 `fp_token` cookie（和 `Middleware` 默认读取的 cookie 一致），并返回 `{status:"SUCCESS", token, sessionId, user}`。桌面 App 不用 cookie，可以直接取 `token`。要自己决定怎么交付 token，就传 `OnLogin`。
- **失败**：返回 `{code, msg}`，`msg` 可以直接展示给用户，HTTP 状态码和 `fpsdk.WriteError` 一致。
- 业务后端在反向代理后面时，要传 `ClientIP`（从 `X-Forwarded-For` 取）；要区分移动端空闲超时，就传 `Mobile`。

不用 Handler 时，直接调底层方法：`StartOAuth`、`ExchangeOAuth`、`PollOAuth`、`SendLoginCode`、`BindPhone`。

## 4. 前端

下面的示例里，`post` 是 `fetch` 加 JSON 的简单封装。

### 网页端、桌面 webview

```js
// 登录按钮
const r = await post('/auth/wechat/start', { mode: 'web' })
location.href = r.authUrl

// 回跳页 /login/wechat
const q = new URLSearchParams(location.search)
history.replaceState(null, '', location.pathname) // 一次性票据别留在地址栏和历史记录里
if (q.get('fpError')) {
  show(q.get('fpError') === 'cancelled' ? '已取消登录' : '登录失败，请重试')
} else {
  const res = await post('/auth/wechat/exchange', { ticket: q.get('fpTicket') })
  if (res.status === 'SUCCESS') goHome()
  else if (res.status === 'BIND_PHONE') showBindForm(res) // 可以展示 res.nickname、res.avatarUrl
}

// 绑手机表单
await post('/auth/wechat/send-code', { phone })
const res = await post('/auth/wechat/bind', { bindTicket, phone, code })
// 验证码错了返回 {code:'CODE_INVALID', msg}，同一个 bindTicket 可以重试，10 分钟内有效
```

想把二维码嵌在自己的页面里，可以用微信官方的 `wxLogin.js`，参数取 `start` 返回的 `appId`、`redirectUri`、`state`，`scope` 填 `snsapi_login`，`self_redirect` 保持默认的 `false`。fp 的回调页禁止被 iframe 嵌入，必须让顶层窗口跳转。之后的流程和跳转版完全一样。

### 桌面 App：系统浏览器 + 自定义协议

```js
const r = await post('/auth/wechat/start', { mode: 'desktop' })
shell.openExternal(r.authUrl) // 用系统浏览器打开

// App 注册的 myapp:// 协议被唤起时：
app.on('open-url', async (_, link) => {
  const q = new URL(link).searchParams
  if (q.get('fpError')) return show('登录失败，请重试')
  const res = await post('/auth/wechat/exchange', { ticket: q.get('fpTicket') })
  // 后面和网页端一样：SUCCESS 用 res.token；BIND_PHONE 在 App 里显示绑手机表单
})
```

### 桌面 App：系统浏览器 + 轮询

```js
const r = await post('/auth/wechat/start', { mode: 'desktop' })
shell.openExternal(r.authUrl)
const deadline = Date.now() + r.expiresIn * 1000
const timer = setInterval(async () => {
  const res = await get('/auth/wechat/poll?state=' + r.state)
  if (res.status === 'PENDING' && Date.now() < deadline) return
  clearInterval(timer)
  // SUCCESS → res.token；BIND_PHONE → 绑手机表单；CANCELLED / FAILED / 超时 → 提示重新发起
}, 1500)
```

结果只能取一次，取走后再轮询会返回 `OAUTH_STATE_INVALID`。

## 5. 绑手机规则

- 手机号已经有账号：把微信绑到这个账号上。这个账号原来绑的微信会被**自动换下**，旧微信同一 unionId 下的其它身份也一并删除。旧微信以后再扫码，会重新进入绑手机流程。
- 手机号没有账号：新建一个账号，用微信昵称和头像作为初始资料（头像存在 `avatar_url` 里，规则见[用户文档](user.md)）。
- 这个微信原来挂在别的账号上：挪到手机号对应的账号。用户同时证明了自己拥有这个手机号和这个微信。
- 换绑不影响已登录的会话。登录日志的「原因」一栏会记下被换下的旧 openid（脱敏后）。

## 6. 错误码

| 码 | 什么时候出现 | 怎么处理 |
|---|---|---|
| `OAUTH_STATE_INVALID` | `state` 过期（5 分钟）、已经取走，或者不是按轮询发起的 | 重新发起 |
| `OAUTH_TICKET_INVALID` | `fpTicket`（2 分钟）或 `bindTicket`（10 分钟）过期或已经用过 | 重新扫码 |
| `RETURN_URL_NOT_ALLOWED` | `ReturnURL` 不在回跳白名单里 | 到控制台补白名单 |
| `DESKTOP_MODE_DISABLED` | 应用既没配协议，也没开轮询 | 到控制台配置 |
| `PUBLIC_URL_MISSING` | 系统配置没填 `public_url` | 填好后重启 fp |
| `CONNECTOR_DISABLED` | 应用没启用微信扫码登录 | 到控制台启用 |
| `CODE_INVALID` | 绑手机时验证码错误或已过期 | 让用户重输 |

微信接口本身失败时（比如 AppSecret 配错、code 失效），不会以错误码的形式返回给业务方：网页端回跳时带 `fpError=failed`，轮询拿到 `FAILED`。具体原因写在 fp 的日志里，搜索「三方 code 换身份失败」。
