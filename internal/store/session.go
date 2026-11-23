package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/domain"
)

// Redis 键前缀。
//
//	fp:sess:{token}   → 会话 JSON，TTL 与 IdleExpiresAt 保持一致（权威数据）
//	fp:usess:{userID} → 该用户的 token 集合（超集，用于在线设备与批量撤销）
const (
	sessionKeyPrefix   = "fp:sess:"
	userSessionsPrefix = "fp:usess:"
	sessionLockPrefix  = "fp:lock:"
	// rotationPrefix 是「旧 token → 新 token」映射的键前缀。
	rotationPrefix = "fp:rot:"
	// kickedPrefix 是被踢会话的墓碑：fp:sess:kicked:{sha256(token)} → 应用内部 ID。
	// 键里放哈希而不是 token 原文：墓碑要留到会话本来的绝对上限，没必要让一份可用凭据的原文在 Redis 里多躺几十天。
	kickedPrefix = "fp:sess:kicked:"
)

func kickedKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return kickedPrefix + hex.EncodeToString(sum[:])
}

// PutKicked 给被踢会话的 token 立墓碑，之后校验它得到「已被移出」而不是「登录过期」。ttl 不为正时不写。
func (s *SessionStore) PutKicked(ctx context.Context, token string, appID uuid.UUID, ttl time.Duration) error {
	if ttl <= 0 {
		return nil
	}
	if err := s.rdb.Set(ctx, kickedKey(token), appID.String(), ttl).Err(); err != nil {
		return fmt.Errorf("store: 写入踢出墓碑: %w", err)
	}
	return nil
}

// KickedApp 读 token 的踢出墓碑，返回当时会话所属的应用。没有墓碑时 ok 为假。
func (s *SessionStore) KickedApp(ctx context.Context, token string) (appID uuid.UUID, ok bool, err error) {
	v, err := s.rdb.Get(ctx, kickedKey(token)).Result()
	if errors.Is(err, redis.Nil) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("store: 读取踢出墓碑: %w", err)
	}
	id, err := uuid.Parse(v)
	if err != nil {
		return uuid.Nil, false, nil
	}
	return id, true, nil
}

// SessionStore 用 Redis 存放会话。
//
// 会话只存 Redis、不落 PostgreSQL：这沿用了 3s 的成熟做法，
// 代价是 Redis 数据丢失等同全体重新登录——这是 3s 现在就接受的风险。
type SessionStore struct {
	rdb *redis.Client
}

// NewSessionStore 构造 SessionStore。
func NewSessionStore(rdb *redis.Client) *SessionStore {
	return &SessionStore{rdb: rdb}
}

// Put 写入会话，并把 token 记进该用户的索引。**延期也走这里**——
// 它是唯一同时刷新会话主键与用户索引的入口，只刷主键会让索引先于会话过期，
// 在线设备列表静默清空、批量撤销漏掉仍然有效的会话。
//
// 索引 TTL 取会话 TTL 与 7 天中的**较大者**，保证索引不早于会话过期。
// 索引本身是超集（允许含已过期的 token），ListUserTokens 读取时会清理。
//
// ttl <= 0 直接报错：go-redis 对 expiration <= 0 会**省略 TTL 参数**，
// SET 出来的是一个永不过期的键。这类键没有任何东西会回收它——
// ListUserTokens 只清理"已消失"的键，反而会把它当成活跃会话一直列出去。
func (s *SessionStore) Put(ctx context.Context, sess *domain.Session, ttl time.Duration) error {
	return s.put(ctx, sess, ttl, 0)
}

// PutTracked 同 Put，并在同一个事务里把会话的到期时刻（毫秒）记进到期集合，供过期扫描发 logged_out(expired)。
// 签发、延期、轮换出的新 token 走它；轮换后缩短到过渡期的旧 token 走 Put——那不是会话的到期时刻。
func (s *SessionStore) PutTracked(ctx context.Context, sess *domain.Session, ttl time.Duration, expiresAt int64) error {
	return s.put(ctx, sess, ttl, expiresAt)
}

func (s *SessionStore) put(ctx context.Context, sess *domain.Session, ttl time.Duration, expiresAt int64) error {
	if ttl <= 0 {
		return domain.Fail(domain.ErrInternal, domain.CodeInternal, "服务器内部错误").
			WithDesc("会话 TTL 必须为正，收到 %v（写入会得到一个永不过期的键）", ttl)
	}
	raw, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("store: 序列化会话: %w", err)
	}

	indexTTL := ttl
	if indexTTL < 7*24*time.Hour {
		indexTTL = 7 * 24 * time.Hour
	}

	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, sessionKeyPrefix+sess.Token, raw, ttl)
	pipe.SAdd(ctx, userSessionsPrefix+sess.UserID.String(), sess.Token)
	pipe.Expire(ctx, userSessionsPrefix+sess.UserID.String(), indexTTL)
	if expiresAt > 0 {
		pipe.ZAdd(ctx, SessionExpiryKey, redis.Z{Score: float64(expiresAt), Member: ExpiryMember(sess)})
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("store: 写入会话: %w", err)
	}
	return nil
}

// Get 按 token 读会话。不存在或已过期返回 domain.ErrNotFound。
func (s *SessionStore) Get(ctx context.Context, token string) (*domain.Session, error) {
	raw, err := s.rdb.Get(ctx, sessionKeyPrefix+token).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, domain.Failf(domain.ErrNotFound, domain.CodeSessionNotFound, "会话不存在或已过期")
	}
	if err != nil {
		return nil, fmt.Errorf("store: 读取会话: %w", err)
	}
	var sess domain.Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, fmt.Errorf("store: 解析会话: %w", err)
	}
	return &sess, nil
}

// Delete 删除一个会话。token 不存在时返回 nil。
func (s *SessionStore) Delete(ctx context.Context, token string) error {
	// 先读出会话拿到 userID，才能同步清理索引。读不到就只删主键。
	sess, err := s.Get(ctx, token)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}

	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, sessionKeyPrefix+token)
	if sess != nil {
		pipe.SRem(ctx, userSessionsPrefix+sess.UserID.String(), token)
		// 撤销了就不该再报过期。成员按会话不按 token，轮换的新旧 token 删哪个都摘同一个成员。
		pipe.ZRem(ctx, SessionExpiryKey, ExpiryMember(sess))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("store: 删除会话: %w", err)
	}
	return nil
}

// ListUserTokens 返回该用户当前仍然存活的 token，并顺手把索引里的悬挂项移除。
func (s *SessionStore) ListUserTokens(ctx context.Context, userID uuid.UUID) ([]string, error) {
	indexKey := userSessionsPrefix + userID.String()
	tokens, err := s.rdb.SMembers(ctx, indexKey).Result()
	if err != nil {
		return nil, fmt.Errorf("store: 读取用户会话索引: %w", err)
	}
	if len(tokens) == 0 {
		return []string{}, nil
	}

	alive := make([]string, 0, len(tokens))
	var dangling []any

	// 分批发命令。MGET / SREM / DEL 都是 O(N) 且在 Redis 单线程里跑完才让位，
	// 一次塞进成千上万个 key 会把整个实例卡住——而一个长期活跃、从不显式登出
	// 的用户，索引里堆积几千个 token 是完全可能的。
	for _, batch := range chunkStrings(tokens, redisBatchSize) {
		keys := make([]string, len(batch))
		for i, tok := range batch {
			keys[i] = sessionKeyPrefix + tok
		}
		values, err := s.rdb.MGet(ctx, keys...).Result()
		if err != nil {
			return nil, fmt.Errorf("store: 批量读取会话: %w", err)
		}
		for i, v := range values {
			if v == nil {
				dangling = append(dangling, batch[i])
				continue
			}
			alive = append(alive, batch[i])
		}
	}

	for _, batch := range chunkAny(dangling, redisBatchSize) {
		if err := s.rdb.SRem(ctx, indexKey, batch...).Err(); err != nil {
			// 清理失败不该让整个设备列表不可用——读取本身已经成功了。
			slog.Warn("store: 清理悬挂 token 失败", "err", err, "count", len(batch))
			break
		}
	}
	return alive, nil
}

// redisBatchSize 是单条 Redis 命令携带的最大 key/member 数。
const redisBatchSize = 500

func chunkStrings(in []string, size int) [][]string {
	var out [][]string
	for len(in) > size {
		out = append(out, in[:size])
		in = in[size:]
	}
	if len(in) > 0 {
		out = append(out, in)
	}
	return out
}

func chunkAny(in []any, size int) [][]any {
	var out [][]any
	for len(in) > size {
		out = append(out, in[:size])
		in = in[size:]
	}
	if len(in) > 0 {
		out = append(out, in)
	}
	return out
}

// DeleteUserTokens 撤销该用户的全部会话，返回 Redis 实际删除的键数。
func (s *SessionStore) DeleteUserTokens(ctx context.Context, userID uuid.UUID) (int, error) {
	tokens, err := s.ListUserTokens(ctx, userID)
	if err != nil {
		return 0, err
	}
	if len(tokens) == 0 {
		return 0, nil
	}

	// 同样分批，理由见 ListUserTokens。
	deleted := 0
	for _, batch := range chunkStrings(tokens, redisBatchSize) {
		keys := make([]string, len(batch))
		members := make([]any, len(batch))
		for i, tok := range batch {
			keys[i] = sessionKeyPrefix + tok
			members[i] = tok
		}

		pipe := s.rdb.TxPipeline()
		del := pipe.Del(ctx, keys...)
		pipe.SRem(ctx, userSessionsPrefix+userID.String(), members...)
		if _, err := pipe.Exec(ctx); err != nil {
			return deleted, fmt.Errorf("store: 批量删除会话: %w", err)
		}
		// 返回 Redis 实际删掉的条数，而不是我们打算删的条数——
		// 撤销事件会把这个数字报给管理员，虚报没有意义。
		deleted += int(del.Val())
	}
	return deleted, nil
}

// TryLock 尝试获取一把带 TTL 的互斥锁，用于给延期写与轮换去重。
// 锁不需要显式释放，靠 TTL 自然过期即可——它的作用是限流而非临界区保护。
func (s *SessionStore) TryLock(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	ok, err := s.rdb.SetNX(ctx, sessionLockPrefix+key, 1, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("store: 获取锁: %w", err)
	}
	return ok, nil
}

// PutRotation 记下一次轮换的去向：旧 token 换成了哪个新 token。
//
// ttl 必须与轮换过渡期一致。过渡期内每一次带旧 token 的校验都会读这条映射，
// 把新 token 再告知一次——轮换的交接因此从"一次性投递"变成"重复告知"，
// 不再依赖某一个响应必须送达。
//
// 拒绝非正的 ttl：go-redis 在 ttl <= 0 时不发 EX 参数，写进去的是永不过期的键。
// 一条不朽的轮换映射会在旧 token 早已死透之后，继续把客户端指向一个同样
// 不存在的新 token。
func (s *SessionStore) PutRotation(ctx context.Context, oldToken, newToken string, ttl time.Duration) error {
	if ttl <= 0 {
		return domain.Fail(domain.ErrInternal, domain.CodeInternal, "服务器内部错误").WithDesc("轮换映射的 ttl 必须为正，得到 %v", ttl)
	}
	if err := s.rdb.Set(ctx, rotationPrefix+oldToken, newToken, ttl).Err(); err != nil {
		return fmt.Errorf("store: 写入轮换映射: %w", err)
	}
	return nil
}

// RotatedTo 返回旧 token 轮换后的新 token。
//
// 没有记录时返回空串与 nil error，而不是 ErrNotFound：调用方问的是
// "这个 token 轮换过吗"，"没有"是一个正常答案，不是异常。
func (s *SessionStore) RotatedTo(ctx context.Context, oldToken string) (string, error) {
	v, err := s.rdb.Get(ctx, rotationPrefix+oldToken).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: 读取轮换映射: %w", err)
	}
	return v, nil
}

// SessionExpiryKey 是会话到期集合：分值是会话到期时刻（毫秒），成员见 ExpiryMember。
const SessionExpiryKey = "fp:sess:exp"

// ExpiryMember 是会话在到期集合里的成员 {app_id}:{user_id}:{session_id}:{client_ref}。
// 带全事件要用的字段：会话 key 被 TTL 清掉之后，扫描仍能只凭成员串发出 logged_out。
func ExpiryMember(sess *domain.Session) string {
	return sess.AppID.String() + ":" + sess.UserID.String() + ":" + sess.ID + ":" + sess.ClientRef
}

// ParseExpiryMember 拆开成员串。client_ref 在最后，本身含冒号也不影响。
func ParseExpiryMember(m string) (appID, userID uuid.UUID, sessionID, clientRef string, ok bool) {
	parts := strings.SplitN(m, ":", 4)
	if len(parts) != 4 {
		return uuid.Nil, uuid.Nil, "", "", false
	}
	a, err1 := uuid.Parse(parts[0])
	u, err2 := uuid.Parse(parts[1])
	if err1 != nil || err2 != nil || parts[2] == "" {
		return uuid.Nil, uuid.Nil, "", "", false
	}
	return a, u, parts[2], parts[3], true
}

// claimExpiredScript 原子领取分值 ≤ now 的至多 limit 个成员，并把它们的分值推到 lease：
// 并发的扫描器领不到同一批；领走的那个崩了，lease 到点后会被别人重新领到，事件不会丢。
var claimExpiredScript = redis.NewScript(`
local ms = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, tonumber(ARGV[2]))
for _, m in ipairs(ms) do
  redis.call('ZADD', KEYS[1], ARGV[3], m)
end
return ms`)

// ClaimExpired 领取到期时刻 ≤ now 的至多 limit 个成员，领走的分值改成 leaseUntil。
func (s *SessionStore) ClaimExpired(ctx context.Context, now int64, limit int, leaseUntil int64) ([]string, error) {
	ms, err := claimExpiredScript.Run(ctx, s.rdb, []string{SessionExpiryKey}, now, limit, leaseUntil).StringSlice()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("store: 领取到期会话: %w", err)
	}
	return ms, nil
}

// TrackExpiry 改已登记成员的到期时刻（会话其实延期了，或要提前复核）。只改不加：扫描复核期间会话被撤销、
// 成员已随会话摘掉的话，放回去就会在下一轮再报一次 expired。
func (s *SessionStore) TrackExpiry(ctx context.Context, member string, expiresAt int64) error {
	if err := s.rdb.ZAddXX(ctx, SessionExpiryKey, redis.Z{Score: float64(expiresAt), Member: member}).Err(); err != nil {
		return fmt.Errorf("store: 更新会话到期: %w", err)
	}
	return nil
}

// ReserveExpiry 新登记一个成员，用于登录在签发之前的预占（见 service.SessionService.ReservePending）。
func (s *SessionStore) ReserveExpiry(ctx context.Context, member string, expiresAt int64) error {
	if err := s.rdb.ZAdd(ctx, SessionExpiryKey, redis.Z{Score: float64(expiresAt), Member: member}).Err(); err != nil {
		return fmt.Errorf("store: 登记会话到期: %w", err)
	}
	return nil
}

// UntrackExpiry 从到期集合摘掉成员。
func (s *SessionStore) UntrackExpiry(ctx context.Context, member string) error {
	if err := s.rdb.ZRem(ctx, SessionExpiryKey, member).Err(); err != nil {
		return fmt.Errorf("store: 摘除会话到期: %w", err)
	}
	return nil
}

// ExpiryScore 是成员当前的分值，不在集合里时 ok 为假。
func (s *SessionStore) ExpiryScore(ctx context.Context, member string) (score int64, ok bool, err error) {
	v, err := s.rdb.ZScore(ctx, SessionExpiryKey, member).Result()
	if errors.Is(err, redis.Nil) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("store: 读取会话到期: %w", err)
	}
	return int64(v), true, nil
}

// FindSession 按会话 id 找该用户的会话，轮换过渡期内新旧 token 并存时取到期最晚的那个（新 token）。
// 找不到返回 domain.ErrNotFound。
func (s *SessionStore) FindSession(ctx context.Context, userID uuid.UUID, sessionID string) (*domain.Session, error) {
	tokens, err := s.rdb.SMembers(ctx, userSessionsPrefix+userID.String()).Result()
	if err != nil {
		return nil, fmt.Errorf("store: 读取用户会话索引: %w", err)
	}
	var best *domain.Session
	for _, batch := range chunkStrings(tokens, redisBatchSize) {
		keys := make([]string, len(batch))
		for i, tok := range batch {
			keys[i] = sessionKeyPrefix + tok
		}
		values, err := s.rdb.MGet(ctx, keys...).Result()
		if err != nil {
			return nil, fmt.Errorf("store: 批量读取会话: %w", err)
		}
		for _, v := range values {
			raw, ok := v.(string)
			if !ok {
				continue
			}
			var sess domain.Session
			if json.Unmarshal([]byte(raw), &sess) != nil || sess.ID != sessionID {
				continue
			}
			if best == nil || sess.IdleExpiresAt > best.IdleExpiresAt {
				best = &sess
			}
		}
	}
	if best == nil {
		return nil, domain.Failf(domain.ErrNotFound, domain.CodeSessionNotFound, "会话不存在或已过期")
	}
	return best, nil
}
