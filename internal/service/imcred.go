package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/basicfu/fp/internal/domain"
)

// IMCredentialService 管理 fp-im 网关用来连 fp 的那份凭据。
//
// 全库只有一条（im_credential 的 CHECK (id = 1) 保证），因为 fp-im 是一个
// 服务而不是一群应用：多实例 fp-im 共用同一份凭据。
//
// 与 application.app_secret_hash 同一纪律：只存 bcrypt 哈希，明文只在
// Rotate 时返回一次，之后无法读回。
type IMCredentialService struct {
	pool *pgxpool.Pool
}

func NewIMCredentialService(pool *pgxpool.Pool) *IMCredentialService {
	return &IMCredentialService{pool: pool}
}

// Rotate 生成一份新的 IM secret，落哈希，返回仅此一次可见的明文。
//
// 用 upsert 而不是先查后写：单行表上的并发轮换靠主键冲突收敛，不需要额外
// 的事务或锁。两个人同时点「重新生成」时后写的那份胜出，双方各自拿到自己
// 生成的明文——只有胜出的那份能用。这是可接受的：轮换是低频的人工操作，
// 而失败的那一方下次校验时就会发现自己那份不通。
func (s *IMCredentialService) Rotate(ctx context.Context) (string, error) {
	secret, err := randomToken()
	if err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(secret), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("service: 计算 IM secret 哈希: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO im_credential (id, secret_hash, updated_at) VALUES (1, $1, now())
		ON CONFLICT (id) DO UPDATE SET secret_hash = EXCLUDED.secret_hash, updated_at = now()`,
		string(hash)); err != nil {
		return "", fmt.Errorf("service: 写入 IM 凭据: %w", err)
	}
	return secret, nil
}

// Verify 校验 IM secret。
//
// 「还没生成过」与「secret 不对」返回同一个错误：前者是运维顺序问题
// （fp-im 先配好了而 fp 这边还没生成），不是内部故障。报成内部错误会让人
// 去查日志和数据库连接，而正确的动作是去控制台点一下「生成」。
func (s *IMCredentialService) Verify(ctx context.Context, plainSecret string) error {
	if plainSecret == "" {
		return domain.Failf(domain.ErrInvalidCredential, domain.CodeAppCredentialInvalid, "IM 凭据无效")
	}
	var hash string
	err := s.pool.QueryRow(ctx, `SELECT secret_hash FROM im_credential WHERE id = 1`).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Failf(domain.ErrInvalidCredential, domain.CodeAppCredentialInvalid, "IM 凭据无效")
	}
	if err != nil {
		return fmt.Errorf("service: 读取 IM 凭据哈希: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plainSecret)); err != nil {
		return domain.Failf(domain.ErrInvalidCredential, domain.CodeAppCredentialInvalid, "IM 凭据无效")
	}
	return nil
}

// Exists 报告是否已经生成过 IM 凭据，供控制台展示状态。
// 只回答有没有，绝不回显任何与 secret 有关的东西——库里也只有哈希。
func (s *IMCredentialService) Exists(ctx context.Context) (bool, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*)::int FROM im_credential WHERE id = 1`).Scan(&n); err != nil {
		return false, fmt.Errorf("service: 查询 IM 凭据: %w", err)
	}
	return n > 0, nil
}
