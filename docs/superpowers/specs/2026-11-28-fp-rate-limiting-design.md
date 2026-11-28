# fp 安全频率限制设计

- 日期:2026-11-28
- 状态:设计已与用户逐条确认,待实施
- 相关:kkdesk 仓库 `docs/specs/2026-11-28-rate-limiting-design.md`(**通用机制全文与 kkdesk 侧规则,两边必须同步修改**)、kkdesk 仓库 `docs/specs/2026-11-22-account-fp-direct-design.md`(账号接口直连 fp)、kkdesk 仓库 `docs/specs/2026-11-24-unified-http-response-design.md`(统一响应)、[微信登录](../../wechat-login.md)
- 起因:登录和账号接口迁到 fp 直连后,fp 自己要承担防撞库、验证码轰炸、绑定与注销滥用、IM 连接洪水的限频。现状只有发码按收件人限频、验证码每码 5 次、IM 访客按 IP 去重计数,密码登录没有任何失败计数

## 1. 现状(2026-11-28 核对)

- 终端用户账号接口 `internal/accountapi`(`/api/account/*`)经 apisix 直达 fp,`withClientIP` 只在直连方是可信代理时取 `X-Forwarded-For` 最后一跳。该组接口只经 apisix 对外,不直接暴露公网。
- kkdesk 控制台和社区登录仍走 gRPC `AuthService.Login`(`LoginInput.IP`),所以登录类规则要放在 **service 层**,不能只挂在 HTTP 处理函数上。
- 现有限频:`LoginCodeService.AllowSend` 按收件人逐条规则调用(默认 30 秒 1 条、1 小时 5 条、1 天 10 条,来自配置文件 `verify_code.rate_limit`,短信、邮件各一份),一次发码 3 次 Redis 往返;`store.RateLimiter` 是首次计数起算的固定窗口;验证码每码最多校验 5 次(`verifyScript`,用尽即删码);IM 访客 `GuestLimiter`(进程内、按 app 加 IP 每分钟新 id 数,`GuestIPRate`)。
- 密码连接器(`internal/connector/password.go`)无失败计数,登录失败只写 `login_log`(带 IP、UA、脱敏标识)。
- 验证码校验的结果被折成同一句"验证码不正确或已过期",输错、过期、用尽无法区分,日志里也没有"次数用尽"的痕迹。
- fp 的 gRPC `SendLoginCode`、`StartOAuth`、`ValidateToken` 等调用的参数里没有终端 IP(登录、`ExchangeOAuth`、`BindPhone` 有)。

## 2. 通用机制摘要

完整说明见 kkdesk 文档 §3,这里只列 fp 落地要用到的:

- **规则**:名称 + 最多 3 档(窗口,次数);任一档超限拒绝;冷却是被触发那一档的剩余窗口;无"关闭";漏配走代码默认值。配置只有档位。
- **计法**:请求(动作前 `Hit`,`计数 > 次数` 拒绝)、失败(动作前 `Check`,`计数 >= 次数` 即被挡;失败后 `Fail` +1)。成功后清零只有"密码失败(登录标识)"一条,写在注册表里,不进配置。
- **判定顺序**:先判断再验证;被挡请求不计入失败计数;账号不存在也照样计数,响应一致。
- **批量脚本**:一个动作涉及的全部规则、全部档位,一次 `EVALSHA`,先检查全部、全部通过才一起计数。单节点 Redis 前提(fp 主库 `redis.NewClient`,单节点)。
- **IP**:IPv4 按 /32、IPv6 按 /64,IPv4 映射还原;缺失则只跳过 IP 维度并 warn。
- **Redis 故障**:拒绝并返回"服务暂不可用";限频调用单独设短超时(100 毫秒量级)。
- **观察模式**:规则可设为只记录不拦截。fp 侧阈值都不是估算性质,可直接拦截;如要放观察,以规则为单位开关。

## 3. 规则表

键名用 `<动作>.<维度>`。窗口语义沿用 `store.RateLimiter`(首次计数起算的固定窗口)。

| 键 | 规则 | 维度 | 计法 | 成功后清零 | 档1 | 档2 | 档3 | 来源 |
|---|---|---|---|---|---|---|---|---|
| `pwd_fail.account` | 密码失败 | 登录标识 | 失败 | ✅ | 15m/5 | | | 新增 |
| `verify_fail.ip` | 验证失败(密码、短信验证码、邮箱验证码) | IP | 失败 | ❌ | 1d/100 | | | 新增 |
| `sms_send.target` | 短信发码 | 收件人 | 请求 | — | 30s/1 | 1h/5 | 1d/10 | 已有,配置文件作默认 |
| `sms_send.ip` | 短信发码 | IP | 请求 | — | 1h/20 | 1d/100 | | 新增 |
| `email_send.target` | 邮件发码 | 收件人 | 请求 | — | 30s/1 | 1h/5 | 1d/10 | 已有,配置文件作默认 |
| `email_send.ip` | 邮件发码 | IP | 请求 | — | 1h/20 | 1d/100 | | 新增 |
| `wechat_login.ip` | 微信登录 | IP | 请求 | — | 1m/30 | 1h/300 | | 新增 |
| `sms_rebind.user` | 短信换绑 | 用户 | 请求 | — | 1h/5 | 1d/10 | | 新增 |
| `email_rebind.user` | 邮箱换绑 | 用户 | 请求 | — | 1h/5 | 1d/10 | | 新增 |
| `wechat_bind.user` | 微信绑定 | 用户 | 请求 | — | 1h/20 | | | 新增 |
| `notify_qr.user` | 订阅公众号 | 用户 | 请求 | — | 1h/10 | | | 新增 |
| `account_delete.user` | 注销申请 | 用户 | 请求 | — | 1d/2 | | | 新增 |
| `im_connect.ip` | IM 连接 | IP | 请求 | — | 1m/60 | 1h/600 | | 新增,替换访客限速 |

"登录标识"指 `DetectIdentityType` 判出的类型加 `normalizeSubject` 规整后的值(邮箱、用户名转小写去空白,手机号只去空白),与库里 `UNIQUE (type, subject)` 的字面比较同一口径;key 限长并取哈希,避免攻击者用超长随机账号名造出大量键。

## 4. 接口映射与判定位置

计数点按 `internal/accountapi` 的 15 个接口列出。"提交前"指进入校验之前,"拒绝范围"指超限时拒绝什么。

| 接口(参数) | 提交前 | 失败后 | 成功后 | 拒绝范围 |
|---|---|---|---|---|
| `POST /login` `type=password` | `Check`:`pwd_fail.account`、`verify_fail.ip` | `Fail` 两条(密码错) | `Reset`:`pwd_fail.account` | 该标识的密码登录;该 IP 的校验类接口 |
| `POST /login` `type=sms` | `Check`:`verify_fail.ip` | `Fail`:`verify_fail.ip`(验证码错、过期、用尽) | — | 该 IP 的校验类接口 |
| `POST /login` `type=wechat_phone` | 同 `sms` | 同 `sms` | — | 同 `sms` |
| `POST /login` `type=wechat_app` | `Hit`:`wechat_login.ip` | — | — | 该 IP 的微信登录 |
| `POST /login` `type=wechat` | 不计(换票只读 Redis,不请求微信) | — | — | — |
| `POST /wechat/start` `purpose=login` | `Hit`:`wechat_login.ip` | — | — | 该 IP 的微信登录 |
| `POST /wechat/start` `purpose=bind` | `Hit`:`wechat_bind.user` | — | — | 该用户的微信绑定发起 |
| `POST /code` `purpose=login`(短信) | 滑块通过后、发送前一次批量 `Hit`:`sms_send.target`、`sms_send.ip` | — | — | 发往该号码、来自该 IP 的短信发码 |
| `POST /code` `purpose=verify` / `password`(短信,发到当前手机号) | 一次批量 `Hit`:`sms_send.target`、`sms_send.ip` | — | — | 同上 |
| `POST /code` `purpose=bind` `channel=sms` | 一次批量 `Hit`:`sms_send.target`、`sms_send.ip`、`sms_rebind.user` | — | — | 同上,另限该用户换绑次数 |
| `POST /code` `purpose=bind` `channel=email` | 一次批量 `Hit`:`email_send.target`、`email_send.ip`、`email_rebind.user` | — | — | 同上 |
| `POST /code/verify` | `Check`:`verify_fail.ip` | `Fail`:`verify_fail.ip` | — | 该 IP 的校验类接口 |
| `POST /password` | `Check`:`verify_fail.ip` | 验证码错时 `Fail` | — | 同上 |
| `POST /identity/bind` `type=phone` / `email` | `Check`:`verify_fail.ip` | 验证码错时 `Fail` | — | 同上 |
| `POST /identity/bind` `type=wechat` | 不计(发起已计) | — | — | — |
| `GET /notify/qr` | `Hit`:`notify_qr.user` | — | — | 该用户的订阅二维码 |
| `POST /delete` | `Hit`:`account_delete.user` | — | — | 该用户的注销申请 |
| `/captcha`、`/me`、`/profile`、`/avatar`、`/logout`、`/identity/unbind`、`/notify/off` | 不限 | — | — | — |

要点:

- **发码的收件人规则必须在滑块通过之后才计数**。放在滑块之前,攻击者故意答错滑块就能烧掉受害号码的额度。收件人和 IP 规则放在同一次批量调用里,所以 IP 规则也在滑块之后;滑块本身没有成本,不额外保护。
- 发码的收件人规则对所有 `purpose` 共用一个计数(沿用现状:登录、验身份、换绑、设密码共用),所以 `verify`、`password` 发到当前手机号的,也走 `sms_send.target`。
- `purpose=verify`、`password` 只发到账号当前绑定的号码,不认客户端给的 `target`;不需要"换绑"规则。
- **"验证失败"只统计校验类的 `ErrInvalidCredential`**:密码错、验证码错、过期、次数用尽。不统计 `bad_request`、滑块失败、连接器未启用、系统错误。账号管理侧(`/code/verify`、`/password`、`/identity/bind` 的验证码错)也并进同一个计数器。
- **验码次数用尽单独记日志**:`verifyScript` 已区分"输错(0)、无码(-1)、用尽(-2)"。对外响应仍是同一句提示(防枚举),对内在 `login_log` 的 reason 和一条 warn 日志里标明 `code_attempts_exhausted`(带脱敏手机号和 IP),这是批量尝试的最好告警信号。

## 5. 放在 service 层

规则判定放在 service 层各入口,gRPC(kkdesk 控制台、社区)和 HTTP(`/api/account/*`)共用,不会被任何一条入口绕过:

- `AuthService.Login`:覆盖密码和短信两种连接器。
- `AuthService.BindPhone`、账号管理侧的验证码校验(`VerifyCode`、`BindIdentity`)、`SetPassword` 前的验证。
- `AuthService.SendLoginCode`、`SendVerifyCode`:用批量 `Hit` 替换原来的 `Codes.AllowSend`。
- `StartOAuth` / `StartBindOAuth`、`ExchangeAppCode`、`RequestDeletion`、`WechatSubscribeQR`。

**IP 通过 `context` 传递**:`accountapi` 中间件 `withClientIP` 把 IP 放进 ctx;gRPC 处理函数把 `LoginInput.IP`、`OAuthClient.IP` 放进 ctx;没有 IP 字段的 gRPC 调用(`SendLoginCode` 等)IP 缺失,只跳过 IP 维度。社区经 gRPC 发码因此只受收件人规则约束,这是已知并接受的缺口,直到社区接入通用登录。

## 6. IM 连接

- 规则 `im_connect.ip`:握手阶段按 IP 计数,**token、访客都算**,放在读认证帧之前(`internal/im/wsapi/handler.go` 的握手入口),未认证的洪水尽早拒绝。超限沿用现有的拒绝帧和关闭码(`CloseAuthFailed` + `rate_limited`),文案"连接过于频繁,请稍后再试"。
- 用主库 Redis(`store`),不用 `im/redisx` 那套(它支持集群,批量脚本的多键不能跨槽)。
- **移除访客 id 去重限速**:删除 `internal/im/wsapi/guestlimit.go` 及 `resolveIdentity` 里对它的调用;`GuestIPRate` 从 `model.AppConfig`、`httpapi/application.go`、`fpauth.go`、`web/src/components/ApplicationIMSettings.tsx` 以及对应测试中去掉;`appconfig.go` 里"开了访客必须配 `GuestIPRate`"的校验一并删除。
- 这样做的后果:fp 不再限制一个 IP 能造多少访客 id(原注释里"id 免费无限造,只能限制造它的人"这道防线退出)。连接后注册不注册、造多少 id,归 kkdesk 管。
- 默认值要宽松(1m/60,1h/600):旧规则允许同一访客反复重连不占额度,新规则每次连接都计,公司或网吧共用出口、断网后集体重连时容易误伤。

## 7. 实现

### 7.1 组件

- 新增限频包(建议 `internal/ratelimit`):规则注册表、`Guard`(`Hit` / `Check` / `Fail` / `Reset`,批量入口)、批量 Lua 脚本、IP 归一函数、文案渲染。底座沿用 `store.RateLimiter` 的键前缀 `fp:rl:`。
- **注册表**每条规则声明:键、中文说明、分组、维度、计法、是否成功后清零、默认档位、文案模板。页面和判定都读它。
- **批量脚本**输入(键、窗口毫秒、次数、计法)的列表;先逐项检查,全部通过再逐项 `INCR` 并在首次时设 `PEXPIRE`;返回超限的序号和最长剩余毫秒。失败类的 `Check` 用只读调用。
- **成功路径不碰 Redis**:没有声明清零的规则,成功后不调用 `Reset`。
- 并发窗口:失败类的"查"和"+1"之间没有原子性,并发尝试可能略超上限,上限受并发数约束,可接受。

### 7.2 配置与页面

- **来源**:应用配置中心的 `rate_limits` 分区(kkdesk 已在读的同一份),格式不变:规则名 → `[{window, limit}]`。fp 自己的规则按请求的 Host 识别应用后读该应用的配置;发码收件人规则在未配时用配置文件 `verify_code.rate_limit` 的值作默认。具体读写走现有配置中心接口,实施时核对。
- **热更新**:沿用配置变更广播(`fp:config`),fp 各实例刷新本地规则缓存。
- **页面**:在应用详情里新增"限频规则"页(`web/src/pages/ApplicationDetail.tsx` 同级),是 `rate_limits` 分区的结构化表格视图。行是规则,同一动作的不同维度归一组;只有档 1 到档 3 可编辑,维度、计法、成功后清零、文案只读。保存时校验:窗口 1 秒到 366 天、窗口递增、次数递增、档 1 非空、最多 3 档。
- 页面同时展示 kkdesk 侧和被控端本地规则的行(首版由 fp 种子数据预置),它们由 kkdesk 读取执行;执行端那一列标明。
- 配置中心的原始编辑页和本页改的是同一份数据,以现有版本号做乐观锁,冲突时后者提示刷新。

### 7.3 响应

- `accountapi`:新格式(带 `X-Resp-Format: 2`)用 `resp.Fail(w, "rate_limited", msg, {"retry_after_ms": ...}, 429)`,按规范落成 HTTP 400;老格式保持 429、`rate_limited`,等待毫秒数放进 `detail`。`msg` 为空时才用 `errTable` 里的默认"操作过于频繁,请稍后再试",限频要显式传入渲染后的文案。
- gRPC:返回 `domain.ErrRateLimited`(`CodeRateLimited`),`msg` 为渲染后的文案,`detail` 带 `retryAfterMs`(与现有发码限频一致)。SDK 侧映射成 `fpsdk.ErrRateLimited`,kkdesk 控制台已直接把 `fe.Msg` 展示给用户。
- Redis 故障:HTTP 返回 `unavailable`,gRPC 返回 `ErrUnavailable`。

## 8. 提示文案

变量、取整和渲染规则见 kkdesk 文档 §3.11。IP 类不写具体次数。

| 规则 | 文案 |
|---|---|
| 密码失败(登录标识) | 连续登录失败超过 {limit} 次,请 {retry} 后重试 |
| 验证失败(密码、短信验证码、邮箱验证码)(IP) | 验证失败次数过多,请稍后再试 |
| 短信发码、邮件发码(收件人、IP) | 发送过于频繁,请 {retry} 后重试 |
| 短信换绑、邮箱换绑、注销申请 | {window}内最多申请 {limit} 次,请稍后再试 |
| 微信登录、微信绑定、订阅公众号 | 操作过于频繁,请稍后再试 |
| IM 连接 | 连接过于频繁,请稍后再试 |

文案模板写在注册表里,不进页面配置;错误文案里不出现 fp、不带产品名。

## 9. 迁移

| 现有 | 处理 |
|---|---|
| `LoginCodeService.AllowSend`、`LoginCodeRateRule`、`WithLoginCodeRateRules`、`WithEmailCodeRateRules`、`cmd/fp/main.go` 里的 `codeRateRules` | 改为从注册表取;配置文件 `verify_code.rate_limit` 只作为 `sms_send.target`、`email_send.target` 的默认值 |
| `store.RateLimiter.Allow` | 保留给批量脚本使用的键前缀和窗口语义;逐条调用的入口不再使用 |
| `GuestLimiter`、`GuestIPRate` | 按 §6 删除 |
| `verify()` 把输错、过期、用尽折成一句 | 对外不变,对内新增 `code_attempts_exhausted` 日志 |

## 10. 测试

- 批量脚本:多规则多档原子性(任一超限则都不计)、窗口边界、剩余等待取最长、失败类 `Check`/`Fail`/`Reset`。
- 密码失败:不存在的账号也计数且响应一致;成功后只清账号计数、不清 IP 计数("80 次失败加 1 次成功"不能重置 IP);被挡的请求不增加失败计数;被挡时不校验密码。
- 验证失败:覆盖登录侧和账号管理侧各接口;验证码用尽记 `code_attempts_exhausted`;`bad_request`、滑块失败不计。
- 发码:收件人规则在滑块通过之后才计数;一次批量调用 1 次 Redis 往返;`purpose=bind` 额外计换绑。
- 微信:发起和 app 授权码换票各计一次,换票和轮询不计。
- IM:token 和访客都计数;访客去重相关代码和配置项已移除;关闭码与文案。
- IP:IPv4 /32、IPv6 /64、映射还原、缺失时跳过 IP 维度并 warn。
- 控制台、社区经 gRPC `Login` 同样触发密码失败和验证失败规则。
- Redis 故障和超时:拒绝并返回"服务暂不可用"。
- 页面:校验规则、保存后热更新、乐观锁冲突。

## 11. 不做与待定

- 不做:无效 token 探测、滑块出题、解绑微信、登录入口总量、密码登录和短信登录按 IP 的请求量、短信登录验码失败(手机号维度)、头像上传。理由见 kkdesk 文档 §1。
- 待定:如需观察再上线,可对单条规则开观察模式;fp 侧阈值不是估算性质,默认直接拦截。
- 已知缺口:社区经 gRPC 发码拿不到终端 IP,发码 IP 规则管不到它,直到社区接入通用登录。
