// Package service 承载 fp 的业务逻辑。本包不依赖任何传输层类型。
package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

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

// bcryptCost 是密码哈希代价。10 是 bcrypt 的常用生产取值。
const bcryptCost = 10

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

// EnsureBootstrap 在管理员不存在时创建引导管理员。
// username 或 password 为空时静默跳过；管理员已存在时不改写其密码。
func (s *AdminService) EnsureBootstrap(ctx context.Context, username, password string) error {
	if username == "" || password == "" {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return fmt.Errorf("service: 计算管理员密码哈希: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO admin (username, password_hash, display_name)
		VALUES ($1, $2, $1)
		ON CONFLICT (username) DO NOTHING`, username, string(hash))
	if err != nil {
		return fmt.Errorf("service: 创建引导管理员: %w", err)
	}
	return nil
}

// Login 校验用户名密码并签发一个管理端会话 token。
func (s *AdminService) Login(ctx context.Context, username, password string) (string, error) {
	var (
		id     uuid.UUID
		hash   string
		status string
	)
	err := s.pool.QueryRow(ctx,
		`SELECT id, password_hash, status FROM admin WHERE username = $1`, username).
		Scan(&id, &hash, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		// 与密码错误返回同一错误，避免用户名枚举。
		return "", domain.Errorf(domain.ErrInvalidCredential, "用户名或密码不正确")
	}
	if err != nil {
		return "", fmt.Errorf("service: 查询管理员: %w", err)
	}
	if status != "ACTIVE" {
		return "", domain.Errorf(domain.ErrForbidden, "管理员账号已停用")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return "", domain.Errorf(domain.ErrInvalidCredential, "用户名或密码不正确")
	}

	token, err := randomToken()
	if err != nil {
		return "", err
	}
	payload := id.String() + "|" + username
	if err := s.rdb.Set(ctx, adminTokenPrefix+token, payload, adminSessionTTL).Err(); err != nil {
		return "", fmt.Errorf("service: 写入管理端会话: %w", err)
	}
	return token, nil
}

// Authenticate 校验管理端 token，返回管理员 ID 与用户名，并顺延会话有效期。
func (s *AdminService) Authenticate(ctx context.Context, token string) (uuid.UUID, string, error) {
	if token == "" {
		return uuid.Nil, "", domain.Errorf(domain.ErrUnauthorized, "缺少管理端凭据")
	}
	key := adminTokenPrefix + token
	payload, err := s.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return uuid.Nil, "", domain.Errorf(domain.ErrUnauthorized, "管理端凭据无效或已过期")
	}
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("service: 读取管理端会话: %w", err)
	}

	idStr, username, ok := strings.Cut(payload, "|")
	if !ok {
		return uuid.Nil, "", domain.Errorf(domain.ErrUnauthorized, "管理端凭据损坏")
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return uuid.Nil, "", domain.Errorf(domain.ErrUnauthorized, "管理端凭据损坏")
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

// randomToken 生成 32 字节的密码学随机 token，base64url 编码后为 43 字符。
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("service: 生成随机 token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
