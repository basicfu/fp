package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/store"
)

// SessionService 负责令牌的签发与校验。
//
// 设计文档 4.5.1 的核心约束在 Validate 里：**返回 cache_ttl 而非 expiry**。
// SDK 只按 cache_ttl 缓存判定结果，永远不自己推断 token 是否过期，
// 因此不存在「一处延期、另一处按旧 expiry 误判」的竞态。
type SessionService struct {
	store  *store.SessionStore
	pub    *store.RevokePublisher
	epochs EpochStore
	// now 返回当前毫秒时间戳。可注入，便于测试过期与轮换边界。
	now func() int64
}

// EpochStore 是 SessionService 需要的最小纪元能力。
// 用窄接口而非直接依赖 *store.EpochStore，是为了让测试能注入桩，
// 也让这层的意图一目了然：它只读写一个计数器。
type EpochStore interface {
	Current(ctx context.Context, userID uuid.UUID) (int64, error)
}

// NewSessionService 构造使用真实时钟的 SessionService。
func NewSessionService(st *store.SessionStore, pub *store.RevokePublisher, ep EpochStore) *SessionService {
	return NewSessionServiceWithClock(st, pub, ep, func() int64 { return time.Now().UnixMilli() })
}

// NewSessionServiceWithClock 构造使用自定义时钟的 SessionService。
func NewSessionServiceWithClock(st *store.SessionStore, pub *store.RevokePublisher, ep EpochStore, now func() int64) *SessionService {
	return &SessionService{store: st, pub: pub, epochs: ep, now: now}
}

// IssueInput 描述一次签发请求。
type IssueInput struct {
	UserID uuid.UUID
	App    *domain.Application
	IP     string
	UA     string
	// Mobile 决定适用哪一档空闲超时。
	Mobile bool
}

// Issue 为一次成功的认证签发新会话。
func (s *SessionService) Issue(ctx context.Context, in IssueInput) (*domain.Session, error) {
	if in.App == nil {
		return nil, domain.Errorf(domain.ErrInvalidArgument, "签发会话缺少应用信息")
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}

	// 纪元在这里读、刻进会话。读得越早能拦住的撤销越多，但不能早于登录流程本身；
	// 这个读之前发生的撤销由 AuthService.recheckLoginable 兜住（见本任务开头的论证）。
	epoch, err := s.epochs.Current(ctx, in.UserID)
	if err != nil {
		return nil, err
	}

	now := s.now()
	idle := in.App.Session.IdleTimeoutFor(in.Mobile)

	sess := &domain.Session{
		ID:             uuid.NewString(),
		Token:          token,
		UserID:         in.UserID,
		AppID:          in.App.ID,
		FirstAuthAt:    now,
		IssuedAt:       now,
		LastExtendedAt: now,
		IdleExpiresAt:  now + idle.Milliseconds(),
		IP:             in.IP,
		UA:             in.UA,
		Mobile:         in.Mobile,
		Epoch:          epoch,
	}
	if err := s.store.Put(ctx, sess, idle); err != nil {
		return nil, err
	}
	return sess, nil
}

// ValidateResult 是一次校验的结果。
type ValidateResult struct {
	Session *domain.Session
	// CacheTTL 是 SDK 可以缓存本次判定结果的时长，
	// 等于 min(应用配置的 token_cache_ttl, token 剩余有效期)。
	//
	// 契约：0 表示"不要缓存"，不是"未设置"。会话距 max_lifetime 只剩
	// 不到一毫秒时，剩余有效期本身就会算出 0——这是合法值，调用方
	// （计划二的 SDK）不能把 0 当成"没给，套用本地默认值"来处理，
	// 否则一个即将到期的会话会被缓存本不该有的时长。
	CacheTTL time.Duration
	// Rotated 为 true 时 NewToken 非空，调用方须把新 token 下发给客户端。
	Rotated  bool
	NewToken string
}

// MinRotateGrace 是 token 轮换后旧 token 的最短过渡期。
const MinRotateGrace = 15 * time.Second

// extendLockTTL 是**延期写**的去重锁时长。轮换用的是过渡期时长，不是这个值。
// 它只用于限流，不保护临界区，因此不需要显式释放。
const extendLockTTL = 10 * time.Second

// GraceDuration 返回 token 轮换后旧 token 的过渡期。
//
// 取 max(15s, token_cache_ttl)：过渡期短于缓存窗口的话，
// SDK 本地缓存里的旧 token 会在过渡期结束后仍被放行——
// 请求打到 fp 时旧 token 已经不存在，用户被无谓地踢掉。
func GraceDuration(p domain.SessionPolicy) time.Duration {
	g := MinRotateGrace
	if c := time.Duration(p.TokenCacheTTLSeconds) * time.Second; c > g {
		g = c
	}
	return g
}

// Validate 校验 token，必要时顺带轮换或延期。
//
// 校验失败一律返回 domain.ErrUnauthorized 的包装，不区分「不存在」「已过期」
// 「不属于本应用」——这些差异对调用方没有意义，却会给攻击者提供信息。
func (s *SessionService) Validate(ctx context.Context, token string, app *domain.Application) (*ValidateResult, error) {
	if app == nil {
		return nil, domain.Errorf(domain.ErrInvalidArgument, "校验 token 缺少应用信息")
	}
	if token == "" {
		return nil, domain.Errorf(domain.ErrUnauthorized, "缺少 token")
	}

	sess, err := s.store.Get(ctx, token)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, domain.Errorf(domain.ErrUnauthorized, "token 无效或已过期")
	}
	if err != nil {
		return nil, err
	}
	// token 与应用必须匹配：A 应用签发的 token 不能在 B 应用上使用。
	if sess.AppID != app.ID {
		return nil, domain.Errorf(domain.ErrUnauthorized, "token 无效或已过期")
	}

	// 纪元比对：会话刻的纪元与当前不一致，说明它是在一次撤销的竞态窗口里
	// 签发的，撤销的清扫没扫到它。
	epoch, err := s.epochs.Current(ctx, sess.UserID)
	if err != nil {
		return nil, err
	}
	if epoch != sess.Epoch {
		// 主动删掉：留着它只会让后续每次校验都白跑两趟 Redis，
		// 而它永远不可能再通过。
		if err := s.store.Delete(ctx, sess.Token); err != nil {
			slog.Error("service: 删除纪元失配的会话失败", "err", err, "sessionId", sess.ID)
		}
		return nil, domain.Errorf(domain.ErrUnauthorized, "token 无效或已过期")
	}

	now := s.now()
	// 以 IdleExpiresAt / MaxExpiresAt 为准，不看 Redis TTL——TTL 只是兜底清理，
	// 时钟精度或取整都不该成为放行一个已过期 token 的理由。
	if sess.RemainingAt(now, app.Session) <= 0 {
		return nil, domain.Errorf(domain.ErrUnauthorized, "token 无效或已过期")
	}

	res := &ValidateResult{Session: sess}

	// 轮换优先于延期：轮换本身就会给新会话一个完整的空闲窗口。
	if now-sess.IssuedAt >= app.Session.RotateInterval().Milliseconds() {
		rotated, newSess, err := s.tryRotate(ctx, sess, app, now)
		if err != nil {
			return nil, err
		}
		if rotated {
			res.Session = newSess
			res.Rotated = true
			res.NewToken = newSess.Token
		}
	} else if now-sess.LastExtendedAt >= app.Session.ExtendInterval().Milliseconds() {
		extended, err := s.tryExtend(ctx, sess, app, now)
		if err != nil {
			return nil, err
		}
		if extended != nil {
			res.Session = extended
		}
	}

	res.CacheTTL = cacheTTL(res.Session.RemainingAt(s.now(), app.Session), app.Session)
	return res, nil
}

// tryExtend 在拿到去重锁时刷新空闲超时，否则原样返回 nil。
//
// 两层降频（设计文档 4.5.2）：调用方已按 extend_interval 做了时间窗判断，
// 这里的锁负责挡住同一瞬间的并发请求，避免同一行被反复 UPDATE。
func (s *SessionService) tryExtend(ctx context.Context, sess *domain.Session, app *domain.Application, now int64) (*domain.Session, error) {
	ok, err := s.store.TryLock(ctx, "ext:"+sess.Token, extendLockTTL)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	idle := app.Session.IdleTimeoutFor(sess.Mobile)
	updated := *sess
	updated.LastExtendedAt = now
	updated.IdleExpiresAt = now + idle.Milliseconds()

	// Redis TTL 取剩余有效期而非空闲超时：绝对上限更近时，
	// 让 Redis 在上限时刻自动清理，避免留下必然会被拒绝的僵尸会话。
	if err := s.store.Put(ctx, &updated, updated.RemainingAt(now, app.Session)); err != nil {
		return nil, err
	}
	return &updated, nil
}

// tryRotate 在拿到去重锁时换发新 token，并把旧 token 缩短到过渡期。
//
// 新会话继承 ID 与 FirstAuthAt：轮换只换 token 的值，会话本身没有变。
// 特别是 FirstAuthAt 绝不能重置，否则 max_lifetime 会被活跃用户无限续命。
func (s *SessionService) tryRotate(ctx context.Context, sess *domain.Session, app *domain.Application, now int64) (bool, *domain.Session, error) {
	grace := GraceDuration(app.Session)

	// 锁的 TTL 取过渡期：过渡期内旧 token 仍可用，但不应再次触发轮换。
	ok, err := s.store.TryLock(ctx, "rot:"+sess.ID, grace)
	if err != nil {
		return false, nil, err
	}
	if !ok {
		return false, nil, nil
	}

	newToken, err := randomToken()
	if err != nil {
		return false, nil, err
	}
	idle := app.Session.IdleTimeoutFor(sess.Mobile)

	newSess := *sess
	newSess.Token = newToken
	newSess.IssuedAt = now
	newSess.LastExtendedAt = now
	newSess.IdleExpiresAt = now + idle.Milliseconds()
	if err := s.store.Put(ctx, &newSess, newSess.RemainingAt(now, app.Session)); err != nil {
		return false, nil, err
	}

	// 旧 token 缩短到过渡期。
	// 同时把 IdleExpiresAt 一起改小——只改 Redis TTL 的话，会话 JSON 里
	// 仍是很远的过期时间，cache_ttl 会按完整窗口下发，而 Redis key 在过渡期
	// 结束就没了，SDK 会拿着一个已被删除的 token 继续放行。
	//
	// 特意不改 IssuedAt：过渡期内旧 token 再次被校验时，(now-IssuedAt) 依旧
	// 越过 rotate_interval，会继续走 Validate 的 if 分支进入 tryRotate——
	// 但 "rot:"+sess.ID 锁此时仍握着（TTL 正是 grace），会直接把它拦下，
	// 函数原样返回。这一步真正的作用是把控制流留在 if 分支，不落到
	// else if 的延期分支：一旦落到延期分支，LastExtendedAt 早已陈旧，会被
	// 判定为"该延期"，从而把刚缩短的 IdleExpiresAt 重新拉回一整个空闲窗口，
	// 过渡期形同虚设。
	oldSess := *sess
	oldSess.IdleExpiresAt = now + grace.Milliseconds()
	if err := s.store.Put(ctx, &oldSess, grace); err != nil {
		return false, nil, err
	}

	return true, &newSess, nil
}

// SessionByToken 按 token 读取会话，不做任何过期判定，也不产生副作用。
//
// 只给需要"撤销之前先拿归属信息"的调用方用（例如登出要记审计）。
// 鉴权一律走 Validate——它才会判过期、判应用归属，并给出 cache_ttl。
func (s *SessionService) SessionByToken(ctx context.Context, token string) (*domain.Session, error) {
	return s.store.Get(ctx, token)
}

// ListByUser 返回该用户当前存活的全部会话，用于在线设备列表。
func (s *SessionService) ListByUser(ctx context.Context, userID uuid.UUID) ([]domain.Session, error) {
	tokens, err := s.store.ListUserTokens(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Session, 0, len(tokens))
	for _, tok := range tokens {
		sess, err := s.store.Get(ctx, tok)
		if errors.Is(err, domain.ErrNotFound) {
			// 读取与索引清理之间存在窗口，跳过即可。
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, *sess)
	}
	return out, nil
}

// maxTokensPerEvent 是单条撤销事件能携带的 token 上限。
//
// 一万个用户 × 每人 3 个会话 = 三万个 token，序列化后约 1 MB；广播给 20 个
// fp 实例就是 20 MB，SDK 侧还要吃一个逼近 gRPC 默认 4 MB 接收上限的消息。
// 超出就切成多条——"批量"是为了减少广播条数，不是为了把单条撑到无界。
const maxTokensPerEvent = 1000

// Revoke 作废单个 token 所属的**整个会话**，用于登出。
//
// 注意它撤销的是会话而不只是这一个 token：轮换过渡期内同一个会话有新旧
// 两个 token 同时有效，只删调用方递上来的那个，另一个会继续有效最多
// GraceDuration（默认 30 秒），而且不会出现在撤销事件里——SDK 那边的缓存
// 也照样放行。用户点了"退出登录"却还能被另一个 token 访问，这不可接受。
func (s *SessionService) Revoke(ctx context.Context, token, reason string) error {
	sess, err := s.store.Get(ctx, token)
	if errors.Is(err, domain.ErrNotFound) {
		// 已经不存在了，登出视为成功。重复登出、并发登出都会走到这里。
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.revokeMatching(ctx, []uuid.UUID{sess.UserID}, sess.AppID, reason, func(x *domain.Session) bool {
		return x.ID == sess.ID
	})
	return err
}

// RevokeSession 作废一个会话的全部 token，包括轮换过渡期内尚存的旧 token。
// sessionID 不属于该用户时不做任何事，返回 0。
//
// 绝不能递增撤销纪元：纪元是用户级的，递增会把该用户全部设备一起踢下线，
// 一次"踢掉这台平板"就会连累手机、电脑。这层压根不持有 EpochBumper
// （只有 AccountService 持有），结构上就做不到误加。
func (s *SessionService) RevokeSession(ctx context.Context, userID uuid.UUID, sessionID, reason string) (int, error) {
	return s.revokeMatching(ctx, []uuid.UUID{userID}, uuid.Nil, reason, func(sess *domain.Session) bool {
		return sess.ID == sessionID
	})
}

// RevokeUsers 撤销一批用户的全部会话。
//
// 这是**唯一实现**：revokeMatching 本身就是按 userIDs 切片写的，Revoke /
// RevokeSession / RevokeUser / RevokeUserInApp 全部只是它在不同参数下的
// 特例，单用户路径永远传一个元素的切片。两套实现意味着修一个 bug 要改
// 两处，而漏改的那一处不会有任何测试变红——两条路径各有各的绿测试。
func (s *SessionService) RevokeUsers(ctx context.Context, userIDs []uuid.UUID, reason string) (int, error) {
	return s.revokeMatching(ctx, userIDs, uuid.Nil, reason, func(*domain.Session) bool { return true })
}

// RevokeUser 撤销单个用户的全部会话。用于封号、改密。是 RevokeUsers 的单元素包装。
func (s *SessionService) RevokeUser(ctx context.Context, userID uuid.UUID, reason string) (int, error) {
	return s.RevokeUsers(ctx, []uuid.UUID{userID}, reason)
}

// RevokeUserInApp 只作废该用户在指定应用下的会话。
// 多个应用共享同一套用户体系时，封禁某个应用的账号不应波及其他应用。
func (s *SessionService) RevokeUserInApp(ctx context.Context, userID, appID uuid.UUID, reason string) (int, error) {
	return s.revokeMatching(ctx, []uuid.UUID{userID}, appID, reason, func(sess *domain.Session) bool {
		return sess.AppID == appID
	})
}

// revokeMatching 遍历这些用户的会话，删除满足 match 的那些，按 maxTokensPerEvent
// 切片广播。
//
// userIDs 为多个元素时是 RevokeUsers 的批量路径；Revoke / RevokeSession /
// RevokeUser / RevokeUserInApp 这些单用户入口都传一个元素的切片——这是全部
// 撤销操作**唯一**的底层实现，不能有第二份（理由见 RevokeUsers 的注释）。
//
// eventAppID 直接写入事件而不从会话推断：会话删除后已无从查证，
// 而调用方本来就知道这次撤销的作用域（uuid.Nil 表示跨全部应用）。
func (s *SessionService) revokeMatching(
	ctx context.Context,
	userIDs []uuid.UUID, eventAppID uuid.UUID,
	reason string,
	match func(*domain.Session) bool,
) (int, error) {
	var (
		tokens  []string
		touched []uuid.UUID
	)

	// 中途出错时，已经删掉的 token 必须照样广播出去。
	//
	// 它们在 Redis 里是真的没了（权威撤销已完成），但如果因为报错而跳过
	// announce，SDK 那边就收不到通知，会继续拿本地缓存放行最长一个 cache_ttl。
	// 所以这里用 defer 兜住：无论正常返回还是中途返回，只要删过东西就广播，
	// 并如实返回"已撤销的条数 + 错误"，而不是谎报 0。
	//
	// 用 WithoutCancel 剥掉取消信号。
	//
	// 触发这个 defer 的失败里，最常见的一种恰恰就是 ctx 被取消——
	// 管理员对大批用户执行撤销撞上 handler 超时、或客户端断开。那时
	// store.Delete 因 ctx 报错退出，而 deferred 的 announce 若沿用
	// 同一个已死的 ctx，go-redis 会在取连接阶段直接拒掉 PUBLISH——
	// token 已经从 Redis 删了，事件却发不出去，SDK 继续用缓存放行
	// 一整个 cache_ttl。这正是 defer 要堵的洞，不剥掉取消信号就等于没堵。
	defer func() {
		if len(tokens) == 0 {
			return
		}
		s.announceBatch(context.WithoutCancel(ctx), tokens, touched, eventAppID, reason)
	}()

	for _, uid := range userIDs {
		before := len(tokens)
		err := s.revokeUserTokens(ctx, uid, match, &tokens)
		// 没有贡献 token 的用户不进 touched——批量踢 500 个用户时大多数本来
		// 就不在线，给它们各发一条空事件等于把刚省下的广播又加回来。即使
		// 这个用户处理到一半出错，只要它已经贡献过 token，也要算进
		// touched，否则事件里的 Tokens 与 UserIDs 会对不上。
		if len(tokens) > before {
			touched = append(touched, uid)
		}
		if err != nil {
			return len(tokens), err
		}
	}
	return len(tokens), nil
}

// revokeUserTokens 删除单个用户满足 match 的会话，把被删的 token 追加进 *tokens。
//
// 这里不直接调用 store.DeleteUserTokens 做批量删除：撤销事件必须携带具体的
// token 列表（SDK 按 token 缓存校验结果，只给 userID 无从得知该清哪些缓存
// 条目），而 DeleteUserTokens 只返回删除的条数，不返回具体是哪些 token，
// 所以这里逐个 token 处理。
//
// 顺带记一笔 DeleteUserTokens 自身的契约，供以后想换成它做批量优化的人参考：
// 它分批删除，中途某一批失败时会**同时**返回已经真实删除的条数和一个非
// nil 错误——那些删除已经在 Redis 里真实发生了，"出错就当 0 个"会让管理员
// 误以为撤销完全没生效。
func (s *SessionService) revokeUserTokens(ctx context.Context, userID uuid.UUID, match func(*domain.Session) bool, tokens *[]string) error {
	toks, err := s.store.ListUserTokens(ctx, userID)
	if err != nil {
		return err
	}
	for _, tok := range toks {
		sess, err := s.store.Get(ctx, tok)
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if !match(sess) {
			continue
		}
		if err := s.store.Delete(ctx, tok); err != nil {
			return err
		}
		*tokens = append(*tokens, tok)
	}
	return nil
}

// announce 广播撤销事件。发布失败只记日志不返回错误——
// 权威撤销（删除会话）已经完成，推送仅是加速手段，
// 最坏情况下 SDK 在一个 cache_ttl 后回源也会被拒绝。
func (s *SessionService) announce(ctx context.Context, ev domain.RevokeEvent) {
	if s.pub == nil {
		return
	}
	if err := s.pub.Publish(ctx, ev); err != nil {
		slog.Error("service: 广播撤销事件失败", "err", err, "userIds", ev.UserIDs)
	}
}

// announceBatch 把一批 token 按 maxTokensPerEvent 切片广播。
//
// 切片时 UserIDs 原样带在每一条上：它只用于日志，精确对应哪一片没有意义，
// 而"这批撤销涉及哪些用户"对排障是有意义的。
func (s *SessionService) announceBatch(ctx context.Context, tokens []string, userIDs []uuid.UUID, appID uuid.UUID, reason string) {
	for start := 0; start < len(tokens); start += maxTokensPerEvent {
		end := min(start+maxTokensPerEvent, len(tokens))
		s.announce(ctx, domain.RevokeEvent{
			Tokens:  tokens[start:end],
			UserIDs: userIDs,
			AppID:   appID,
			Reason:  reason,
			At:      s.now(),
		})
	}
}

// cacheTTL 计算 SDK 可缓存的时长：不超过应用配置，也不超过 token 剩余有效期。
//
// 后半条是整个方案的安全底线——它保证「token 越接近过期，SDK 回源越频繁」，
// 从而杜绝过期 token 被本地缓存继续放行。
func cacheTTL(remaining time.Duration, p domain.SessionPolicy) time.Duration {
	configured := time.Duration(p.TokenCacheTTLSeconds) * time.Second
	if remaining < configured {
		return remaining
	}
	return configured
}
