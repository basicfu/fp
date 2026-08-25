package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/basicfu/fp/internal/domain"
)

// minPasswordLength 是密码最小长度（按 rune 计）。第一阶段只做长度校验，
// 完整密码策略随 password connector 的配置项在后续阶段落地。
const minPasswordLength = 8

// maxPasswordBytes 是 bcrypt 的硬上限：超过 72 字节它直接返回 ErrPasswordTooLong。
// 必须在调用 bcrypt 之前自己拦住，否则那个错误会被包成普通 error、匹配不上任何
// 哨兵，最终变成 500——而触发它只需要一个 73 字符的密码管理器口令，或者约 25 个汉字。
const maxPasswordBytes = 72

const userColumns = `
	id, password_hash, nickname, avatar_url, gender, status,
	coalesce((extract(epoch from delete_submitted_at) * 1000)::bigint, 0),
	(extract(epoch from created_at) * 1000)::bigint,
	(extract(epoch from updated_at) * 1000)::bigint`

const identityColumns = `
	id, user_id, type, subject, union_key, credential,
	coalesce((extract(epoch from last_login_at) * 1000)::bigint, 0),
	(extract(epoch from created_at) * 1000)::bigint`

// UserService 管理用户、登录标识与应用注册关系。
type UserService struct {
	pool *pgxpool.Pool
}

// NewUserService 构造 UserService。
func NewUserService(pool *pgxpool.Pool) *UserService {
	return &UserService{pool: pool}
}

// EnsureIdentityInput 描述一次"确保某个登录标识存在"的请求。
type EnsureIdentityInput struct {
	Type    string
	Subject string
	// UnionKey 非空时参与跨 Type 归并（微信 unionId）。
	UnionKey string
	// Credential 存第三方 token 等。密码不走这里。
	Credential string
	// Nickname 仅在需要新建用户时用作初始昵称。
	Nickname string
}

func (in EnsureIdentityInput) validate() error {
	if in.Type == "" {
		return domain.Errorf(domain.ErrInvalidArgument, "identity 类型不能为空")
	}
	if in.Subject == "" {
		return domain.Errorf(domain.ErrInvalidArgument, "identity subject 不能为空")
	}
	return nil
}

// normalize 把 subject 规整成唯一形式。
//
// 归并靠的是 (type, subject) 的字面相等，所以"同一个人的两种写法"必须在**进入
// 归并之前**收敛成一个值，否则 Alice@Example.com 与 alice@example.com 会变成两个账号。
// 规整只能有一处实现：放在各 connector 里的话，两个 connector 规整方式不一致就会
// 破坏归并不变式。
//
// 手机号刻意不做规整：DetectIdentityType 只认光秃秃的 11 位号码，"+86..." 根本
// 不会被判成 phone。完整的 E.164 处理需要国际号码策略，属于后续阶段。
func (in EnsureIdentityInput) normalize() EnsureIdentityInput {
	switch in.Type {
	case domain.IdentityTypeEmail, domain.IdentityTypeUsername:
		in.Subject = strings.ToLower(strings.TrimSpace(in.Subject))
	default:
		in.Subject = strings.TrimSpace(in.Subject)
	}
	in.UnionKey = strings.TrimSpace(in.UnionKey)
	return in
}

// EnsureUserWithIdentity 按归并规则找到或创建用户，并确保该登录标识挂在其名下。
//
// 归并规则（设计文档 4.3）：
//  1. (type, subject) 已存在 → 直接复用其 user，不新建任何行
//  2. UnionKey 非空且已有同 union_key 的 identity → 复用该 user，新建一行 identity
//  3. 否则新建 user + identity
//
// 返回的 created 表示是否新建了用户。
func (s *UserService) EnsureUserWithIdentity(ctx context.Context, in EnsureIdentityInput) (*domain.User, *domain.Identity, bool, error) {
	if err := in.validate(); err != nil {
		return nil, nil, false, err
	}
	in = in.normalize()

	user, identity, created, err := s.ensureOnce(ctx, in)
	// 并发的"首次登录"会双双走到规则 3，其中一个撞上 UNIQUE (type, subject)。
	// 这不是冲突，是竞争——重试一次必然命中规则 1。不重试的话，用户在登录按钮上
	// 双击就会看到"已被其他账号占用"这种既错误又吓人的提示。
	if errors.Is(err, domain.ErrConflict) {
		return s.ensureOnce(ctx, in)
	}
	return user, identity, created, err
}

func (s *UserService) ensureOnce(ctx context.Context, in EnsureIdentityInput) (*domain.User, *domain.Identity, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, false, fmt.Errorf("service: 开启事务: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // 提交成功后 Rollback 是 no-op

	// 规则 1：标识已存在
	user, identity, err := findByIdentityTx(ctx, tx, in.Type, in.Subject)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, nil, false, fmt.Errorf("service: 提交事务: %w", err)
		}
		return user, identity, false, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return nil, nil, false, err
	}

	// 规则 2：unionKey 归并
	var userID uuid.UUID
	createdUser := false
	if in.UnionKey != "" {
		if err := lockUnionKeyTx(ctx, tx, in.UnionKey); err != nil {
			return nil, nil, false, err
		}
		err := tx.QueryRow(ctx,
			`SELECT user_id FROM identity WHERE union_key = $1 LIMIT 1`, in.UnionKey).Scan(&userID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, false, fmt.Errorf("service: 按 unionKey 查询: %w", err)
		}
	}

	// 规则 3：新建用户
	if userID == uuid.Nil {
		row := tx.QueryRow(ctx,
			`INSERT INTO app_user (nickname) VALUES ($1) RETURNING `+userColumns, in.Nickname)
		u, err := scanUser(row)
		if err != nil {
			return nil, nil, false, fmt.Errorf("service: 创建用户: %w", err)
		}
		userID = u.ID
		createdUser = true
	}

	newIdentity, err := insertIdentityTx(ctx, tx, userID, in)
	if err != nil {
		return nil, nil, false, err
	}

	finalUser, err := getUserTx(ctx, tx, userID)
	if err != nil {
		return nil, nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, false, fmt.Errorf("service: 提交事务: %w", err)
	}
	return finalUser, newIdentity, createdUser, nil
}

// AttachIdentity 给已存在的用户挂上一个新的登录标识。
// 该标识已被别的用户占用时返回 domain.ErrConflict。
func (s *UserService) AttachIdentity(ctx context.Context, userID uuid.UUID, in EnsureIdentityInput) (*domain.Identity, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("service: 开启事务: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// union_key 的不变式是"同一 union_key 下的所有 identity 指向同一个 user"。
	// 单列唯一索引表达不了它——同一 union_key 本来就允许多行（一个微信用户的
	// 公众号 openid 与小程序 openid 共享 unionId）——所以只能在这里守住。
	// EnsureUserWithIdentity 天然满足该不变式（它就是按 union_key 找用户），
	// AttachIdentity 是唯一能把它打破的入口。
	if in.UnionKey != "" {
		if err := lockUnionKeyTx(ctx, tx, in.UnionKey); err != nil {
			return nil, err
		}
		var owner uuid.UUID
		err := tx.QueryRow(ctx,
			`SELECT user_id FROM identity WHERE union_key = $1 LIMIT 1`, in.UnionKey).Scan(&owner)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("service: 按 unionKey 查询归属: %w", err)
		}
		if err == nil && owner != userID {
			return nil, domain.Errorf(domain.ErrConflict, "该 unionKey 已归属其他账号")
		}
	}

	id, err := insertIdentityTx(ctx, tx, userID, in)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("service: 提交事务: %w", err)
	}
	return id, nil
}

// FindByIdentity 按 (type, subject) 查用户与该标识。
func (s *UserService) FindByIdentity(ctx context.Context, identityType, subject string) (*domain.User, *domain.Identity, error) {
	return findByIdentityTx(ctx, s.pool, identityType, subject)
}

// FindByUnionKey 按 union_key 查用户。
func (s *UserService) FindByUnionKey(ctx context.Context, unionKey string) (*domain.User, error) {
	if unionKey == "" {
		return nil, domain.Errorf(domain.ErrInvalidArgument, "unionKey 不能为空")
	}
	var userID uuid.UUID
	err := s.pool.QueryRow(ctx,
		`SELECT user_id FROM identity WHERE union_key = $1 LIMIT 1`, unionKey).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Errorf(domain.ErrNotFound, "未找到该 unionKey 对应的用户")
	}
	if err != nil {
		return nil, fmt.Errorf("service: 按 unionKey 查询: %w", err)
	}
	return s.GetByID(ctx, userID)
}

// GetByID 按用户 ID 查用户。
func (s *UserService) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	return getUserTx(ctx, s.pool, id)
}

// ListIdentities 返回某个用户的全部登录标识。永不返回 nil 切片。
func (s *UserService) ListIdentities(ctx context.Context, userID uuid.UUID) ([]domain.Identity, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+identityColumns+` FROM identity WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("service: 查询 identity 列表: %w", err)
	}
	defer rows.Close()

	out := []domain.Identity{}
	for rows.Next() {
		id, err := scanIdentity(rows)
		if err != nil {
			return nil, fmt.Errorf("service: 扫描 identity: %w", err)
		}
		out = append(out, *id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service: 遍历 identity: %w", err)
	}
	return out, nil
}

// SetPassword 设置或重置用户密码。
func (s *UserService) SetPassword(ctx context.Context, userID uuid.UUID, plain string) error {
	if len([]rune(plain)) < minPasswordLength {
		return domain.Errorf(domain.ErrInvalidArgument, "密码长度不能少于 %d 位", minPasswordLength)
	}
	if len(plain) > maxPasswordBytes {
		return domain.Errorf(domain.ErrInvalidArgument,
			"密码过长（超过 %d 字节，约 %d 个汉字）", maxPasswordBytes, maxPasswordBytes/3)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return fmt.Errorf("service: 计算密码哈希: %w", err)
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE app_user SET password_hash = $2, updated_at = now() WHERE id = $1`, userID, string(hash))
	if err != nil {
		return fmt.Errorf("service: 更新密码: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.Errorf(domain.ErrNotFound, "用户不存在")
	}
	return nil
}

// VerifyPassword 校验用户密码。未设置密码的用户一律返回 ErrInvalidCredential。
func (s *UserService) VerifyPassword(ctx context.Context, userID uuid.UUID, plain string) error {
	var hash string
	err := s.pool.QueryRow(ctx, `SELECT password_hash FROM app_user WHERE id = $1`, userID).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Errorf(domain.ErrInvalidCredential, "账号或密码不正确")
	}
	if err != nil {
		return fmt.Errorf("service: 读取密码哈希: %w", err)
	}
	if hash == "" {
		// 未设置密码。bcrypt 对空哈希会直接报错，这里显式短路以保证错误一致。
		return domain.Errorf(domain.ErrInvalidCredential, "账号或密码不正确")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)); err != nil {
		return domain.Errorf(domain.ErrInvalidCredential, "账号或密码不正确")
	}
	return nil
}

// SetStatus 迁移用户状态，非法迁移返回 domain.ErrInvalidArgument。
func (s *UserService) SetStatus(ctx context.Context, userID uuid.UUID, status string) (*domain.User, error) {
	current, err := s.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if current.Status == status {
		return current, nil
	}
	if !domain.CanTransitionUserStatus(current.Status, status) {
		return nil, domain.Errorf(domain.ErrInvalidArgument,
			"不允许的状态迁移 %s → %s", current.Status, status)
	}

	// 进入注销保护期时记录提交时间；迁移到其他状态时清空，
	// 这样"保护期内登录撤销注销"之后不会残留一个误导性的时间戳。
	var row pgx.Row
	if status == domain.UserStatusPendingDelete {
		row = s.pool.QueryRow(ctx,
			`UPDATE app_user SET status = $2, delete_submitted_at = now(), updated_at = now()
			 WHERE id = $1 RETURNING `+userColumns, userID, status)
	} else {
		row = s.pool.QueryRow(ctx,
			`UPDATE app_user SET status = $2, delete_submitted_at = NULL, updated_at = now()
			 WHERE id = $1 RETURNING `+userColumns, userID, status)
	}
	u, err := scanUser(row)
	if err != nil {
		return nil, fmt.Errorf("service: 更新用户状态: %w", err)
	}
	return u, nil
}

// TouchIdentityLogin 记录该登录标识的最后使用时间。
func (s *UserService) TouchIdentityLogin(ctx context.Context, identityID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `UPDATE identity SET last_login_at = now() WHERE id = $1`, identityID)
	if err != nil {
		return fmt.Errorf("service: 更新 identity 登录时间: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.Errorf(domain.ErrNotFound, "identity 不存在")
	}
	return nil
}

// EnsureRegistration 确保用户在该应用下有注册关系。可重复调用。
//
// 注意：user_application 上没有也不允许有 role 列（设计文档 5.5）。
func (s *UserService) EnsureRegistration(ctx context.Context, userID, appID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_application (user_id, application_id)
		VALUES ($1, $2)
		ON CONFLICT (user_id, application_id) DO NOTHING`, userID, appID)
	if err != nil {
		return fmt.Errorf("service: 写入注册关系: %w", err)
	}
	return nil
}

// querier 抽象 pgxpool.Pool 与 pgx.Tx 的公共查询能力，让辅助函数在事务内外都能用。
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// lockUnionKeyTx 串行化对同一个 unionKey 的并发绑定。
//
// 为什么需要：pgx 默认 READ COMMITTED，而"某个 unionKey 还不存在"是一个
// **不存在的行**——普通 SELECT 锁不住不存在的东西，两个并发事务会双双读到零行、
// 双双插入，于是同一个微信用户裂成两个账号。这个不变式没法用单列唯一索引表达
// （同 unionKey 本来就允许多行），只能靠显式串行化。
//
// 事务级 advisory lock 随提交/回滚自动释放，不需要手动解锁。
// hashtext 有碰撞概率，代价仅仅是两个无关的 unionKey 偶尔互相等一下，无正确性影响。
func lockUnionKeyTx(ctx context.Context, tx pgx.Tx, unionKey string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, unionKey); err != nil {
		return fmt.Errorf("service: 锁定 unionKey: %w", err)
	}
	return nil
}

func findByIdentityTx(ctx context.Context, q querier, identityType, subject string) (*domain.User, *domain.Identity, error) {
	row := q.QueryRow(ctx,
		`SELECT `+identityColumns+` FROM identity WHERE type = $1 AND subject = $2`,
		identityType, subject)
	identity, err := scanIdentity(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, domain.Errorf(domain.ErrNotFound, "登录标识不存在")
	}
	if err != nil {
		return nil, nil, fmt.Errorf("service: 查询 identity: %w", err)
	}
	user, err := getUserTx(ctx, q, identity.UserID)
	if err != nil {
		return nil, nil, err
	}
	return user, identity, nil
}

func getUserTx(ctx context.Context, q querier, id uuid.UUID) (*domain.User, error) {
	row := q.QueryRow(ctx, `SELECT `+userColumns+` FROM app_user WHERE id = $1`, id)
	u, err := scanUser(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Errorf(domain.ErrNotFound, "用户不存在")
	}
	if err != nil {
		return nil, fmt.Errorf("service: 查询用户: %w", err)
	}
	return u, nil
}

func insertIdentityTx(ctx context.Context, q querier, userID uuid.UUID, in EnsureIdentityInput) (*domain.Identity, error) {
	row := q.QueryRow(ctx, `
		INSERT INTO identity (user_id, type, subject, union_key, credential)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+identityColumns,
		userID, in.Type, in.Subject, in.UnionKey, in.Credential)

	id, err := scanIdentity(row)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return nil, domain.Errorf(domain.ErrConflict,
				"登录标识 %s:%s 已被其他账号占用", in.Type, in.Subject)
		}
		return nil, fmt.Errorf("service: 创建 identity: %w", err)
	}
	return id, nil
}

func scanUser(r rowScanner) (*domain.User, error) {
	var u domain.User
	err := r.Scan(&u.ID, &u.PasswordHash, &u.Nickname, &u.AvatarURL, &u.Gender, &u.Status,
		&u.DeleteSubmittedAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func scanIdentity(r rowScanner) (*domain.Identity, error) {
	var i domain.Identity
	err := r.Scan(&i.ID, &i.UserID, &i.Type, &i.Subject, &i.UnionKey, &i.Credential,
		&i.LastLoginAt, &i.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &i, nil
}
