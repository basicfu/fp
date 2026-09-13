# 访问密钥（AccessKey）对接文档

第三方程序调用业务方接口时不登录账号，改用 fp 控制台发放的 AccessKey ID（AK）与 AccessKey Secret（SK）给请求签名。

## 1. 拿到密钥

业务方管理员在 fp 控制台「访问密钥」里新建：填备注，绑定角色，设置有效期与 IP 白名单。创建成功的弹窗里显示 AK 与 SK，**SK 只显示这一次**。

## 2. 每个请求带 4 个请求头

| 请求头 | 值 |
|---|---|
| `X-Fp-Access-Key` | AK |
| `X-Fp-Timestamp` | 当前 Unix 时间（秒），与服务器相差不超过 15 分钟 |
| `X-Fp-Nonce` | 每个请求都不同的随机串，只能包含字母、数字、`-`、`_`，最长 64 位，建议用 UUID |
| `X-Fp-Signature` | 签名，小写十六进制 |

## 3. 计算签名

把下面 6 项用换行符 `\n` 连起来（最后一项后面没有换行）：

1. HTTP 方法，如 `POST`
2. 路径：与请求行里发出去的**完全一致**，不解码、不重新编码
3. query：`?` 后面的部分，原样，不排序；没有 query 就是空串
4. 与 `X-Fp-Timestamp` 相同的时间戳
5. 与 `X-Fp-Nonce` 相同的 nonce
6. 请求体的 SHA-256（小写十六进制）；没有请求体时是 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`

签名 = `hex(HMAC-SHA256(SK, 拼出的字符串))`。HMAC 的密钥就是 SK 字符串本身的字节，**不做 base64 解码**。请求头不参与签名。

## 4. 测试向量

SK：`ZmFrZS1zZWNyZXQtZm9yLWRvY3MtZXhhbXBsZS0wMDE`，时间戳：`1789200000`。

**向量 1**：`POST /api/v1/orders?source=partner`，nonce `3f2b8c1e-7d4a-4e1b-9c55-2a6f0d8e9b17`，请求体 `{"sku":"A100","qty":2}`。

待签名串：

```
POST
/api/v1/orders
source=partner
1789200000
3f2b8c1e-7d4a-4e1b-9c55-2a6f0d8e9b17
eac3608933ecc3a8767f2d0966e808202214fa80644296ed3b7a0286dae3c383
```

签名：`a3d533065c49e9a9888c0f6e8c1b41c9d70316891411e001ec73ef12f464f3c4`

**向量 2**：`GET /api/v1/orders/a%2Fb`，没有 query、没有请求体，nonce `n-0001`。

待签名串（第 3 行是空行）：

```
GET
/api/v1/orders/a%2Fb

1789200000
n-0001
e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
```

签名：`46b32c81d96b8e44e56e152af7bb2e6a6485968fc9bac34df180b2fa299ba8f8`

## 5. Go 示例

`github.com/basicfu/fp/sdk/aksign` 只依赖标准库：

```go
req, _ := http.NewRequest(http.MethodPost, "https://biz.example.com/api/v1/orders?source=partner", body)
req.Header.Set("Content-Type", "application/json")
if err := aksign.Sign(req, accessKeyID, secret); err != nil {
	return err
}
resp, err := http.DefaultClient.Do(req)
```

## 6. 错误码

失败时响应体为 `{"code": "...", "msg": "..."}`。

| 状态码 | code | 含义与处理 |
|---|---|---|
| 401 | `SIGNATURE_INVALID` | 缺少签名请求头，或格式不对 |
| 401 | `TIMESTAMP_EXPIRED` | 时间戳与服务器相差超过 15 分钟，检查本机时间 |
| 401 | `ACCESS_KEY_INVALID` | AK 不存在 |
| 401 | `SIGNATURE_MISMATCH` | 签名不对。响应体的 `stringToSign` 是服务端算出的待签名串，逐行对照找差异 |
| 401 | `NONCE_USED` | nonce 已经用过；每个请求（包括重试）都要换新的 nonce |
| 403 | `ACCESS_KEY_DISABLED` / `ACCESS_KEY_EXPIRED` | key 已停用 / 已过期，联系业务方 |
| 403 | `IP_DENIED` | 来源 IP 不在白名单 |
| 403 | 无 | 这把 key 的角色没有这个接口的权限 |
| 413 | `BODY_TOO_LARGE` | 请求体超过上限（默认 10MB） |
| 503 | 无 | 业务方暂时无法校验，稍后重试 |

## 7. 业务方的部署要求

- 最外层代理必须用真实客户端地址**覆盖** `X-Forwarded-For`（nginx：`proxy_set_header X-Forwarded-For $remote_addr;`），**不能追加**。若使用追加式配置（如 nginx 的 `$proxy_add_x_forwarded_for`），客户端自己带的 XFF 会排在第一位，**IP 白名单将完全失效且没有任何报错**。SDK 取的是这个头的第一个地址。
- 中间网关不能改写路径与 query，否则签名必然失败。
- 所有路由都要挂鉴权：认证中间件会放行签名正确的访问密钥请求与匿名请求，能调什么由鉴权决定。
- 先发布 fp，再升级业务方的 SDK。

## 8. 已接受的限制

- nonce 只在业务方单个实例内去重，同一个请求重放到另一个实例能通过。
- SK 缓存在业务方 SDK 的进程内存里；fp 数据库里的 SK 目前是明文存储。
- 请求头不在签名范围内，业务逻辑不要依赖第三方传来的自定义请求头做安全判断。
