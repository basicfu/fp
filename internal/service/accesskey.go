package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/sdk/authzcore"
)

// AccessKeyService 管理访问密钥：控制台的增删改查，以及给业务方 SDK 的校验材料。
type AccessKeyService struct {
	pool *pgxpool.Pool
	// pub 推送 AccessKeyChanged。为 nil 时不推送。
	pub EventPublisher
	now func() time.Time
}

func NewAccessKeyService(pool *pgxpool.Pool, pub EventPublisher) *AccessKeyService {
	return NewAccessKeyServiceWithClock(pool, pub, time.Now)
}

func NewAccessKeyServiceWithClock(pool *pgxpool.Pool, pub EventPublisher, now func() time.Time) *AccessKeyService {
	return &AccessKeyService{pool: pool, pub: pub, now: now}
}

type CreateAccessKeyInput struct {
	Remark     string
	RoleKey    string // 空串表示不绑定
	ValidDays  int    // 0 表示永不过期
	AllowedIPs []string
}

// UpdateAccessKeyInput 里为 nil 的字段不修改。ValidDays 非 nil 时从现在起重新计算，0 表示永不过期。
type UpdateAccessKeyInput struct {
	Remark     *string
	RoleKey    *string
	ValidDays  *int
	AllowedIPs *[]string
}

// AccessKeyMaterial 是给业务方 SDK 的校验材料。
type AccessKeyMaterial struct {
	Key      domain.AccessKey
	CacheTTL time.Duration
}

// Create 新建一把 key。返回值带 SK，这是它唯一一次出现。
func (s *AccessKeyService) Create(ctx context.Context, in CreateAccessKeyInput) (*domain.AccessKey, error) {
	remark, err := checkRemark(in.Remark)
	if err != nil {
		return nil, err
	}
	roleKey, err := checkRoleKey(in.RoleKey)
	if err != nil {
		return nil, err
	}
	expires, err := s.expiresAt(in.ValidDays)
	if err != nil {
		return nil, err
	}
	ips, err := normalizeAllowedIPs(in.AllowedIPs)
	if err != nil {
		return nil, err
	}
	secret, err := randomToken()
	if err != nil {
		return nil, err
	}
	// AK 撞车的概率可以忽略，重试三次只是不让唯一约束冲突变成一次 500。
	for attempt := 0; attempt < 3; attempt++ {
		akID, err := newAccessKeyID()
		if err != nil {
			return nil, err
		}
		k, err := scanAccessKey(s.pool.QueryRow(ctx, `
			INSERT INTO access_key (access_key_id, secret, remark, role_key, allowed_ips, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING `+accessKeyColumns, akID, secret, remark, roleKey, ips, expires))
		switch {
		case isUniqueViolation(err):
			continue
		case isForeignKeyViolation(err):
			return nil, domain.Fail(domain.ErrNotFound, domain.CodeRoleNotFound, "角色不存在")
		case err != nil:
			return nil, fmt.Errorf("service: 创建访问密钥: %w", err)
		}
		return k, nil
	}
	return nil, errors.New("service: 连续生成的 AccessKey ID 都已存在")
}

// List 返回全部 key，roleKey 非空时只返回绑定该角色的。不含 SK。
func (s *AccessKeyService) List(ctx context.Context, roleKey string) ([]domain.AccessKey, error) {
	q, args := `SELECT `+accessKeyColumns+` FROM access_key`, []any{}
	if roleKey != "" {
		q += ` WHERE role_key = $1`
		args = append(args, roleKey)
	}
	rows, err := s.pool.Query(ctx, q+` ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("service: 查询访问密钥列表: %w", err)
	}
	defer rows.Close()
	out := []domain.AccessKey{}
	for rows.Next() {
		k, err := scanAccessKey(rows)
		if err != nil {
			return nil, fmt.Errorf("service: 扫描访问密钥: %w", err)
		}
		k.Secret = ""
		out = append(out, *k)
	}
	return out, rows.Err()
}

// Get 按主键取一把 key。不含 SK。
func (s *AccessKeyService) Get(ctx context.Context, id uuid.UUID) (*domain.AccessKey, error) {
	k, err := scanAccessKey(s.pool.QueryRow(ctx, `SELECT `+accessKeyColumns+` FROM access_key WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errAccessKeyNotFound()
	}
	if err != nil {
		return nil, fmt.Errorf("service: 查询访问密钥: %w", err)
	}
	k.Secret = ""
	return k, nil
}

// Update 只改给出的字段，成功后推送 AccessKeyChanged。
func (s *AccessKeyService) Update(ctx context.Context, id uuid.UUID, in UpdateAccessKeyInput) (*domain.AccessKey, error) {
	var sets []string
	args := []any{id}
	set := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if in.Remark != nil {
		r, err := checkRemark(*in.Remark)
		if err != nil {
			return nil, err
		}
		set("remark", r)
	}
	if in.RoleKey != nil {
		rk, err := checkRoleKey(*in.RoleKey)
		if err != nil {
			return nil, err
		}
		set("role_key", rk)
	}
	if in.ValidDays != nil {
		exp, err := s.expiresAt(*in.ValidDays)
		if err != nil {
			return nil, err
		}
		set("expires_at", exp)
	}
	if in.AllowedIPs != nil {
		ips, err := normalizeAllowedIPs(*in.AllowedIPs)
		if err != nil {
			return nil, err
		}
		set("allowed_ips", ips)
	}
	if len(sets) == 0 {
		return nil, domain.Fail(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "至少要提供一个要修改的字段")
	}
	k, err := scanAccessKey(s.pool.QueryRow(ctx,
		`UPDATE access_key SET `+strings.Join(sets, ", ")+`, updated_at = now() WHERE id = $1 RETURNING `+accessKeyColumns,
		args...))
	return s.afterWrite(ctx, k, err)
}

// SetStatus 启用或停用，成功后推送 AccessKeyChanged。
func (s *AccessKeyService) SetStatus(ctx context.Context, id uuid.UUID, status string) (*domain.AccessKey, error) {
	if status != domain.AccessKeyStatusActive && status != domain.AccessKeyStatusDisabled {
		return nil, domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "未知的状态 %q", status)
	}
	k, err := scanAccessKey(s.pool.QueryRow(ctx,
		`UPDATE access_key SET status = $2, updated_at = now() WHERE id = $1 RETURNING `+accessKeyColumns, id, status))
	return s.afterWrite(ctx, k, err)
}

// Delete 真删，成功后推送 AccessKeyChanged。
func (s *AccessKeyService) Delete(ctx context.Context, id uuid.UUID) error {
	var akID string
	err := s.pool.QueryRow(ctx, `DELETE FROM access_key WHERE id = $1 RETURNING access_key_id`, id).Scan(&akID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errAccessKeyNotFound()
	}
	if err != nil {
		return fmt.Errorf("service: 删除访问密钥: %w", err)
	}
	s.announce(ctx, akID)
	return nil
}

// Resolve 给业务方 SDK 取校验材料。不存在、已停用、已过期分别返回对应错误码，不返回 SK。
func (s *AccessKeyService) Resolve(ctx context.Context, app *domain.Application, accessKeyID string) (*AccessKeyMaterial, error) {
	k, err := scanAccessKey(s.pool.QueryRow(ctx,
		`SELECT `+accessKeyColumns+` FROM access_key WHERE access_key_id = $1`, accessKeyID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Fail(domain.ErrUnauthorized, domain.CodeAccessKeyInvalid, "AccessKey 无效")
	}
	if err != nil {
		return nil, fmt.Errorf("service: 查询访问密钥: %w", err)
	}
	now := s.now()
	switch k.State(now) {
	case domain.AccessKeyStateDisabled:
		return nil, domain.Fail(domain.ErrForbidden, domain.CodeAccessKeyDisabled, "AccessKey 已停用")
	case domain.AccessKeyStateExpired:
		return nil, domain.Fail(domain.ErrForbidden, domain.CodeAccessKeyExpired, "AccessKey 已过期")
	}
	// 与 token 共用应用的缓存时长；快到期时不超过剩余有效期。
	ttl := time.Duration(app.Session.TokenCacheTTLSeconds) * time.Second
	if k.ExpiresAt > 0 {
		ttl = min(ttl, time.UnixMilli(k.ExpiresAt).Sub(now))
	}
	return &AccessKeyMaterial{Key: *k, CacheTTL: max(ttl, 0)}, nil
}

// RecordUsage 写入最后使用时间：只写比库里新的，晚于当前时间的按当前时间记。
func (s *AccessKeyService) RecordUsage(ctx context.Context, usages map[string]time.Time) error {
	if len(usages) == 0 {
		return nil
	}
	now := s.now()
	ids := make([]string, 0, len(usages))
	times := make([]time.Time, 0, len(usages))
	for id, t := range usages {
		ids = append(ids, id)
		times = append(times, minTime(t, now))
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE access_key AS k SET last_used_at = u.t
		FROM unnest($1::text[], $2::timestamptz[]) AS u(id, t)
		WHERE k.access_key_id = u.id AND (k.last_used_at IS NULL OR k.last_used_at < u.t)`,
		ids, times); err != nil {
		return fmt.Errorf("service: 记录访问密钥使用时间: %w", err)
	}
	return nil
}

func (s *AccessKeyService) afterWrite(ctx context.Context, k *domain.AccessKey, err error) (*domain.AccessKey, error) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, errAccessKeyNotFound()
	case isForeignKeyViolation(err):
		return nil, domain.Fail(domain.ErrNotFound, domain.CodeRoleNotFound, "角色不存在")
	case err != nil:
		return nil, fmt.Errorf("service: 更新访问密钥: %w", err)
	}
	s.announce(ctx, k.AccessKeyID)
	k.Secret = ""
	return k, nil
}

// announce 让所有应用的 SDK 丢掉这把 key 的缓存。新建不需要推：还没有任何缓存。
func (s *AccessKeyService) announce(ctx context.Context, accessKeyID string) {
	publishEvent(ctx, s.pub, domain.RevokeEvent{
		Kind: domain.EventKindAccessKeyChanged, AccessKeyID: accessKeyID, AppID: uuid.Nil, At: s.now().UnixMilli(),
	})
}

const accessKeyIDAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// newAccessKeyID 生成 FPAK + 20 位 [A-Z0-9]。拒绝采样保证每个字符等概率。
func newAccessKeyID() (string, error) {
	out := []byte("FPAK")
	var buf [32]byte
	for len(out) < 24 {
		if _, err := rand.Read(buf[:]); err != nil {
			return "", fmt.Errorf("service: 生成 AccessKey ID: %w", err)
		}
		for _, b := range buf {
			if b >= 252 { // 252 = 36 × 7，丢掉尾部才能均匀取模
				continue
			}
			out = append(out, accessKeyIDAlphabet[b%36])
			if len(out) == 24 {
				break
			}
		}
	}
	return string(out), nil
}

func checkRemark(remark string) (string, error) {
	r := strings.TrimSpace(remark)
	if r == "" {
		return "", domain.Fail(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "备注不能为空").WithField("field", "remark")
	}
	return r, nil
}

// checkRoleKey 空串返回 nil（写 NULL）。角色是否存在由外键判断。
func checkRoleKey(roleKey string) (*string, error) {
	if roleKey == "" {
		return nil, nil
	}
	if roleKey == authzcore.GuestRoleKey {
		return nil, domain.Fail(domain.ErrInvalidArgument, domain.CodeRoleBuiltin, "访问密钥不能绑定 GUEST")
	}
	return &roleKey, nil
}

func (s *AccessKeyService) expiresAt(days int) (*time.Time, error) {
	if days < 0 {
		return nil, domain.Fail(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "有效期天数不能为负").WithField("field", "validDays")
	}
	if days == 0 {
		return nil, nil
	}
	t := s.now().Add(time.Duration(days) * 24 * time.Hour)
	return &t, nil
}

// normalizeAllowedIPs 解析 IP 或网段：单个 IP 存为 /32 或 /128，网段去掉主机位，空行跳过。
func normalizeAllowedIPs(in []string) ([]netip.Prefix, error) {
	out := []netip.Prefix{}
	for i, raw := range in {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		bad := domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument,
			"IP 白名单第 %d 行 %q 不是合法的 IP 或网段", i+1, raw).WithField("field", "allowedIps")
		if strings.Contains(s, "/") {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				return nil, bad
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, bad
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	if len(out) > domain.MaxAllowedIPs {
		return nil, domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument,
			"IP 白名单最多 %d 条", domain.MaxAllowedIPs).WithField("field", "allowedIps")
	}
	return out, nil
}

func minTime(a, b time.Time) time.Time {
	if a.After(b) {
		return b
	}
	return a
}

func errAccessKeyNotFound() error {
	return domain.Fail(domain.ErrNotFound, domain.CodeAccessKeyNotFound, "访问密钥不存在")
}

const accessKeyColumns = `id, access_key_id, secret, remark, coalesce(role_key, ''), allowed_ips, status,
	coalesce((extract(epoch from expires_at) * 1000)::bigint, 0),
	coalesce((extract(epoch from last_used_at) * 1000)::bigint, 0),
	(extract(epoch from created_at) * 1000)::bigint,
	(extract(epoch from updated_at) * 1000)::bigint`

func scanAccessKey(row rowScanner) (*domain.AccessKey, error) {
	var k domain.AccessKey
	if err := row.Scan(&k.ID, &k.AccessKeyID, &k.Secret, &k.Remark, &k.RoleKey, &k.AllowedIPs, &k.Status,
		&k.ExpiresAt, &k.LastUsedAt, &k.CreatedAt, &k.UpdatedAt); err != nil {
		return nil, err
	}
	return &k, nil
}
