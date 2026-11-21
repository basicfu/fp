package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/notify"
	"github.com/basicfu/fp/internal/wechatmp"
)

const (
	// 带参二维码的场景值前缀。场景值最长 64 字节：前缀 6 + 随机串 43。
	wechatSubScenePrefix = "fpsub:"
	// 二维码有效期：够用户掏出手机扫，又不至于截图流传太久——谁扫了这张码，账号的通知就发给谁。
	wechatSubQRTTL = 10 * time.Minute
	// 直接关注时要查一次 unionid；微信给推送的回复留了 5 秒，查询不能把它吃光。
	wechatUserInfoTimeout = 3 * time.Second
)

// WechatMPService 管理服务号订阅：给 fp 用户生成开启通知的带参二维码、处理服务号的事件推送、
// 维护「通知发给哪个 openid」的绑定，并给通知中心换收件人。
//
// 服务号凭据是通知中心里唯一的 wechat_mp 供应商实例，这里只读不写。
type WechatMPService struct {
	pool    *pgxpool.Pool
	rdb     *redis.Client
	apiBase string
	// pub 推送 WechatSubscriptionChanged。为 nil 时不推送。
	pub EventPublisher

	mu    sync.Mutex
	cache map[string]*notify.WechatMPAccount // key 是 id@updated_at，改配置自然换新
}

// WechatMPOption 调整 WechatMPService。
type WechatMPOption func(*WechatMPService)

// WithWechatMPAPIBase 替换 api.weixin.qq.com，测试指向 httptest。
func WithWechatMPAPIBase(base string) WechatMPOption {
	return func(s *WechatMPService) { s.apiBase = base }
}

// WithWechatMPPublisher 让微信通知状态的变化经 Watch 流推给业务方。
func WithWechatMPPublisher(pub EventPublisher) WechatMPOption {
	return func(s *WechatMPService) { s.pub = pub }
}

// NewWechatMPService 构造 WechatMPService。
func NewWechatMPService(pool *pgxpool.Pool, rdb *redis.Client, opts ...WechatMPOption) *WechatMPService {
	s := &WechatMPService{pool: pool, rdb: rdb, cache: map[string]*notify.WechatMPAccount{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

func wechatMPUnavailable() *domain.Error {
	return domain.Fail(domain.ErrInternal, domain.CodeWechatMPUnavailable, "微信通知暂未开放")
}

// account 取当前启用的服务号。没配、或停用了都按不可用处理。
func (s *WechatMPService) account(ctx context.Context) (*notify.WechatMPAccount, error) {
	var (
		id        uuid.UUID
		raw       []byte
		enabled   bool
		updatedAt time.Time
	)
	err := s.pool.QueryRow(ctx, `SELECT id, config, enabled, updated_at FROM notify_provider WHERE type = $1`,
		notify.TypeWechatMP).Scan(&id, &raw, &enabled, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, wechatMPUnavailable().WithDesc("通知中心没有 wechat_mp 供应商实例")
	}
	if err != nil {
		return nil, fmt.Errorf("service: 读取服务号配置: %w", err)
	}
	if !enabled {
		return nil, wechatMPUnavailable().WithDesc("wechat_mp 供应商实例已停用")
	}
	key := id.String() + "@" + updatedAt.UTC().Format(time.RFC3339Nano)
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.cache[key]; ok {
		return a, nil
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("service: 解析服务号配置: %w", err)
	}
	a := notify.NewWechatMPAccount(notify.Config(cfg), s.apiBase)
	clear(s.cache) // 只有一个服务号，旧版本的客户端不会再用
	s.cache[key] = a
	return a, nil
}

// WechatSubscribeQR 是一张开启微信通知的二维码。
type WechatSubscribeQR struct {
	// ImageURL 是微信托管的二维码图片，可以直接放进 <img>。
	ImageURL string
	// Content 是二维码内容，想自己画二维码时用。
	Content   string
	ExpiresIn time.Duration
}

type wechatScene struct {
	UserID string `json:"userId"`
	AppID  string `json:"appId"`
}

func wechatSceneKey(token string) string   { return "fp:wxmp:sub:" + token }
func wechatSceneByKey(token string) string { return "fp:wxmp:sub:" + token + ":by" }

// CreateSubscribeQR 给 userID 生成开启微信通知的二维码。用户扫码（没关注的先关注）后，
// 该用户的服务号通知就发给扫码的这个微信；原来绑的微信被换下。appID 是发起的应用，只用于排查。
func (s *WechatMPService) CreateSubscribeQR(ctx context.Context, appID string, userID uuid.UUID) (*WechatSubscribeQR, error) {
	if err := s.requireUser(ctx, userID); err != nil {
		return nil, err
	}
	a, err := s.account(ctx)
	if err != nil {
		return nil, err
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(wechatScene{UserID: userID.String(), AppID: appID})
	if err != nil {
		return nil, err
	}
	// 先存场景再要二维码：反过来的话，用户扫得够快就会撞上"二维码已过期"。
	if err := s.rdb.Set(ctx, wechatSceneKey(token), raw, wechatSubQRTTL).Err(); err != nil {
		return nil, fmt.Errorf("service: 保存二维码场景: %w", err)
	}
	qr, err := a.Client.CreateSceneQR(ctx, wechatSubScenePrefix+token, wechatSubQRTTL)
	if err != nil {
		slog.Error("service: 生成服务号二维码失败", "err", err)
		return nil, domain.Fail(domain.ErrInternal, domain.CodeWechatUpstreamFailed, "二维码生成失败，请稍后重试").WithDesc("%v", err)
	}
	return &WechatSubscribeQR{ImageURL: qr.ImageURL, Content: qr.Content, ExpiresIn: qr.ExpiresIn}, nil
}

func (s *WechatMPService) requireUser(ctx context.Context, userID uuid.UUID) error {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app_user WHERE id = $1)`, userID).Scan(&exists); err != nil {
		return fmt.Errorf("service: 查询用户: %w", err)
	}
	if !exists {
		return domain.Fail(domain.ErrNotFound, domain.CodeUserNotFound, "用户不存在")
	}
	return nil
}

// WechatSubscription 是一个用户的微信通知状态。
type WechatSubscription struct {
	// Bound 表示扫码开启过（或按 unionId 自动关联了）微信通知。
	Bound bool
	// Subscribed 表示绑定的微信当前仍关注着服务号。Bound && Subscribed 才收得到通知。
	Subscribed bool
	BoundAt    time.Time
}

// Subscription 查用户的微信通知状态。用户不存在也按未开启返回：业务方只关心收不收得到。
func (s *WechatMPService) Subscription(ctx context.Context, userID uuid.UUID) (*WechatSubscription, error) {
	var (
		out        WechatSubscription
		subscribed *bool
	)
	err := s.pool.QueryRow(ctx, `
		SELECT b.created_at, f.subscribed
		FROM wechat_mp_binding b LEFT JOIN wechat_mp_follower f ON f.openid = b.openid
		WHERE b.user_id = $1`, userID).Scan(&out.BoundAt, &subscribed)
	if errors.Is(err, pgx.ErrNoRows) {
		return &out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("service: 查询微信通知状态: %w", err)
	}
	out.Bound = true
	out.Subscribed = subscribed != nil && *subscribed
	return &out, nil
}

// Unbind 关掉用户的微信通知：删绑定，不碰关注状态——服务端没法替用户取关，他照样关注着服务号。
func (s *WechatMPService) Unbind(ctx context.Context, userID uuid.UUID) error {
	var creator string
	err := s.pool.QueryRow(ctx, `DELETE FROM wechat_mp_binding WHERE user_id = $1 RETURNING app_id`, userID).Scan(&creator)
	if errors.Is(err, pgx.ErrNoRows) {
		// 本来就没开：状态没变，但业务方可能刚从别的入口看到旧状态，照推一次无害。
		err = nil
	}
	if err != nil {
		return fmt.Errorf("service: 删除微信通知绑定: %w", err)
	}
	// 绑定行已删，发起扫码的应用要显式带上，否则它只在没登录过时会漏掉。
	s.announce(ctx, userID, creator)
	return nil
}

// ResolveRecipient 把通知中心 wechat_mp 渠道的收件人（fp 用户 ID）换成 openid。
func (s *WechatMPService) ResolveRecipient(ctx context.Context, to string) (string, error) {
	userID, err := uuid.Parse(to)
	if err != nil {
		return "", domain.Fail(domain.ErrInvalidArgument, domain.CodeNotifyRecipientInvalid, "服务号通知的收件人是 fp 用户 ID")
	}
	var (
		openid     string
		subscribed *bool
	)
	err = s.pool.QueryRow(ctx, `
		SELECT b.openid, f.subscribed
		FROM wechat_mp_binding b LEFT JOIN wechat_mp_follower f ON f.openid = b.openid
		WHERE b.user_id = $1`, userID).Scan(&openid, &subscribed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.Fail(domain.ErrNotFound, domain.CodeNotifyRecipientUnreachable, "该用户没有开启微信通知")
	}
	if err != nil {
		return "", fmt.Errorf("service: 查询微信通知绑定: %w", err)
	}
	if subscribed == nil || !*subscribed {
		return "", domain.Fail(domain.ErrNotFound, domain.CodeNotifyRecipientUnreachable, "该用户已取消关注服务号")
	}
	return openid, nil
}

// WechatPushResult 告诉 HTTP 层怎么回微信。
type WechatPushResult struct {
	// Body 是要回写的内容：URL 校验时是 echostr，推送时是被动回复 XML 或 "success"。
	Body []byte
	// BadRequest 表示签名不对或报文坏了，回 400。
	BadRequest bool
}

var wechatPushOK = &WechatPushResult{Body: []byte("success")}

// VerifyCallbackURL 处理公众平台配置服务器地址时的 GET 校验。
func (s *WechatMPService) VerifyCallbackURL(ctx context.Context, q url.Values) *WechatPushResult {
	a, err := s.account(ctx)
	if err != nil {
		slog.Warn("service: 服务号回调校验时服务号不可用", "err", err, "detail", detailOf(err))
		return &WechatPushResult{BadRequest: true}
	}
	echo, err := a.Server.VerifyURL(q)
	if err != nil {
		slog.Warn("service: 服务号回调地址校验失败，检查 Token 是否与公众平台一致", "err", err)
		return &WechatPushResult{BadRequest: true}
	}
	return &WechatPushResult{Body: []byte(echo)}
}

// HandlePush 处理一次服务号推送。
//
// 微信 5 秒内收不到回复会重试三次，所以每一步都可重入；出错只记日志并回 success——回错误码只会招来重试，
// 而重试解决不了数据库之类的问题。签名不对的才回 400。
func (s *WechatMPService) HandlePush(ctx context.Context, q url.Values, body []byte) *WechatPushResult {
	a, err := s.account(ctx)
	if err != nil {
		slog.Warn("service: 收到服务号推送但服务号不可用", "err", err, "detail", detailOf(err))
		return wechatPushOK
	}
	m, err := a.Server.Open(q, body)
	if err != nil {
		slog.Warn("service: 服务号推送校验失败", "err", err)
		return &WechatPushResult{BadRequest: true}
	}
	if m.MsgType != "event" || m.FromUserName == "" {
		return wechatPushOK
	}
	reply := ""
	switch m.Event {
	case wechatmp.EventUnsubscribe:
		s.setFollow(ctx, m.FromUserName, false, m.CreateTime, "")
	case wechatmp.EventSubscribe:
		s.setFollow(ctx, m.FromUserName, true, m.CreateTime, "")
		if token, ok := strings.CutPrefix(m.EventKey, "qrscene_"+wechatSubScenePrefix); ok {
			reply = s.bindScene(ctx, a, token, m.FromUserName)
		} else {
			s.autoBind(ctx, a, m.FromUserName, m.CreateTime)
			reply = a.FollowReply
		}
	case wechatmp.EventScan:
		// 能扫出 SCAN 说明他关注着，顺手纠正可能漏掉的关注事件。
		s.setFollow(ctx, m.FromUserName, true, m.CreateTime, "")
		if token, ok := strings.CutPrefix(m.EventKey, wechatSubScenePrefix); ok {
			reply = s.bindScene(ctx, a, token, m.FromUserName)
		}
	}
	if reply == "" {
		return wechatPushOK
	}
	out, err := a.Server.ReplyText(m, reply)
	if err != nil {
		slog.Error("service: 生成服务号回复失败", "err", err)
		return wechatPushOK
	}
	return &WechatPushResult{Body: out}
}

// setFollow 记关注状态。事件时间早于已记下的就不覆盖：推送可能乱序到达。
func (s *WechatMPService) setFollow(ctx context.Context, openid string, subscribed bool, createTime int64, unionID string) {
	at := time.Unix(createTime, 0)
	if createTime <= 0 {
		at = time.Now()
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO wechat_mp_follower (openid, union_id, subscribed, event_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (openid) DO UPDATE SET
			subscribed = CASE WHEN EXCLUDED.event_at >= wechat_mp_follower.event_at THEN EXCLUDED.subscribed ELSE wechat_mp_follower.subscribed END,
			event_at   = GREATEST(wechat_mp_follower.event_at, EXCLUDED.event_at),
			union_id   = CASE WHEN EXCLUDED.union_id <> '' THEN EXCLUDED.union_id ELSE wechat_mp_follower.union_id END,
			updated_at = now()`, openid, unionID, subscribed, at)
	if err != nil {
		slog.Error("service: 记录服务号关注状态失败", "err", err)
		return
	}
	s.announceOpenID(ctx, openid)
}

// announceOpenID 给绑着这个 openid 的每个用户推一次当前状态。同一个微信可以给多个 fp 用户开通知。
//
// 不比较前后状态：乱序推送、重复关注事件都可能让状态其实没变，多推一次的代价远小于漏推。
func (s *WechatMPService) announceOpenID(ctx context.Context, openid string) {
	if s.pub == nil {
		return
	}
	rows, err := s.pool.Query(ctx, `SELECT user_id FROM wechat_mp_binding WHERE openid = $1`, openid)
	if err != nil {
		slog.Error("service: 查询微信通知绑定失败", "err", err)
		return
	}
	users, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		slog.Error("service: 查询微信通知绑定失败", "err", err)
		return
	}
	for _, u := range users {
		s.announce(ctx, u)
	}
}

// announce 把用户当前的微信通知状态推给他登录过的应用，以及发起扫码的应用（extraAppIDs 是对外 app_id）。
// 推的是发布那一刻查到的状态而不是"变成了什么"：并发的两次变化各推一次，后到的那条就是最新状态。
// 失败只记日志：推送只是让客户端不必轮询，业务方仍可随时调 GetWechatSubscription 取准。
func (s *WechatMPService) announce(ctx context.Context, userID uuid.UUID, extraAppIDs ...string) {
	if s.pub == nil {
		return
	}
	sub, err := s.Subscription(ctx, userID)
	if err != nil {
		slog.Error("service: 推送前查询微信通知状态失败", "err", err, "userId", userID)
		return
	}
	rows, err := s.pool.Query(ctx, `
		SELECT application_id FROM user_extra WHERE user_id = $1
		UNION
		SELECT a.id FROM application a
		WHERE a.app_id = ANY($2) OR a.app_id IN (SELECT app_id FROM wechat_mp_binding WHERE user_id = $1)`,
		userID, extraAppIDs)
	if err != nil {
		slog.Error("service: 查询要通知的应用失败", "err", err, "userId", userID)
		return
	}
	apps, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		slog.Error("service: 查询要通知的应用失败", "err", err, "userId", userID)
		return
	}
	now := time.Now().UnixMilli()
	for _, app := range apps {
		// 每个应用一条：撤销频道按 AppID 过滤，uuid.Nil 会推给所有应用。
		publishEvent(ctx, s.pub, domain.RevokeEvent{
			Kind: domain.EventKindWechatSubscriptionChanged, AppID: app,
			UserIDs: []uuid.UUID{userID}, Subscribed: sub.Bound && sub.Subscribed, At: now,
		})
	}
}

// 回给扫码人的提示。场景失效时他还在服务号的聊天界面里，得告诉他下一步怎么做。
const (
	wechatReplyExpired = "二维码已过期，请回到应用刷新二维码后重新扫码。"
	wechatReplyUsed    = "这个二维码已被其他微信使用，请回到应用刷新二维码后重新扫码。"
	wechatReplyFailed  = "开启通知失败，请稍后回到应用重新扫码。"
)

// bindScene 把扫码人绑到二维码对应的用户上，返回回给扫码人的文字。
//
// 一张码只给第一个扫的微信用；同一个微信重复扫（包括微信超时重推同一个事件）照常成功。
func (s *WechatMPService) bindScene(ctx context.Context, a *notify.WechatMPAccount, token, openid string) string {
	raw, err := s.rdb.Get(ctx, wechatSceneKey(token)).Bytes()
	if errors.Is(err, redis.Nil) {
		return wechatReplyExpired
	}
	if err != nil {
		slog.Error("service: 读取二维码场景失败", "err", err)
		return wechatReplyFailed
	}
	var sc wechatScene
	if err := json.Unmarshal(raw, &sc); err != nil {
		slog.Error("service: 二维码场景损坏", "err", err)
		return wechatReplyFailed
	}
	userID, err := uuid.Parse(sc.UserID)
	if err != nil {
		slog.Error("service: 二维码场景里的用户 ID 不合法", "err", err)
		return wechatReplyFailed
	}

	ttl, err := s.rdb.TTL(ctx, wechatSceneKey(token)).Result()
	if err != nil || ttl <= 0 {
		ttl = wechatSubQRTTL
	}
	ok, err := s.rdb.SetNX(ctx, wechatSceneByKey(token), openid, ttl).Result()
	if err != nil {
		slog.Error("service: 占用二维码失败", "err", err)
		return wechatReplyFailed
	}
	if !ok {
		by, err := s.rdb.Get(ctx, wechatSceneByKey(token)).Result()
		if err != nil {
			slog.Error("service: 读取二维码占用者失败", "err", err)
			return wechatReplyFailed
		}
		if by != openid {
			return wechatReplyUsed
		}
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO wechat_mp_binding (user_id, openid, app_id) VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET openid = EXCLUDED.openid, app_id = EXCLUDED.app_id,
			created_at = CASE WHEN wechat_mp_binding.openid = EXCLUDED.openid THEN wechat_mp_binding.created_at ELSE now() END`,
		userID, openid, sc.AppID)
	if err != nil {
		// 多半是二维码有效期内用户被删了。
		slog.Error("service: 写入微信通知绑定失败", "err", err, "userId", userID)
		return wechatReplyFailed
	}
	slog.Info("service: 用户开启微信通知", "userId", userID, "appId", sc.AppID, "serviceAppId", a.Client.AppID())
	s.announce(ctx, userID)
	return a.BindReply
}

// autoBind 处理用户自己搜索关注的情况：按 unionId 找到用微信登录过的 fp 用户，他还没开过微信通知就替他开上。
// 同一个人的网站应用 / 移动应用 / 服务号共用 unionId，前提是这几个应用挂在同一个开放平台账号下。
//
// 只在「还没绑过」时绑：用户主动关掉过通知、或绑的是别的微信，都不该被一次关注改掉。
func (s *WechatMPService) autoBind(ctx context.Context, a *notify.WechatMPAccount, openid string, createTime int64) {
	ctx, cancel := context.WithTimeout(ctx, wechatUserInfoTimeout)
	defer cancel()
	info, err := a.Client.UserInfo(ctx, openid)
	if err != nil {
		slog.Warn("service: 查询关注者 unionid 失败，跳过自动关联", "err", err)
		return
	}
	if info.UnionID == "" {
		return
	}
	s.setFollow(ctx, openid, true, createTime, info.UnionID)
	var userID uuid.UUID
	err = s.pool.QueryRow(ctx, `
		INSERT INTO wechat_mp_binding (user_id, openid, app_id)
		SELECT u.user_id, $2, '' FROM (SELECT user_id FROM identity WHERE union_key = $1 LIMIT 1) u
		ON CONFLICT (user_id) DO NOTHING
		RETURNING user_id`, info.UnionID, openid).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if err != nil {
		slog.Error("service: 按 unionid 自动关联微信通知失败", "err", err)
		return
	}
	slog.Info("service: 按 unionid 自动开启微信通知", "serviceAppId", a.Client.AppID())
	s.announce(ctx, userID)
}
