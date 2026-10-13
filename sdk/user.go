package fpsdk

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// Users 方法可能返回的错误码（*Error 的 Code）。
const (
	// CodeUserNotFound：没有这个用户，userID 写错（不是 UUID）也算。哨兵 ErrNotFound。
	CodeUserNotFound = "USER_NOT_FOUND"
	// CodeNicknameInvalid：昵称去首尾空白后不是 1–32 个字符，或含控制字符、换行。哨兵 ErrInvalidArgument。
	CodeNicknameInvalid = "NICKNAME_INVALID"
	// CodeAvatarInvalid：图片超过 AvatarMaxBytes，或不是 JPEG / PNG / WebP。哨兵 ErrInvalidArgument。
	CodeAvatarInvalid = "AVATAR_INVALID"
	// CodeAvatarNotFound：没有这个哈希的头像——已被替换或清除。哨兵 ErrNotFound。
	CodeAvatarNotFound = "AVATAR_NOT_FOUND"
)

// 性别取值。空串表示未设置。
const (
	GenderMale   = "MALE"
	GenderFemale = "FEMALE"
)

// AvatarMaxBytes 是 fp 接受的头像大小上限（1 MB）。它只是兜底，
// 业务方应当按自己的头像规格设一个严得多的限制，并在客户端上传前压缩。
const AvatarMaxBytes = 1 << 20

// ErrNotFound 要找的东西不存在：用户不存在，或没有这个哈希的头像。
// 用 *Error 的 Code 区分是哪一种。
var ErrNotFound = errors.New("fpsdk: 不存在")

// Users 是用户资料与头像的入口。
//
// 用户全局互通、不与应用关联：任何应用都可以按 userID 查询、修改任何用户，
// 改了之后所有接入同一个 fp 的应用随之变化。
type Users struct{ c *Client }

// Users 返回用户资料入口。
func (c *Client) Users() *Users { return c.users }

// UserProfile 是一个用户的全部个人信息。
type UserProfile struct {
	ID       string
	Nickname string
	// AvatarHash 是存在 fp 里的头像的内容哈希（32 位十六进制），没有头像时为空。
	// 换头像即换哈希，所以按哈希取到的内容永不变化，可以无限期缓存。
	//
	// 规则只有一条：AvatarHash 非空用它（经 Avatar 取图），否则用 AvatarURL，都空就显示默认头像。
	AvatarHash string
	// AvatarURL 是三方登录带来的外部头像地址。
	AvatarURL string
	// Gender 取 GenderMale / GenderFemale，空串表示未设置。
	Gender string
	// Status 是账号状态：ACTIVE / FROZEN / PENDING_DELETE / DELETED。
	Status      string
	HasPassword bool
	// Identities 是全部登录标识。Subject 是完整值（手机号不脱敏）——
	// 要不要对终端用户脱敏、怎么脱，由业务方决定。
	Identities []UserIdentity
	CreatedAt  time.Time
	UpdatedAt  time.Time
	// RegisteredAppID 是注册来源应用的 app_id，仅供展示与统计。
	// 管理员手动建的号、或来源应用已被删除时为空。
	RegisteredAppID string
}

// UserIdentity 是用户的一个登录标识。
type UserIdentity struct {
	// Type 取 phone / username / email / wechat_mp。
	Type    string
	Subject string
	// LastLoginAt 是最后一次用它登录的时间，从未用过时是零值（IsZero）。
	LastLoginAt time.Time
}

// UserUpdate 是一次资料修改。nil 的字段不改。
type UserUpdate struct {
	// Nickname 去首尾空白后须为 1–32 个字符，不含控制字符与换行。
	Nickname *string
	// Gender 取 GenderMale / GenderFemale，指向空串表示清空。
	Gender *string
}

// Get 按 userID 查询个人信息。用户不存在时返回 ErrNotFound（Code 为 CodeUserNotFound）。
func (u *Users) Get(ctx context.Context, userID string) (*UserProfile, error) {
	p, err := u.c.userRPC.GetUser(ctx, &fpv1.GetUserRequest{UserId: userID})
	if err != nil {
		return nil, translateUser(err)
	}
	return userProfileFrom(p), nil
}

// Update 修改昵称、性别，返回改完的完整资料。
func (u *Users) Update(ctx context.Context, userID string, in UserUpdate) (*UserProfile, error) {
	p, err := u.c.userRPC.UpdateUser(ctx, &fpv1.UpdateUserRequest{UserId: userID, Nickname: in.Nickname, Gender: in.Gender})
	if err != nil {
		return nil, translateUser(err)
	}
	return userProfileFrom(p), nil
}

// UpdateAvatar 把 image 存为该用户的头像，返回改完的完整资料，其中 AvatarHash 即新头像的哈希。
// image 为 nil 或空表示清除头像。
//
// fp 对图片只存不处理：不解码、不裁剪、不压缩。只接受 JPEG / PNG / WebP（按文件头识别，
// 不看文件名与声明的类型），上限 AvatarMaxBytes，否则 ErrInvalidArgument（Code 为 CodeAvatarInvalid）。
func (u *Users) UpdateAvatar(ctx context.Context, userID string, image []byte) (*UserProfile, error) {
	// 超限的图不发出去：超过 gRPC 消息上限的请求会被传输层直接拒掉，拿不到 fp 的错误码，
	// 调用方就得为"太大"这同一件事处理两种形状的错误。
	if len(image) > AvatarMaxBytes {
		return nil, &Error{Code: CodeAvatarInvalid, Msg: "头像不能超过 1 MB", sentinel: ErrInvalidArgument}
	}
	p, err := u.c.userRPC.UpdateAvatar(ctx, &fpv1.UpdateAvatarRequest{UserId: userID, Image: image})
	if err != nil {
		return nil, translateUser(err)
	}
	return userProfileFrom(p), nil
}

// Avatar 按内容哈希取回头像的原始字节与 Content-Type（image/jpeg、image/png 或 image/webp）。
//
// 没有这个哈希时返回 ErrNotFound（Code 为 CodeAvatarNotFound）——头像被替换或清除后，旧哈希就取不到了。
func (u *Users) Avatar(ctx context.Context, hash string) (image []byte, contentType string, err error) {
	res, err := u.c.userRPC.GetAvatar(ctx, &fpv1.GetAvatarRequest{Hash: hash})
	if err != nil {
		return nil, "", translateUser(err)
	}
	return res.GetImage(), res.GetContentType(), nil
}

func userProfileFrom(p *fpv1.UserProfile) *UserProfile {
	out := &UserProfile{
		ID:              p.GetId(),
		Nickname:        p.GetNickname(),
		AvatarHash:      p.GetAvatarHash(),
		AvatarURL:       p.GetAvatarUrl(),
		Gender:          p.GetGender(),
		Status:          p.GetStatus(),
		HasPassword:     p.GetHasPassword(),
		Identities:      make([]UserIdentity, 0, len(p.GetIdentities())),
		CreatedAt:       timeFromMs(p.GetCreatedAt()),
		UpdatedAt:       timeFromMs(p.GetUpdatedAt()),
		RegisteredAppID: p.GetRegisteredAppId(),
	}
	for _, i := range p.GetIdentities() {
		out.Identities = append(out.Identities, UserIdentity{
			Type: i.GetType(), Subject: i.GetSubject(), LastLoginAt: timeFromMs(i.GetLastLoginAt()),
		})
	}
	return out
}

// timeFromMs 把 unix 毫秒转成时间。0 表示"没有"，转成零值而不是 1970 年。
func timeFromMs(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// translateUser 与 translate 不同：NotFound 在这里是"用户不存在 / 头像不存在"，
// PermissionDenied 是"应用已停用"，都不能归进 ErrUnauthorized——那会让业务方把
// "查无此人"当成凭据失效去清会话。
func translateUser(err error) error {
	var sentinel error
	switch status.Code(err) {
	case codes.NotFound:
		sentinel = ErrNotFound
	case codes.InvalidArgument:
		sentinel = ErrInvalidArgument
	case codes.Unauthenticated:
		sentinel = ErrUnauthorized
	case codes.PermissionDenied:
		sentinel = ErrForbidden
	case codes.Unavailable, codes.DeadlineExceeded, codes.Internal:
		sentinel = ErrUnavailable
	}
	if fe := errorFrom(err, sentinel); fe != nil {
		return fe
	}
	if sentinel == nil {
		return err
	}
	return errors.Join(sentinel, err)
}
