// Package service 承载 fp 的业务逻辑。本包不依赖任何传输层类型。
package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	"github.com/basicfu/fp/internal/domain"
)

// adminSessionTTL 是平台管理员会话的空闲有效期。
// 管理端是高权限入口，窗口刻意设得比业务侧短。
const adminSessionTTL = 2 * time.Hour

const adminTokenPrefix = "fp:admin:tok:"

// adminEpochKey 是管理端会话的全局纪元。改账号与重置密码时 INCR；token payload
// 带着签发时的纪元，Authenticate 比对不一致即失效。不给 token 按管理员建索引、
// 也不 SCAN：共享 Redis 里还有终端用户会话，扫前缀要遍历整个键空间。
const adminEpochKey = "fp:admin:epoch"

// bcryptCost 是密码哈希代价。10 是 bcrypt 的常用生产取值。
const bcryptCost = 10

const (
	// DefaultAdminUsername / DefaultAdminPassword 是空库首次启动时的内置账号，
	// 登录响应据此判断"仍在使用默认密码"。
	DefaultAdminUsername = "admin"
	DefaultAdminPassword = "admin"

	maxAdminUsernameRunes = 64
	generatedPasswordLen  = 16
	// 去掉易混字符 0 O 1 l I：口述、抄写时不会认错。
	generatedPasswordAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

// AdminService 管理平台管理员账号与管理端会话。
// 管理员与业务用户使用完全独立的表，不共享任何数据（设计文档 12.1）。
type AdminService struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
}

// NewAdminService 构造 AdminService。
func NewAdminService(pool *pgxpool.Pool, rdb *redis.Client) *AdminService {
	return &AdminService{pool: pool, rdb: rdb}
}

// EnsureBootstrap 在 admin 表为空时创建引导管理员。
// username 或 password 为空时静默跳过；表里已有任何管理员时什么都不做。
//
// 判断依据必须是"表为空"而不是"用户名冲突"：登录名可改之后，管理员把 admin
// 改成 root，重启时 admin 这个名字不再冲突，按冲突判断会再造出一个已知密码的账号。
func (s *AdminService) EnsureBootstrap(ctx context.Context, username, password string) error {
	if username == "" || password == "" {
		return nil
	}
	// 同 UserService.SetPassword 的理由：bcrypt 超过 72 字节直接报错，
	// 不拦住就会在启动时炸出一个不知所云的 bcrypt 错误。
	if len(password) > maxPasswordBytes {
		return domain.Failf(domain.ErrInvalidArgument, domain.CodePasswordTooLong,
			"引导管理员密码过长（超过 %d 字节，约 %d 个汉字）", maxPasswordBytes, maxPasswordBytes/3)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return fmt.Errorf("service: 计算管理员密码哈希: %w", err)
	}
	// 末尾的 ON CONFLICT 兜住两个实例同时首次启动的竞态。
	_, err = s.pool.Exec(ctx, `
		INSERT INTO admin (username, password_hash, display_name)
		SELECT $1::text, $2::text, $1::text WHERE NOT EXISTS (SELECT 1 FROM admin)
		ON CONFLICT (username) DO NOTHING`, username, string(hash))
	if err != nil {
		return fmt.Errorf("service: 创建引导管理员: %w", err)
	}
	return nil
}

// Login 校验用户名密码并签发一个管理端会话 token。
func (s *AdminService) Login(ctx context.Context, username, password string) (string, error) {
	// 纪元必须在查库之前读，不能挪到校验之后。改账号与重置是"先提交库、再 INCR"：
	// 这里读到新纪元，说明下面的 SELECT 一定看得见新哈希，旧密码过不了；读到旧纪元，
	// 签出的 token 在 INCR 落地时失配。若校验完再读，bcrypt 那几十毫秒里完成的改密
	// 会让旧密码的登录领到新纪元的 token，而 Authenticate 每次访问都会顺延它。
	epoch, err := s.currentEpoch(ctx)
	if err != nil {
		return "", err
	}

	var (
		id     uuid.UUID
		hash   string
		status string
	)
	err = s.pool.QueryRow(ctx,
		`SELECT id, password_hash, status FROM admin WHERE username = $1`, username).
		Scan(&id, &hash, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		// 与密码错误返回同一错误，避免用户名枚举。
		return "", domain.Failf(domain.ErrInvalidCredential, domain.CodeAdminCredentialInvalid, "用户名或密码不正确")
	}
	if err != nil {
		return "", fmt.Errorf("service: 查询管理员: %w", err)
	}
	if status != "ACTIVE" {
		return "", domain.Failf(domain.ErrForbidden, domain.CodeAdminDisabled, "管理员账号已停用")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return "", domain.Failf(domain.ErrInvalidCredential, domain.CodeAdminCredentialInvalid, "用户名或密码不正确")
	}
	return s.issueToken(ctx, id, username, epoch)
}

// issueToken 签发一个带指定纪元的会话 token。纪元由调用方传入、这里不读：
// 登录要用查库之前读到的值，改账号要用自己那次 INCR 的返回值，
// 签发时再读一遍就把这两处刻意安排的先后冲掉了。
func (s *AdminService) issueToken(ctx context.Context, id uuid.UUID, username string, epoch int64) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	payload := id.String() + "|" + username + "|" + strconv.FormatInt(epoch, 10)
	if err := s.rdb.Set(ctx, adminTokenPrefix+token, payload, adminSessionTTL).Err(); err != nil {
		return "", fmt.Errorf("service: 写入管理端会话: %w", err)
	}
	return token, nil
}

// Authenticate 校验管理端 token，返回管理员 ID 与用户名，并顺延会话有效期。
func (s *AdminService) Authenticate(ctx context.Context, token string) (uuid.UUID, string, error) {
	invalid := func() (uuid.UUID, string, error) {
		return uuid.Nil, "", domain.Failf(domain.ErrUnauthorized, domain.CodeAdminSessionInvalid, "管理端登录已过期，请重新登录")
	}
	if token == "" {
		return invalid()
	}
	key := adminTokenPrefix + token
	payload, err := s.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return invalid()
	}
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("service: 读取管理端会话: %w", err)
	}

	idStr, rest, ok := strings.Cut(payload, "|")
	if !ok {
		return invalid()
	}
	// 登录名可以含 "|"，所以纪元从最后一个 "|" 切。升级前签发的旧格式
	// （id|username，没有纪元段）切不出来，按失效处理，管理员重新登录一次。
	i := strings.LastIndex(rest, "|")
	if i < 0 {
		return invalid()
	}
	username := rest[:i]
	tokenEpoch, err := strconv.ParseInt(rest[i+1:], 10, 64)
	if err != nil {
		return invalid()
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return invalid()
	}
	epoch, err := s.currentEpoch(ctx)
	if err != nil {
		return uuid.Nil, "", err
	}
	if tokenEpoch != epoch {
		return invalid()
	}

	// 管理端会话数量极少，每次访问直接顺延，无需降频。
	if err := s.rdb.Expire(ctx, key, adminSessionTTL).Err(); err != nil {
		return uuid.Nil, "", fmt.Errorf("service: 顺延管理端会话: %w", err)
	}
	return id, username, nil
}

// Logout 作废一个管理端 token。token 不存在时也返回 nil。
func (s *AdminService) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if err := s.rdb.Del(ctx, adminTokenPrefix+token).Err(); err != nil {
		return fmt.Errorf("service: 删除管理端会话: %w", err)
	}
	return nil
}

// ChangeAccountInput 是改账号的入参。NewPassword 为空表示不改密码。
type ChangeAccountInput struct {
	Username    string
	OldPassword string
	NewPassword string
}

// ChangeAccount 修改登录名与（可选的）密码。旧密码必须匹配。
//
// 成功后作废全部管理端会话（含发起修改的这一个），并为调用方重新签发一个
// 带新纪元与新登录名的 token——其他浏览器被踢下线，当前浏览器保持登录。
func (s *AdminService) ChangeAccount(ctx context.Context, id uuid.UUID, in ChangeAccountInput) (token, username string, err error) {
	username = strings.TrimSpace(in.Username)
	if n := utf8.RuneCountInString(username); n < 1 || n > maxAdminUsernameRunes {
		return "", "", domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument,
			"登录名需为 1–%d 个字符", maxAdminUsernameRunes)
	}
	if len(in.NewPassword) > maxPasswordBytes {
		return "", "", domain.Failf(domain.ErrInvalidArgument, domain.CodePasswordTooLong,
			"新密码过长（超过 %d 字节，约 %d 个汉字）", maxPasswordBytes, maxPasswordBytes/3)
	}

	var hash string
	err = s.pool.QueryRow(ctx, `SELECT password_hash FROM admin WHERE id = $1`, id).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", domain.Failf(domain.ErrUnauthorized, domain.CodeAdminSessionInvalid, "管理端登录已过期，请重新登录")
	}
	if err != nil {
		return "", "", fmt.Errorf("service: 查询管理员: %w", err)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.OldPassword)) != nil {
		return "", "", domain.Fail(domain.ErrInvalidArgument, domain.CodeAdminOldPasswordWrong, "旧密码不正确")
	}

	newHash := hash
	if in.NewPassword != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(in.NewPassword), bcryptCost)
		if err != nil {
			return "", "", fmt.Errorf("service: 计算管理员密码哈希: %w", err)
		}
		newHash = string(h)
	}
	// 条件里带开头读到的哈希：从校验旧密码到这里隔着一次 bcrypt，其间另一次改密或
	// 重置若已提交，无条件的 UPDATE 会用旧数据把它盖回去（只改登录名时写回的就是
	// 旧哈希本身）。bcrypt 每次加盐，哈希相同即"这期间没人动过密码"。
	tag, err := s.pool.Exec(ctx, `
		UPDATE admin SET username = $2, password_hash = $3, display_name = $2, updated_at = now()
		WHERE id = $1 AND password_hash = $4`, id, username, newHash, hash)
	if isUniqueViolation(err) {
		return "", "", domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "登录名已被占用")
	}
	if err != nil {
		return "", "", fmt.Errorf("service: 更新管理员: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// 刚校验过的凭据已被换掉：对现在的账号而言，旧密码不再成立。
		return "", "", domain.Fail(domain.ErrInvalidArgument, domain.CodeAdminOldPasswordWrong, "旧密码不正确")
	}
	// 先提交、后 INCR，顺序见 bumpEpoch。用本次 INCR 的返回值签发，不再读一遍计数器：
	// 读回来的可能已是别处新一轮 INCR 的结果，会把这个会话错放进新纪元。
	epoch, err := s.bumpEpoch(ctx)
	if err != nil {
		return "", "", err
	}
	token, err = s.issueToken(ctx, id, username, epoch)
	if err != nil {
		return "", "", err
	}
	return token, username, nil
}

// ResetPassword 把唯一的管理员恢复成用户名 admin + 随机密码，并作废全部会话。
// 表为空时创建。供 `fp reset-password` 使用：这是唯一的重置路径，不依赖通知渠道。
//
// 返回的明文密码只有这一次，库里只存 bcrypt 哈希。
func (s *AdminService) ResetPassword(ctx context.Context) (username, password string, err error) {
	password, err = randomPassword(generatedPasswordLen)
	if err != nil {
		return "", "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", "", fmt.Errorf("service: 计算管理员密码哈希: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", "", fmt.Errorf("service: 开启事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM admin ORDER BY created_at, id LIMIT 1 FOR UPDATE`).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		_, err = tx.Exec(ctx, `
			INSERT INTO admin (username, password_hash, display_name) VALUES ($1, $2, $1)`,
			DefaultAdminUsername, string(hash))
	case err == nil:
		_, err = tx.Exec(ctx, `
			UPDATE admin SET username = $2, password_hash = $3, display_name = $2,
			                 status = 'ACTIVE', updated_at = now()
			WHERE id = $1`, id, DefaultAdminUsername, string(hash))
	}
	if err != nil {
		return "", "", fmt.Errorf("service: 重置管理员: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", "", fmt.Errorf("service: 提交重置: %w", err)
	}
	// 先提交、后 INCR，顺序见 bumpEpoch。
	if _, err := s.bumpEpoch(ctx); err != nil {
		return "", "", err
	}
	return DefaultAdminUsername, password, nil
}

func (s *AdminService) currentEpoch(ctx context.Context) (int64, error) {
	v, err := s.rdb.Get(ctx, adminEpochKey).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("service: 读取管理端会话纪元: %w", err)
	}
	return v, nil
}

// bumpEpoch 递增纪元以作废全部管理端会话，返回递增后的值。必须在数据库写入提交
// 之后调用：INCR 若先于提交，读到新纪元的登录仍可能读到旧哈希（READ COMMITTED），
// Login "先读纪元再查库"的栅栏就破了。
func (s *AdminService) bumpEpoch(ctx context.Context) (int64, error) {
	v, err := s.rdb.Incr(ctx, adminEpochKey).Result()
	if err != nil {
		return 0, fmt.Errorf("service: 作废管理端会话: %w", err)
	}
	return v, nil
}

// randomToken 生成 32 字节的密码学随机 token，base64url 编码后为 43 字符。
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("service: 生成随机 token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// randomPassword 从去掉易混字符的字母表里取 n 位密码学随机字符。
func randomPassword(n int) (string, error) {
	max := big.NewInt(int64(len(generatedPasswordAlphabet)))
	b := make([]byte, n)
	for i := range b {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("service: 生成随机密码: %w", err)
		}
		b[i] = generatedPasswordAlphabet[idx.Int64()]
	}
	return string(b), nil
}
