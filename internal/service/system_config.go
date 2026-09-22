// Package service 的系统配置部分：fp 自身启动配置的版本化存取，形状是
// ConfigService 去掉 application_id/type/push 三个维度之后的样子——系统
// 配置只有 fp 自己一个消费者，不需要分区；只在进程启动时读一次，不广播。
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/domain"
)

// SystemConfigMaxVersions 是保留的版本数上限，超出的从最老的开始删。
// 与 ConfigMaxVersions 同一条纪律，各自独立维护：两张表的主键结构不同
// （(application_id, type, seq) vs (seq)），修剪 SQL 本来就不是一份。
const SystemConfigMaxVersions = 100

// SystemConfigService 管理 fp 自身启动配置的版本快照。
type SystemConfigService struct {
	pool *pgxpool.Pool
}

// NewSystemConfigService 构造 SystemConfigService。
func NewSystemConfigService(pool *pgxpool.Pool) *SystemConfigService {
	return &SystemConfigService{pool: pool}
}

// Current 返回当前版本。一个版本都没有时返回 Seq=0、空 Value，不是
// 错误——首次启动前系统配置表必然是空的，这是正常状态。
func (s *SystemConfigService) Current(ctx context.Context) (domain.SystemConfig, error) {
	seq, value, createdAt, err := s.scanOne(ctx, `
		SELECT seq, value, (extract(epoch FROM created_at) * 1000)::bigint
		FROM system_config ORDER BY seq DESC LIMIT 1`)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SystemConfig{Seq: 0, Value: ""}, nil
	}
	if err != nil {
		return domain.SystemConfig{}, err
	}
	return domain.SystemConfig{Seq: seq, Value: value, CreatedAt: createdAt}, nil
}

// Version 返回指定版本。不存在返回 domain.ErrNotFound 的包装。
func (s *SystemConfigService) Version(ctx context.Context, seq int64) (domain.SystemConfig, error) {
	retSeq, value, createdAt, err := s.scanOne(ctx, `
		SELECT seq, value, (extract(epoch FROM created_at) * 1000)::bigint
		FROM system_config WHERE seq = $1`, seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SystemConfig{}, domain.Fail(domain.ErrNotFound, domain.CodeConfigVersionNotFound, "配置版本不存在").
			WithDesc("系统配置没有第 %d 版", seq)
	}
	if err != nil {
		return domain.SystemConfig{}, err
	}
	return domain.SystemConfig{Seq: retSeq, Value: value, CreatedAt: createdAt}, nil
}

func (s *SystemConfigService) scanOne(ctx context.Context, sql string, args ...any) (int64, string, int64, error) {
	var (
		seq       int64
		value     string
		createdAt int64
	)
	if err := s.pool.QueryRow(ctx, sql, args...).Scan(&seq, &value, &createdAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, "", 0, err
		}
		return 0, "", 0, fmt.Errorf("service: 查询系统配置: %w", err)
	}
	return seq, value, createdAt, nil
}

// ListVersions 返回最近 limit 个版本的元信息。元素的 Value 恒为空字符串——
// 列表页只需要 seq 与时间。
func (s *SystemConfigService) ListVersions(ctx context.Context, limit int) ([]domain.SystemConfig, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		SELECT seq, (extract(epoch FROM created_at) * 1000)::bigint
		FROM system_config ORDER BY seq DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("service: 查询系统配置版本列表: %w", err)
	}
	defer rows.Close()

	out := make([]domain.SystemConfig, 0, limit)
	for rows.Next() {
		var c domain.SystemConfig
		if err := rows.Scan(&c.Seq, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("service: 扫描系统配置版本: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service: 遍历系统配置版本: %w", err)
	}
	return out, nil
}

// Save 把 value（管理端提交的 YAML 原文）存成一个新版本，返回新版本号。
// 全量替换语义、NormalizeConfigYAML/ParseConfigYAML 的校验规则都与
// ConfigService.Save 一致，见其注释。没有 push 参数——系统配置只在进程
// 启动时读一次，不广播。
func (s *SystemConfigService) Save(ctx context.Context, value string) (int64, error) {
	value = domain.NormalizeConfigYAML(value)
	if _, err := domain.ParseConfigYAML(value); err != nil {
		return 0, err
	}

	var seq int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(MAX(seq), 0) + 1 FROM system_config`).Scan(&seq); err != nil {
			return fmt.Errorf("service: 取下一个系统配置版本号: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO system_config (seq, value) VALUES ($1, $2)`, seq, value); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
				return domain.Failf(domain.ErrConflict, domain.CodeConfigVersionConflict, "配置版本冲突，请重试")
			}
			return fmt.Errorf("service: 写入系统配置版本: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM system_config WHERE seq <= $1`, seq-SystemConfigMaxVersions); err != nil {
			return fmt.Errorf("service: 修剪系统配置版本: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return seq, nil
}

// Rollback 把 seq 那一版的 value 复制成一个新版本，返回新版本号。与目标
// 版本内容完全一致时不产生新版本，原样返回当前版本号——理由与
// ConfigService.Rollback 相同：回滚本身也该是可回滚、可审计的一次
// "保存"，但内容没变就不该凭空多出一个版本号。
func (s *SystemConfigService) Rollback(ctx context.Context, seq int64) (int64, error) {
	old, err := s.Version(ctx, seq)
	if err != nil {
		return 0, err
	}
	current, err := s.Current(ctx)
	if err != nil {
		return 0, err
	}
	if current.Value == old.Value {
		return current.Seq, nil
	}
	return s.Save(ctx, old.Value)
}
