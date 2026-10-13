# fp 用户资料查询与头像设计

**日期**：2026-10-12
**状态**：待评审
**上游**：`2026-08-24-fp-foundation-platform-design.md`（用户与标识模型）
**起因**：godesk 桌面端接入登录后，重启即拿不到昵称与头像——资料只在 `Login` 那一刻返回，之后没有任何接口能查

---

## 一、要做什么

给业务方四个接口，全部走 gRPC + SDK，调用方是已认证的业务服务端，与 `Login` / `Notify().Send` 同一信任模型：

| 接口 | 作用 |
|---|---|
| 查询个人信息 | 按 `user_id` 返回该用户的全部个人信息，含登录标识（手机号等） |
| 更新个人信息 | 改昵称、性别 |
| 更新头像 | 存一张图片，返回它的内容哈希 |
| 下载头像 | 按内容哈希取回图片 |

**fp 对图片只存不处理**：不解码、不裁剪、不压缩、不转格式。图片多大、什么尺寸、要不要压缩，由业务方决定——业务服务端校验大小，压缩放在业务方的客户端上传前做。

## 二、明确不做

| 项 | 一句话理由 |
|---|---|
| 图片处理（裁剪、缩放、压缩、去 EXIF） | 各业务的头像规格不同，由业务方在上传前处理；fp 保持只存取 |
| 对象存储（OSS） | 头像是几 KB 到几十 KB 的小图，数据库够用；对外只暴露哈希，日后换存储不影响业务方 |
| fp 直接向终端用户提供图片 | fp 的 HTTP 只有管理控制台，gRPC 在内网；图片由业务方转发 |
| 多种尺寸 | 只存业务方给的那一份 |
| 更新头像的限流 | fp 不再有解码缩放的 CPU 开销；防止用户刷接口是业务方入口的事 |
| 内容审核 | 管理员可在控制台清除头像；自动审核属于业务侧或三方服务 |
| 抓取三方头像（微信等）转存 | 等三方登录接入时再定；`avatar_url` 列留给它 |

## 三、核心模型

**用户全局互通，不与应用关联**：用户属于整个平台而不是某个应用。昵称、性别、头像都落在 `app_user` 上，任何一个应用都可以按 `user_id` 查询、修改任何用户，改了之后所有应用随之变化。接入同一个 fp 的应用彼此是互信的自家应用。

**注册来源只是一条记录**：`user_extra`（用户在各应用的首次登录记录）上加一个标记，标出这个用户最初是从哪个应用注册的，仅供展示与统计，不参与任何权限判断。用户表本身不出现任何应用字段。

**头像两种来源，一个优先级**：

- `avatar_hash`：经"更新头像"存在 fp 里的图片的内容哈希。非空即以它为准。
- `avatar_url`：已有的列，保留给三方登录带来的外部地址。本设计不写它。

业务方的规则只有一条：`avatar_hash` 非空用它拼自己的图片地址，否则用 `avatar_url`，都空就显示默认头像。

**哈希即版本**：`avatar_hash` 是图片原始字节的 SHA-256 前 16 字节的十六进制（32 个字符）。换头像即换哈希，所以按哈希取到的内容永不变化，可以无限期缓存；客户端也据此判断要不要重新下载。

## 四、数据模型

迁移 `00018_user_avatar.sql`：

```sql
ALTER TABLE app_user ADD COLUMN avatar_hash text NOT NULL DEFAULT '';

-- 注册来源：登录流程自动建号时，把当时那个应用的行标为 true；管理员在控制台手动建的号没有任何一行为 true。
ALTER TABLE user_extra ADD COLUMN is_registration boolean NOT NULL DEFAULT false;
-- 一个用户至多一个注册来源。
CREATE UNIQUE INDEX user_extra_registration_idx ON user_extra (user_id) WHERE is_registration;
-- 存量用户按最早登录过的应用回填。
UPDATE user_extra e SET is_registration = true
WHERE e.first_login_at = (SELECT min(first_login_at) FROM user_extra WHERE user_id = e.user_id);

-- 一个用户至多一张头像，随用户删除。
-- 不做按哈希去重的独立内容表：那要引用计数才能清理孤儿，省下的只是偶尔撞图的几 KB。
CREATE TABLE user_avatar (
    user_id      uuid PRIMARY KEY REFERENCES app_user(id) ON DELETE CASCADE,
    hash         text NOT NULL,
    content_type text NOT NULL,
    data         bytea NOT NULL,
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX user_avatar_hash_idx ON user_avatar (hash);
```

- 图片单独成表，不放进 `app_user`：`userColumns` 被登录、校验、列表各处查询共用，不能让它们顺带读出图片。
- `app_user.avatar_hash` 与 `user_avatar` 在同一事务里一起写，前者是为了查资料时不必连表。
- `is_registration` 只在建号那一刻写一次：登录流程里 `EnsureUserWithIdentity` 报告"本次新建了用户"时，紧接着的 `EnsureRegistration` 把这一行标为 true；之后登录别的应用只插入普通行，不再变。
- 应用被删除时它的 `user_extra` 行随之删除，这批用户的注册来源即变为空，用户本身不受影响。
- 回填无法区分"管理员手动建号后首次登录"与"登录时自动建号"，存量数据一律按最早登录的应用算；新数据按上一条规则准确记录。
- 按哈希取图：`SELECT content_type, data FROM user_avatar WHERE hash = $1 LIMIT 1`。两个用户传了同一张图时各存一行，取哪行都一样。

## 五、fp 对图片做的仅有的两件事

1. **兜底上限 1 MB**。业务方有自己更严的限制（头像正常只有几 KB 到几十 KB），这条只是防止接入方漏了校验把大文件灌进数据库；同时让请求留在 gRPC 默认 4 MB 消息上限之内。超出返回 `AVATAR_INVALID`。
2. **按文件头识别类型**，只接受 JPEG、PNG、WebP，把识别出的 `content_type` 存下来。不信调用方声明，也不解码图片内容。识别不出返回 `AVATAR_INVALID`。存类型是为了下载时能回正确的 `Content-Type`，也挡掉把任意文件当头像存的用法（下载地址是公开的）。

EXIF 等元数据（可能含拍摄位置）原样保留。业务方客户端在上传前压缩重编码时会自然去掉；直接上传原图的业务方需自行处理。

## 六、对外接口

### gRPC：新增 `UserService`

认证与 `AuthService` 相同（`fp-app-id` + `fp-app-secret`）；fp-im 网关凭据（`CallerTypeIM`）一律 `PermissionDenied`。

```proto
service UserService {
  rpc GetUser(GetUserRequest) returns (UserProfile);
  rpc UpdateUser(UpdateUserRequest) returns (UserProfile);
  rpc UpdateAvatar(UpdateAvatarRequest) returns (UserProfile);
  rpc GetAvatar(GetAvatarRequest) returns (GetAvatarResponse);
}

message UserProfile {
  string id = 1;
  string nickname = 2;
  string avatar_hash = 3;
  string avatar_url = 4;
  string gender = 5;
  string status = 6;
  bool has_password = 7;
  repeated UserIdentity identities = 8;
  int64 created_at = 9;      // unix 毫秒
  int64 updated_at = 10;     // unix 毫秒
  string registered_app_id = 11;  // 注册来源应用的对外 app_id；管理员手动建的号或应用已删除时为空
}

message UserIdentity {
  string type = 1;           // phone / username / email / wechat_mp
  string subject = 2;        // 完整值，不脱敏
  int64 last_login_at = 3;   // unix 毫秒，0 表示从未用它登录
}

message GetUserRequest      { string user_id = 1; }
message UpdateUserRequest   { string user_id = 1; optional string nickname = 2; optional string gender = 3; }
message UpdateAvatarRequest { string user_id = 1; bytes image = 2; }
message GetAvatarRequest    { string hash = 1; }
message GetAvatarResponse   { bytes image = 1; string content_type = 2; }
```

- **手机号不脱敏**：调用方是受信任的业务服务端，要不要对终端用户脱敏、怎么脱，是业务方的事。
- **密码哈希、第三方凭据（`identity.credential`）永不返回**，只给 `has_password`。
- **昵称**：去首尾空白后 1–32 个字符，不含控制字符与换行；不合规返回 `NICKNAME_INVALID`。
- **性别**：取值沿用 `app_user.gender` 现有约定，空串表示未设置；不在取值范围内返回 `INVALID_ARGUMENT`。
- `UpdateUser` 没带的字段不改；`UpdateUser` 与 `UpdateAvatar` 都返回改完的完整资料，业务方不必再查一次。
- `UpdateAvatar` 的 `image` 为空表示清除头像（`avatar_hash` 置空、删掉 `user_avatar` 行）。
- `UserInfo`（`Login` 的返回）追加 `string avatar_hash = 6;`，登录当下就能拿到头像哈希。

### 错误码

| 码 | 哨兵 | 何时 |
|---|---|---|
| `USER_NOT_FOUND`（已有） | NotFound | 用户不存在 |
| `NICKNAME_INVALID`（新增） | InvalidArgument | 昵称不合规 |
| `AVATAR_INVALID`（新增） | InvalidArgument | 图片超过 1 MB，或不是 JPEG / PNG / WebP |
| `AVATAR_NOT_FOUND`（新增） | NotFound | 没有这个哈希的头像（已被替换或清除） |

### SDK

```go
u := client.Users()

p, err := u.Get(ctx, userID)                                    // 查询个人信息 → *fpsdk.UserProfile
p, err  = u.Update(ctx, userID, fpsdk.UserUpdate{Nickname: &name}) // 更新个人信息
p, err  = u.UpdateAvatar(ctx, userID, imageBytes)               // 更新头像；传 nil 清除
img, contentType, err := u.Avatar(ctx, hash)                    // 下载头像
```

SDK 只做调用与错误映射，不带 HTTP 处理器。业务方自己写上传与下载两个 HTTP 入口。

`fpsdk.LoginResult.User` 是生成的 `*fpv1.UserInfo`，新字段 `AvatarHash` 随重新生成自动出现。

## 七、控制台

- 用户详情页显示头像（有 `avatar_hash` 时），旁边一个「清除头像」按钮，用于处理违规图片。
- 管理端接口：`GET /admin/api/avatars/{hash}` 回图片；`DELETE /admin/api/users/{id}/avatar` 清除。
- 用户详情页显示注册来源应用。
- 用户列表不显示头像，避免一页发几十个图片请求。

## 八、业务方接入约定（以 godesk 为例）

1. **登录成功**：缓存 `user_id`、昵称、`avatar_hash`。
2. **刷新资料**：每次校验登录态时顺带 `Users().Get`，把最新昵称与 `avatar_hash` 带回客户端；客户端发现 `avatar_hash` 变了才重新下载。
3. **下载**：业务服务端开一个公开的 `GET /avatar/{hash}`，调 `Users().Avatar` 后回图片，带 `Cache-Control: public, max-age=31536000, immutable`。客户端以哈希为文件名缓存到本地，离线可用。
4. **上传**：客户端先把图片压到业务规定的规格再上传；业务服务端校验登录态和图片大小，通过后调 `Users().UpdateAvatar`。

## 九、测试

- **类型识别**（纯函数，表驱动）：JPEG / PNG / WebP 的文件头被认出；GIF、SVG、空文件、随机字节、伪装扩展名被拒；1 MB 整与 1 MB + 1 字节的边界。
- **service**（真实 Postgres）：更新、替换、清除后 `app_user.avatar_hash` 与 `user_avatar` 一致；同一图片两次上传哈希相同；删用户级联删头像；按旧哈希取图在替换后 `AVATAR_NOT_FOUND`；昵称与性别的各类非法输入；`UpdateUser` 只带一个字段时另一个不变。
- **查询**：返回的标识齐全且不含凭据；`has_password` 正确。
- **互通**：应用 A 能查、改从未在 A 登录过的用户；A 改昵称后 B 查到新值。
- **注册来源**：经应用 A 短信登录自动建的号 `registered_app_id` 是 A，之后再登录应用 B 不变；控制台手动建的号为空；删除应用 A 后该字段变空、用户仍在；并发首次登录两个应用时只有一行被标记。
- **gRPC**：`CallerTypeIM` 调四个 RPC 全部 `PermissionDenied`；停用的应用被拒。
- **SDK**：错误码到哨兵的映射；`UpdateAvatar(nil)` 清除。
- **迁移**：`00018` 的 Up / Down 往返；存量用户按最早登录的应用回填。
