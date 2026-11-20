package fpsdk

import (
	"context"
	"time"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// 微信服务号通知可能返回的错误码（*Error 的 Code）。
const (
	// CodeNotifyRecipientUnreachable：服务号渠道的收件人没开启微信通知，或已取关服务号。按"不收微信通知"跳过即可。
	CodeNotifyRecipientUnreachable = "NOTIFY_RECIPIENT_UNREACHABLE"
	// CodeWechatMPUnavailable：fp 里没配（或停用了）服务号，微信通知整体不可用，业务方应隐藏入口。
	CodeWechatMPUnavailable = "WECHAT_MP_UNAVAILABLE"
)

// WechatSubscribeQR 是一张开启微信通知的服务号二维码。
type WechatSubscribeQR struct {
	// ImageURL 是微信托管的二维码图片，可以直接放进 <img>。
	ImageURL string
	// Content 是二维码里编码的内容，想自己画二维码时用。
	Content   string
	ExpiresIn time.Duration
}

// WechatSubscription 是用户的微信通知状态。
type WechatSubscription struct {
	// Bound 表示开启过微信通知。
	Bound bool
	// Subscribed 表示绑定的微信当前仍关注着服务号。Receivable() 为 true 才收得到通知。
	Subscribed bool
	// BoundAt 是开启时间，没开启时为零值。
	BoundAt time.Time
}

// Receivable 报告这个用户现在能不能收到服务号通知。
func (s *WechatSubscription) Receivable() bool { return s.Bound && s.Subscribed }

// WechatSubscribeQR 给 userID 生成开启微信通知的二维码：10 分钟有效，只给第一个扫码的微信用。
// 用户扫码后（没关注的先关注服务号），发给该用户的 wechat_mp 渠道通知就到这个微信；原来绑的微信被换下。
//
// 二维码就是这个账号的通知去向，只能展示给本人（已登录的客户端里），不要放进可分享的页面。
func (n *Notify) WechatSubscribeQR(ctx context.Context, userID string) (*WechatSubscribeQR, error) {
	res, err := n.c.notifyRPC.CreateWechatSubscribeQR(ctx, &fpv1.CreateWechatSubscribeQRRequest{UserId: userID})
	if err != nil {
		return nil, translate(err)
	}
	return &WechatSubscribeQR{
		ImageURL: res.GetImageUrl(), Content: res.GetContent(),
		ExpiresIn: time.Duration(res.GetExpiresInSec()) * time.Second,
	}, nil
}

// WechatSubscription 查用户的微信通知状态。展示二维码期间可以每 2 秒查一次，Receivable() 变 true 就关掉二维码。
func (n *Notify) WechatSubscription(ctx context.Context, userID string) (*WechatSubscription, error) {
	res, err := n.c.notifyRPC.GetWechatSubscription(ctx, &fpv1.GetWechatSubscriptionRequest{UserId: userID})
	if err != nil {
		return nil, translate(err)
	}
	out := &WechatSubscription{Bound: res.GetBound(), Subscribed: res.GetSubscribed()}
	if ms := res.GetBoundAtMs(); ms > 0 {
		out.BoundAt = time.UnixMilli(ms)
	}
	return out, nil
}

// DeleteWechatSubscription 关掉用户的微信通知。用户在微信里照样关注着服务号，服务端没法替他取关；
// 想再开就重新扫码。
func (n *Notify) DeleteWechatSubscription(ctx context.Context, userID string) error {
	_, err := n.c.notifyRPC.DeleteWechatSubscription(ctx, &fpv1.DeleteWechatSubscriptionRequest{UserId: userID})
	return translate(err)
}
