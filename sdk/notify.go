package fpsdk

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

const (
	// notifyAttempts 是一次 Send 最多发起的 RPC 数：首次 + 两次重试。
	notifyAttempts = 3
	// notifyAttemptTimeout 是单次 RPC 的截止时间。没有它，一个挂死的连接会让重试永远轮不到。
	notifyAttemptTimeout = 30 * time.Second
	// notifyInProgressCode 是服务端"相同幂等键的通知正在发送中"的错误码。
	notifyInProgressCode = "NOTIFY_IN_PROGRESS"
)

// notifyBackoff 是第一次重试前的等待，之后每次翻倍。是变量而不是常量，只为让测试能调短它。
var notifyBackoff = 200 * time.Millisecond

// Notify 是发送通知的入口。
type Notify struct{ c *Client }

// Notify 返回通知入口。
func (c *Client) Notify() *Notify { return c.notify }

// SendOption 调整一次 Send。
type SendOption func(*sendOptions)

type sendOptions struct{ idempotencyKey string }

// WithIdempotencyKey 指定幂等键，覆盖 SDK 默认自动生成的那个。
// 用业务事件的 ID 当键，业务方自己的重试（重跑任务、重放消息）也不会重复发送。
// 键只需在本应用内唯一，最长 128 字节。
func WithIdempotencyKey(key string) SendOption {
	return func(o *sendOptions) { o.idempotencyKey = key }
}

// Send 按通知模板 code 发送一条通知。
//
// to 是收件人：短信是手机号，邮件是邮箱；telegram / 企业微信 / 钉钉 / webhook 传空串。
// params 的键集合必须与模板声明的变量完全一致。
//
// 默认每次调用自动生成一个幂等键，并在内部重试时复用它：fp 不可达、超时，以及同键的上一次请求
// 还在处理（NOTIFY_IN_PROGRESS）时，最多重试两次。服务端凭这个键避免因 SDK 重试而重复发送；
// 供应商超时但其实已经送达这类情况仍可能重复，详见 docs/notify.md。
func (n *Notify) Send(ctx context.Context, code, to string, params map[string]string, opts ...SendOption) error {
	var o sendOptions
	for _, fn := range opts {
		fn(&o)
	}
	if o.idempotencyKey == "" {
		key, err := newIdempotencyKey()
		if err != nil {
			return err
		}
		o.idempotencyKey = key
	}
	req := &fpv1.SendRequest{Code: code, To: to, Params: params, IdempotencyKey: o.idempotencyKey}

	backoff := notifyBackoff
	var err error
	for attempt := 1; attempt <= notifyAttempts; attempt++ {
		err = n.sendOnce(ctx, req)
		if err == nil || attempt == notifyAttempts || !retryableNotify(err) || ctx.Err() != nil {
			break
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return translateNotify(err)
		}
		backoff *= 2
	}
	return translateNotify(err)
}

func (n *Notify) sendOnce(ctx context.Context, req *fpv1.SendRequest) error {
	ctx, cancel := context.WithTimeout(ctx, notifyAttemptTimeout)
	defer cancel()
	_, err := n.c.notifyRPC.Send(ctx, req)
	return err
}

// retryableNotify 报告这次失败值不值得用同一个幂等键再试一次：只有"没到达或没说完"的情况。
// 业务错误（模板不存在、变量不匹配……）重试多少次结果都一样。
func retryableNotify(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return true
	}
	// 上一次请求还在处理：稍后重试会拿到"已完成"，或者在它失败后真正重发。
	if fe := errorFrom(err, nil); fe != nil && fe.Code == notifyInProgressCode {
		return true
	}
	return false
}

// translateNotify 与 translate 不同：NotFound / PermissionDenied 在这里是"模板不存在 / 已停用"
// 这类业务错误，不能归进 ErrUnauthorized——那会让调用方以为是凭据问题。
func translateNotify(err error) error {
	if err == nil {
		return nil
	}
	var sentinel error
	switch status.Code(err) {
	case codes.Unauthenticated:
		sentinel = ErrUnauthorized
	case codes.Unavailable, codes.DeadlineExceeded:
		sentinel = ErrUnavailable
	case codes.InvalidArgument:
		sentinel = ErrInvalidArgument
	}
	if fe := errorFrom(err, sentinel); fe != nil {
		return fe
	}
	if sentinel == nil {
		return err
	}
	return errors.Join(sentinel, err)
}

func newIdempotencyKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("fpsdk: 生成幂等键: %w", err)
	}
	return hex.EncodeToString(b), nil
}
