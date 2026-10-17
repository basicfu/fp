# 用户资料与头像接入文档

业务服务端用 SDK 的 `client.Users()` 查询、修改用户的个人信息与头像。调用方是用 `AppID` / `AppSecret` 认证的应用，与 `Login`、`Notify().Send` 同一信任模型；fp-im 网关的凭据会被拒绝。

设计背景见 `docs/superpowers/specs/2026-10-12-fp-user-profile-avatar-design.md`。

## 1. 三条规则

**用户全局互通，不与应用关联。** 昵称、性别、头像都属于用户本身。任何应用都可以按 `user_id` 查询、修改任何用户——哪怕他从没在这个应用登录过；一个应用改了，所有应用随之变化。接入同一个 fp 的应用彼此是互信的自家应用，fp 不做"这个用户归不归你管"的判断，这道门由业务方自己把：**只让登录用户改自己的资料**。

**fp 对图片只存不处理。** 不解码、不裁剪、不压缩、不转格式、不去 EXIF（照片里的拍摄位置等元数据会原样保留）。头像多大、什么尺寸、要不要压缩由业务方决定：客户端在上传前压到业务规定的规格，业务服务端校验大小。fp 只做两件事：

- 兜底上限 1 MB（`fpsdk.AvatarMaxBytes`）。它只是防止接入方漏了校验，正常头像只有几 KB 到几十 KB，业务方应当设一个严得多的限制。
- 按文件头识别类型，只接受 JPEG、PNG、WebP。不看文件名、不信调用方声明的类型。

**哈希即版本。** `avatar_hash` 是图片原始字节 SHA-256 前 16 字节的十六进制（32 个小写字符）。换头像即换哈希，所以按哈希取到的内容永不变化，可以无限期缓存；客户端也据此判断要不要重新下载。

头像有两个来源，业务方的规则只有一条：`avatar_hash` 非空用它，否则用 `avatar_url`（留给三方登录带来的外部地址，目前不会被写入），都空就显示默认头像。

## 2. 调用

```go
u := client.Users()

p, err := u.Get(ctx, userID)                                       // 查询个人信息
p, err  = u.Update(ctx, userID, fpsdk.UserUpdate{Nickname: &name}) // 改昵称 / 性别，nil 的字段不改
p, err  = u.UpdateAvatar(ctx, userID, imageBytes)                  // 更新头像；传 nil 清除
img, contentType, err := u.Avatar(ctx, hash)                       // 按哈希下载头像
```

`Update` 与 `UpdateAvatar` 都返回改完的完整资料，不必再查一次。登录返回的 `LoginResult.User.GetAvatarHash()` 也带着头像哈希。

`UserProfile` 的字段：

| 字段 | 说明 |
|---|---|
| `ID` | 用户 ID |
| `Nickname` | 昵称。修改时去首尾空白后须为 1–32 个字符，不含控制字符与换行 |
| `AvatarHash` / `AvatarURL` | 见上面的优先级规则 |
| `Gender` | `MALE`（`fpsdk.GenderMale`）/ `FEMALE`（`fpsdk.GenderFemale`），空串表示未设置。修改时 `UserUpdate.Gender` 指向空串即清空 |
| `Status` | `ACTIVE` / `FROZEN` / `PENDING_DELETE` / `DELETED` |
| `HasPassword` | 是否设过密码。密码哈希与第三方凭据永不返回 |
| `Identities` | 全部登录标识：`Type`（`phone` / `username` / `email` / `wechat_mp` / `wechat_web`）、`Subject`、`LastLoginAt`（从未用它登录过时是零值） |
| `CreatedAt` / `UpdatedAt` | 注册时间、资料最后修改时间 |
| `RegisteredAppID` | 注册来源应用的 `app_id`，见第 5 节 |

**手机号不脱敏。** `Identities[].Subject` 是完整值：调用方是受信任的业务服务端，要不要对终端用户脱敏、怎么脱，由业务方决定。别把 `UserProfile` 原样转给前端。

## 3. 错误码

按 [SDK 的错误处理](../sdk/README.md#错误处理) 取 `*fpsdk.Error`，用 `Code` 分支；`errors.Is` 判哨兵同样有效。

| 码 | 哨兵 | gRPC | 何时 |
|---|---|---|---|
| `USER_NOT_FOUND` | `ErrNotFound` | NotFound | 用户不存在（`userID` 不是 UUID 也算） |
| `AVATAR_NOT_FOUND` | `ErrNotFound` | NotFound | 没有这个哈希的头像——已被替换或清除 |
| `NICKNAME_INVALID` | `ErrInvalidArgument` | InvalidArgument | 昵称不合规 |
| `AVATAR_INVALID` | `ErrInvalidArgument` | InvalidArgument | 图片超过 1 MB，或不是 JPEG / PNG / WebP |
| `INVALID_ARGUMENT` | `ErrInvalidArgument` | InvalidArgument | 性别不在取值范围内 |
| `APP_DISABLED` | `ErrForbidden` | PermissionDenied | 调用方应用已停用 |
| （无） | `ErrForbidden` | PermissionDenied | 用了 fp-im 网关的凭据 |
| `APP_CREDENTIAL_INVALID` | `ErrUnauthorized` | Unauthenticated | `AppID` / `AppSecret` 不对 |
| （无） | `ErrUnavailable` | Unavailable 等 | fp 不可达或内部错误 |

这里的 `ErrNotFound` 与 `ErrForbidden` 是业务结论，**不会**像登录那一侧一样被归进 `ErrUnauthorized`——"查无此人"不该让客户端清会话。`fpsdk.WriteError` 把 `ErrNotFound` 回成 404。

超过 1 MB 的图片 SDK 在本地就拒绝（同样是 `AVATAR_INVALID`），不会发给 fp。

这四个方法不重试，也不设自己的超时，由调用方的 `ctx` 控制。

## 4. 业务方要写的两个 HTTP 入口

SDK 只做调用与错误映射，不带 HTTP 处理器；fp 也不直接向终端用户提供图片（fp 的 HTTP 只有管理控制台，gRPC 在内网）。业务方自己写上传与下载两个入口：

```go
// 下载：公开，按哈希缓存一年。
mux.HandleFunc("GET /avatar/{hash}", func(w http.ResponseWriter, r *http.Request) {
    img, contentType, err := client.Users().Avatar(r.Context(), r.PathValue("hash"))
    if err != nil {
        fpsdk.WriteError(w, err) // 没有这个哈希 → 404
        return
    }
    w.Header().Set("Content-Type", contentType)
    w.Header().Set("X-Content-Type-Options", "nosniff")
    w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
    _, _ = w.Write(img)
})

// 上传：必须登录，只能改自己的头像。
mux.Handle("PUT /api/me/avatar", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    id, _ := fpsdk.IdentityFrom(r.Context())
    if id.UserID == "" {
        fpsdk.WriteError(w, fpsdk.ErrNoToken)
        return
    }
    image, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10)) // 业务自己的上限
    if err != nil {
        http.Error(w, "头像过大", http.StatusRequestEntityTooLarge)
        return
    }
    p, err := client.Users().UpdateAvatar(r.Context(), id.UserID, image)
    if err != nil {
        fpsdk.WriteError(w, err)
        return
    }
    _ = json.NewEncoder(w).Encode(map[string]string{"avatarHash": p.AvatarHash})
})))
```

接入约定（以桌面端为例）：

1. **登录成功**：缓存 `user_id`、昵称、`avatar_hash`。
2. **刷新资料**：每次校验登录态时顺带 `Users().Get`，把最新昵称与 `avatar_hash` 带回客户端；客户端发现 `avatar_hash` 变了才重新下载。
3. **下载**：客户端以哈希为文件名缓存到本地，离线可用。
4. **上传**：客户端先把图片压到业务规定的规格再上传；业务服务端校验登录态和图片大小，通过后调 `Users().UpdateAvatar`。

防止用户刷上传接口是业务方入口的事，fp 不对更新头像限流。

## 5. 注册来源

`RegisteredAppID` 标出这个用户最初是从哪个应用注册的，**仅供展示与统计，不参与任何权限判断**：

- 登录流程自动建号时（例如短信验证码首次登录），当时那个应用被记为来源，之后登录别的应用不再变。
- 管理员在控制台手动建的号没有来源，为空。
- 来源应用被删除后变为空，用户本身不受影响。
- 迁移 `00018` 之前就存在的用户，按最早登录过的应用回填——回填无法区分"管理员手动建号后首次登录"与"登录时自动建号"，存量数据一律按最早登录的应用算。

## 6. 控制台

用户详情页（`/users/:id`）显示头像与注册来源；头像旁的「清除头像」用于处理违规图片，清除后所有应用里都变回默认头像，图片不保留。用户列表不显示头像。

自动内容审核不在 fp 的范围内，属于业务侧或三方服务。

## 7. 已知限制

- 图片存在数据库里（`user_avatar` 表，一个用户至多一行）。头像是小图，数据库够用；对外只暴露哈希，日后换成对象存储不影响业务方。
- 只存业务方给的那一份，没有多种尺寸。
- 不抓取三方头像（微信等）转存，`avatar_url` 列留给三方登录接入时使用。
- 两个用户传了同一张图时各存一行，不做去重。
