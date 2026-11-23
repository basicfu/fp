package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/basicfu/fp/internal/domain"
)

// UserProfile 是一个用户的全部个人信息。
//
// 用户全局互通、不与应用关联：任何应用都可以按 user_id 查询、修改任何用户，
// 所以这里的读写都不带应用参数，也没有"该用户是否在本应用登录过"的检查。
type UserProfile struct {
	User       domain.User
	Identities []domain.Identity
	// RegisteredAppID / RegisteredAppName 是注册来源应用的对外 app_id 与名称。
	// 管理员手动建的号、或来源应用已被删除时为空串。仅供展示与统计，不参与权限判断。
	RegisteredAppID   string
	RegisteredAppName string
	// AvatarURL 是头像的完整公开地址；没有头像、或头像还没迁到对象存储时为空串。
	AvatarURL string
}

// UserProfileUpdate 是一次资料修改。nil 的字段不改。
type UserProfileUpdate struct {
	Nickname *string
	Gender   *string
}

// GetProfile 按用户 ID 取个人信息。
func (s *UserService) GetProfile(ctx context.Context, id uuid.UUID) (*UserProfile, error) {
	u, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	ids, err := s.ListIdentities(ctx, id)
	if err != nil {
		return nil, err
	}
	p := &UserProfile{User: *u, Identities: ids}
	err = s.pool.QueryRow(ctx, `
		SELECT a.app_id, a.name FROM user_extra e
		JOIN application a ON a.id = e.application_id
		WHERE e.user_id = $1 AND e.is_registration`, id).Scan(&p.RegisteredAppID, &p.RegisteredAppName)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("service: 查询注册来源: %w", err)
	}
	var key string
	err = s.pool.QueryRow(ctx, `SELECT object_key FROM user_avatar WHERE user_id = $1`, id).Scan(&key)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("service: 查询头像对象: %w", err)
	}
	if key != "" && s.objects != nil {
		p.AvatarURL = s.objects.URL(key)
	}
	return p, nil
}

// UpdateProfile 修改昵称、性别，返回改完的完整资料。
func (s *UserService) UpdateProfile(ctx context.Context, id uuid.UUID, in UserProfileUpdate) (*UserProfile, error) {
	if in.Nickname != nil {
		nickname, err := domain.NormalizeNickname(*in.Nickname)
		if err != nil {
			return nil, err
		}
		in.Nickname = &nickname
	}
	if in.Gender != nil && !domain.ValidGender(*in.Gender) {
		return nil, domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument,
			"性别只能是 %s、%s 或留空", domain.GenderMale, domain.GenderFemale)
	}
	if in.Nickname != nil || in.Gender != nil {
		tag, err := s.pool.Exec(ctx, `
			UPDATE app_user
			SET nickname = coalesce($2::text, nickname), gender = coalesce($3::text, gender), updated_at = now()
			WHERE id = $1`, id, in.Nickname, in.Gender)
		if err != nil {
			return nil, fmt.Errorf("service: 更新用户资料: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return nil, domain.Failf(domain.ErrNotFound, domain.CodeUserNotFound, "用户不存在")
		}
	}
	return s.GetProfile(ctx, id)
}

// avatarCacheControl：key 每次上传都换，内容永不变化，可以永久缓存而不用刷 CDN。
const avatarCacheControl = "public, max-age=31536000, immutable"

// avatarExt 是头像对象 key 的扩展名，由 CheckAvatar 识别出的类型决定。
func avatarExt(contentType string) string {
	switch contentType {
	case "image/png":
		return "png"
	case "image/webp":
		return "webp"
	default:
		return "jpg"
	}
}

func newAvatarKey(contentType string) (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("service: 生成头像 key: %w", err)
	}
	return "avatar/" + id.String() + "." + avatarExt(contentType), nil
}

// UpdateAvatar 把 image 存为该用户的头像，返回改完的完整资料。image 为空表示清除头像。
//
// fp 对图片只存不处理：不解码、不裁剪、不压缩。规格由业务方在上传前自行处理。
//
// 先上传对象再开事务：对象存储没法进事务，反过来先提交再上传会留下"有记录没图片"。
// 上传前先把新 key 以较晚的期限登记进 object_gc，事务提交时同事务摘掉：上传成功而事务失败、
// 甚至进程在两步之间崩溃，后台任务到期也会把它删掉，不会成孤儿。旧 key 在同一事务里登记进
// object_gc，提交后由后台任务删除。
func (s *UserService) UpdateAvatar(ctx context.Context, id uuid.UUID, image []byte) (*UserProfile, error) {
	var hash, contentType, newKey string
	if len(image) > 0 {
		var err error
		if contentType, err = domain.CheckAvatar(image); err != nil {
			return nil, err
		}
		hash = domain.AvatarHash(image)
		if s.objects == nil {
			return nil, domain.Fail(domain.ErrInternal, domain.CodeObjectStoreUnavailable, "头像存储未配置")
		}
		if newKey, err = newAvatarKey(contentType); err != nil {
			return nil, err
		}
		if err := s.reserveObjectKey(ctx, newKey); err != nil {
			return nil, err
		}
		if err := s.objects.Put(ctx, newKey, contentType, image, avatarCacheControl); err != nil {
			return nil, fmt.Errorf("service: 上传头像: %w", err)
		}
	}

	if err := s.storeAvatar(ctx, id, hash, contentType, newKey, false); err != nil {
		if newKey != "" {
			// ctx 可能正是因取消才失败的，清理要用不随它取消的 ctx。
			if derr := s.objects.Delete(context.WithoutCancel(ctx), newKey); derr != nil {
				slog.Error("service: 头像入库失败后回收对象也失败，留下孤儿对象", "key", newKey, "err", derr)
			}
		}
		return nil, err
	}
	return s.GetProfile(ctx, id)
}

// errAvatarNotEmpty 是 storeAvatar 只补空时用户已有头像（或用户已不在）的结果，不是故障。
var errAvatarNotEmpty = errors.New("service: 用户已有头像")

// storeAvatar 在一个事务里写头像两张表。onlyIfEmpty 时只在 avatar_hash 仍为空时写，否则返回 errAvatarNotEmpty。
func (s *UserService) storeAvatar(ctx context.Context, id uuid.UUID, hash, contentType, newKey string, onlyIfEmpty bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("service: 开启事务: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// 先改 app_user：这一行的行锁让同一用户的并发换头像排队，两张表不会各留一方的结果。
	// 只补空的条件也放这条 UPDATE 里：等到行锁后 PG 会按提交后的新值重判 WHERE，
	// 和用户同时手动上传时，先提交的手动头像不会被补空覆盖。
	tag, err := tx.Exec(ctx,
		`UPDATE app_user SET avatar_hash = $2, updated_at = now() WHERE id = $1 AND (NOT $3 OR avatar_hash = '')`,
		id, hash, onlyIfEmpty)
	if err != nil {
		return fmt.Errorf("service: 更新头像哈希: %w", err)
	}
	if tag.RowsAffected() == 0 {
		if onlyIfEmpty {
			return errAvatarNotEmpty
		}
		return domain.Failf(domain.ErrNotFound, domain.CodeUserNotFound, "用户不存在")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO object_gc (key) SELECT object_key FROM user_avatar
		WHERE user_id = $1 AND object_key <> '' ON CONFLICT DO NOTHING`, id); err != nil {
		return fmt.Errorf("service: 登记旧头像待删除: %w", err)
	}
	if hash == "" {
		_, err = tx.Exec(ctx, `DELETE FROM user_avatar WHERE user_id = $1`, id)
	} else {
		// data 置空：图片只在对象存储里，PG 不再留一份。
		_, err = tx.Exec(ctx, `
			INSERT INTO user_avatar (user_id, hash, content_type, data, object_key) VALUES ($1, $2, $3, '', $4)
			ON CONFLICT (user_id) DO UPDATE
			SET hash = EXCLUDED.hash, content_type = EXCLUDED.content_type, data = EXCLUDED.data,
			    object_key = EXCLUDED.object_key, updated_at = now()`,
			id, hash, contentType, newKey)
	}
	if err != nil {
		return fmt.Errorf("service: 写入头像: %w", err)
	}
	if newKey != "" {
		if err := releaseObjectKey(ctx, tx, newKey); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("service: 提交事务: %w", err)
	}
	return nil
}

// HasObjectStore 报告是否配了头像所在的对象存储。
func (s *UserService) HasObjectStore() bool { return s.objects != nil }

// FillEmptyAvatar 用户还没有头像时把 image 存为头像，返回是否写入。已有头像、没配对象存储都不写，也不算错。
//
// 存储路径与 UpdateAvatar 相同（预登记 → 上传 → 事务入库）。没写进去时对象已经上传，
// 把预登记改成立即到期交给 object_gc 删，不在这里直接删：删除失败还有它重试。
func (s *UserService) FillEmptyAvatar(ctx context.Context, id uuid.UUID, image []byte) (bool, error) {
	if s.objects == nil {
		return false, nil
	}
	contentType, err := domain.CheckAvatar(image)
	if err != nil {
		return false, err
	}
	key, err := newAvatarKey(contentType)
	if err != nil {
		return false, err
	}
	if err := s.reserveObjectKey(ctx, key); err != nil {
		return false, err
	}
	err = s.objects.Put(ctx, key, contentType, image, avatarCacheControl)
	if err != nil {
		err = fmt.Errorf("service: 上传头像: %w", err)
	} else {
		err = s.storeAvatar(ctx, id, domain.AvatarHash(image), contentType, key, true)
	}
	if err == nil {
		return true, nil
	}
	if _, derr := s.pool.Exec(context.WithoutCancel(ctx), `UPDATE object_gc SET due_at = now() WHERE key = $1`, key); derr != nil {
		// 预登记还在，到期照样会删，只是晚一些。
		slog.Warn("service: 丢弃补空头像对象失败，等预登记到期再删", "key", key, "err", derr)
	}
	if errors.Is(err, errAvatarNotEmpty) {
		return false, nil
	}
	return false, err
}

// pendingObjectGrace 是上传前预登记的待删期限：远长于一次上传加入库，到点还没被摘掉说明入库没成。
const pendingObjectGrace = time.Hour

// reserveObjectKey 在上传之前把 key 以较晚的期限登记待删，见 UpdateAvatar。
func (s *UserService) reserveObjectKey(ctx context.Context, key string) error {
	if _, err := s.pool.Exec(ctx, `INSERT INTO object_gc (key, due_at) VALUES ($1, now() + make_interval(secs => $2))
		ON CONFLICT (key) DO NOTHING`, key, pendingObjectGrace.Seconds()); err != nil {
		return fmt.Errorf("service: 预登记头像对象: %w", err)
	}
	return nil
}

// releaseObjectKey 在引用 key 的事务里摘掉预登记，与引用一起提交。
func releaseObjectKey(ctx context.Context, tx pgx.Tx, key string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM object_gc WHERE key = $1`, key); err != nil {
		return fmt.Errorf("service: 摘除头像对象预登记: %w", err)
	}
	return nil
}

// AvatarURLByHash 按内容哈希取头像的公开地址，给老路径 GET /avatar/{hash} 做 302。
func (s *UserService) AvatarURLByHash(ctx context.Context, hash string) (string, error) {
	notFound := domain.Fail(domain.ErrNotFound, domain.CodeAvatarNotFound, "头像不存在")
	if !domain.IsAvatarHash(hash) {
		return "", notFound
	}
	var key string
	err := s.pool.QueryRow(ctx,
		`SELECT object_key FROM user_avatar WHERE hash = $1 AND object_key <> '' LIMIT 1`, hash).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", notFound
	}
	if err != nil {
		return "", fmt.Errorf("service: 查询头像地址: %w", err)
	}
	if s.objects == nil {
		return "", domain.Fail(domain.ErrInternal, domain.CodeObjectStoreUnavailable, "头像存储未配置")
	}
	return s.objects.URL(key), nil
}

// MigrateAvatars 把还在 PG 里的头像（object_key 为空、data 非空）搬进对象存储并回填 key，
// 返回搬了多少张。可重复执行：已有 key 的不再选中。data 不清，老路径与回滚还要用，收尾再单独删。
func (s *UserService) MigrateAvatars(ctx context.Context, batch int) (int, error) {
	if s.objects == nil {
		return 0, domain.Fail(domain.ErrInternal, domain.CodeObjectStoreUnavailable, "头像存储未配置")
	}
	if batch < 1 {
		batch = 100
	}
	total := 0
	for {
		rows, err := s.pool.Query(ctx, `
			SELECT user_id, hash, content_type, data FROM user_avatar
			WHERE object_key = '' AND length(data) > 0
			ORDER BY user_id LIMIT $1`, batch)
		if err != nil {
			return total, fmt.Errorf("service: 查询待迁移头像: %w", err)
		}
		type item struct {
			id       uuid.UUID
			hash, ct string
			data     []byte
		}
		var items []item
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.id, &it.hash, &it.ct, &it.data); err != nil {
				rows.Close()
				return total, fmt.Errorf("service: 扫描待迁移头像: %w", err)
			}
			items = append(items, it)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return total, fmt.Errorf("service: 遍历待迁移头像: %w", err)
		}
		if len(items) == 0 {
			return total, nil
		}
		for _, it := range items {
			key, err := newAvatarKey(it.ct)
			if err != nil {
				return total, err
			}
			if err := s.reserveObjectKey(ctx, key); err != nil {
				return total, err
			}
			if err := s.objects.Put(ctx, key, it.ct, it.data, avatarCacheControl); err != nil {
				return total, fmt.Errorf("service: 上传 %s 的头像: %w", it.id, err)
			}
			filled, err := s.fillObjectKey(ctx, it.id, it.hash, key)
			if err != nil {
				return total, err
			}
			if !filled {
				// 预登记留着，后台任务到期会再删一遍，删除是幂等的。
				_ = s.objects.Delete(ctx, key)
				continue
			}
			total++
		}
	}
}

// fillObjectKey 回填迁移上去的对象 key，与摘预登记同一事务。
// 带 hash 条件：迁移期间用户换了头像时这行已不是我们读到的那张，返回 false，调用方回收对象。
func (s *UserService) fillObjectKey(ctx context.Context, userID uuid.UUID, hash, key string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("service: 开启事务: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx,
		`UPDATE user_avatar SET object_key = $2 WHERE user_id = $1 AND object_key = '' AND hash = $3`, userID, key, hash)
	if err != nil {
		return false, fmt.Errorf("service: 回填头像 key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if err := releaseObjectKey(ctx, tx, key); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("service: 提交回填: %w", err)
	}
	return true, nil
}

// GetAvatar 按内容哈希取头像。
//
// 按哈希而不是按用户取：哈希即版本，同一个哈希的内容永不变化，业务方可以无限期缓存。
// 两个用户传了同一张图时各存一行，取哪行都一样。
func (s *UserService) GetAvatar(ctx context.Context, hash string) (contentType string, image []byte, err error) {
	notFound := domain.Fail(domain.ErrNotFound, domain.CodeAvatarNotFound, "头像不存在")
	if !domain.IsAvatarHash(hash) {
		return "", nil, notFound
	}
	err = s.pool.QueryRow(ctx,
		`SELECT content_type, data FROM user_avatar WHERE hash = $1 AND length(data) > 0 LIMIT 1`, hash).Scan(&contentType, &image)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, notFound
	}
	if err != nil {
		return "", nil, fmt.Errorf("service: 查询头像: %w", err)
	}
	return contentType, image, nil
}

// FillEmptyNickname 用户昵称为空时写入 nickname（须已规整），已有昵称不覆盖。返回写完的用户。
func (s *UserService) FillEmptyNickname(ctx context.Context, id uuid.UUID, nickname string) (*domain.User, error) {
	// 「为空才写」放进 WHERE：调用方读到空到这里写入之间，用户可能刚好自己改了昵称。
	if _, err := s.pool.Exec(ctx,
		`UPDATE app_user SET nickname = $2, updated_at = now() WHERE id = $1 AND nickname = ''`, id, nickname); err != nil {
		return nil, fmt.Errorf("service: 补昵称: %w", err)
	}
	return s.GetByID(ctx, id)
}

// thirdPartyNickname 把三方昵称规整成合法昵称：超长截断，含控制字符等规整不了的返回空串（不补）。
func thirdPartyNickname(raw string) string {
	r := []rune(strings.TrimSpace(raw))
	if len(r) > domain.NicknameMaxLen {
		r = r[:domain.NicknameMaxLen]
	}
	n, err := domain.NormalizeNickname(string(r))
	if err != nil {
		return ""
	}
	return n
}
