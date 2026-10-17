package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/connector"
)

const (
	// oauthStateTTL 覆盖「打开授权页 → 扫码 / 点快捷登录 → 回调 → 桌面端轮询取走」整段。
	oauthStateTTL = 5 * time.Minute
	// oauthTicketTTL 只需覆盖 302 回到业务页面、业务后端换票据这一跳。
	oauthTicketTTL = 2 * time.Minute
	// oauthBindTTL 要留够收短信、输验证码的时间。
	oauthBindTTL = 10 * time.Minute
)

// 交付方式：结果怎么从回调送回发起方。
const (
	oauthDeliverWeb    = "web"    // 302 回业务页面，带一次性 fpTicket
	oauthDeliverScheme = "scheme" // 结果页跳桌面 App 的自定义协议，带一次性 fpTicket
	oauthDeliverPoll   = "poll"   // 结果挂在 state 上，桌面 App 轮询取走
)

// 回调的结论。
const (
	oauthOK        = "ok"
	oauthCancelled = "cancelled"
	oauthFailed    = "failed"
)

type oauthState struct {
	AppID     uuid.UUID `json:"appId"`
	Connector string    `json:"connector"`
	Deliver   string    `json:"deliver"`
	ReturnURL string    `json:"returnUrl,omitempty"`
	Scheme    string    `json:"scheme,omitempty"`
}

type oauthOutcome struct {
	Status  string                  `json:"status"`
	Profile *connector.OAuthProfile `json:"profile,omitempty"`
}

// oauthTicket 是 fpTicket 与 bindTicket 共用的载荷：都要绑定应用与登录方式，防止跨应用拿去用。
type oauthTicket struct {
	AppID     uuid.UUID    `json:"appId"`
	Connector string       `json:"connector"`
	Outcome   oauthOutcome `json:"outcome"`
}

// OAuthStore 是三方扫码登录在 Redis 里的全部状态。所有读取都是一次性的。
type OAuthStore struct {
	rdb *redis.Client
}

// NewOAuthStore 构造 OAuthStore。
func NewOAuthStore(rdb *redis.Client) *OAuthStore {
	return &OAuthStore{rdb: rdb}
}

func oauthStateKey(state string) string   { return "fp:oauth:state:" + state }
func oauthDoneKey(state string) string    { return "fp:oauth:done:" + state }
func oauthResultKey(state string) string  { return "fp:oauth:result:" + state }
func oauthTicketKey(ticket string) string { return "fp:oauth:ticket:" + ticket }
func oauthBindKey(ticket string) string   { return "fp:oauth:bind:" + ticket }

func (o *OAuthStore) putState(ctx context.Context, state string, st oauthState) error {
	return o.setJSON(ctx, oauthStateKey(state), st, oauthStateTTL)
}

func (o *OAuthStore) getState(ctx context.Context, state string) (*oauthState, error) {
	var st oauthState
	ok, err := o.getJSON(ctx, o.rdb.Get(ctx, oauthStateKey(state)), &st)
	if !ok {
		return nil, err
	}
	return &st, nil
}

// claimCallback 保证同一个 state 的回调只处理一次（用户刷新回调页、微信重复回调）。
func (o *OAuthStore) claimCallback(ctx context.Context, state string) (bool, error) {
	ok, err := o.rdb.SetNX(ctx, oauthDoneKey(state), 1, oauthStateTTL).Result()
	if err != nil {
		return false, fmt.Errorf("service: 占用 oauth 回调: %w", err)
	}
	return ok, nil
}

func (o *OAuthStore) putResult(ctx context.Context, state string, out oauthOutcome) error {
	return o.setJSON(ctx, oauthResultKey(state), out, oauthStateTTL)
}

// takeResult 取走轮询结果；还没回调时返回 nil, nil。取走后 state 一并作废。
func (o *OAuthStore) takeResult(ctx context.Context, state string) (*oauthOutcome, error) {
	var out oauthOutcome
	ok, err := o.getJSON(ctx, o.rdb.GetDel(ctx, oauthResultKey(state)), &out)
	if !ok {
		return nil, err
	}
	o.rdb.Del(ctx, oauthStateKey(state))
	return &out, nil
}

func (o *OAuthStore) deleteState(ctx context.Context, state string) {
	o.rdb.Del(ctx, oauthStateKey(state))
}

func (o *OAuthStore) putTicket(ctx context.Context, t oauthTicket) (string, error) {
	return o.newTicket(ctx, oauthTicketKey, t, oauthTicketTTL)
}

func (o *OAuthStore) takeTicket(ctx context.Context, ticket string) (*oauthTicket, error) {
	return o.ticket(ctx, o.rdb.GetDel(ctx, oauthTicketKey(ticket)))
}

func (o *OAuthStore) putBind(ctx context.Context, t oauthTicket) (string, error) {
	return o.newTicket(ctx, oauthBindKey, t, oauthBindTTL)
}

// peekBind 只看不消费：验证码输错可以在同一张票据上重试。
func (o *OAuthStore) peekBind(ctx context.Context, ticket string) (*oauthTicket, error) {
	return o.ticket(ctx, o.rdb.Get(ctx, oauthBindKey(ticket)))
}

// takeBind 原子取走；并发的两次绑定只有一次能拿到。
func (o *OAuthStore) takeBind(ctx context.Context, ticket string) (*oauthTicket, error) {
	return o.ticket(ctx, o.rdb.GetDel(ctx, oauthBindKey(ticket)))
}

func (o *OAuthStore) newTicket(ctx context.Context, key func(string) string, t oauthTicket, ttl time.Duration) (string, error) {
	id, err := randomToken()
	if err != nil {
		return "", err
	}
	return id, o.setJSON(ctx, key(id), t, ttl)
}

func (o *OAuthStore) ticket(ctx context.Context, cmd *redis.StringCmd) (*oauthTicket, error) {
	var t oauthTicket
	ok, err := o.getJSON(ctx, cmd, &t)
	if !ok {
		return nil, err
	}
	return &t, nil
}

func (o *OAuthStore) setJSON(ctx context.Context, key string, v any, ttl time.Duration) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("service: 序列化 oauth 状态: %w", err)
	}
	if err := o.rdb.Set(ctx, key, raw, ttl).Err(); err != nil {
		return fmt.Errorf("service: 写入 oauth 状态: %w", err)
	}
	return nil
}

// getJSON 返回 false, nil 表示键不存在。
func (o *OAuthStore) getJSON(_ context.Context, cmd *redis.StringCmd, v any) (bool, error) {
	raw, err := cmd.Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("service: 读取 oauth 状态: %w", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return false, fmt.Errorf("service: 解析 oauth 状态: %w", err)
	}
	return true, nil
}
