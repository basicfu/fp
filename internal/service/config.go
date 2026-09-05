// Package service 的配置中心部分：配置项由人在控制台创建，SDK 只读。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/domain"
)

// ConfigPublisher 是 ConfigService 对推送通道的全部依赖。
//
// 拆成接口而不是直接吃 *store.ConfigPublisher：保存逻辑的测试不需要
// Redis，而"选了仅落库就一次都不发"这条断言恰恰需要一个能数调用次数的
// 假实现（见 Task 3）。
type ConfigPublisher interface {
	Publish(ctx context.Context, appID uuid.UUID, typ string, seq int64) error
}

// ConfigService 管理配置的版本快照。
type ConfigService struct {
	pool *pgxpool.Pool
	pub  ConfigPublisher // 可为 nil：不推送，只落库
}

// NewConfigService 构造 ConfigService。
func NewConfigService(pool *pgxpool.Pool, pub ConfigPublisher) *ConfigService {
	return &ConfigService{pool: pool, pub: pub}
}

// checkConfigType 校验分区取值。
func checkConfigType(typ string) error {
	if !domain.IsConfigType(typ) {
		return domain.Fail(domain.ErrInvalidArgument, domain.CodeConfigTypeInvalid, "配置分区不合法").
			WithDesc("未知分区 %q", typ)
	}
	return nil
}

// Current 返回该分区当前版本。
//
// 一个版本都没有时返回 Seq=0 与空 Fields，**不是** ErrNotFound：
// "这个应用还没配过任何东西"是正常状态，不是错误。控制台第一次打开、
// SDK 第一次 Bind 走的都是这条路径。
func (s *ConfigService) Current(ctx context.Context, appID uuid.UUID, typ string) (domain.Config, error) {
	if err := checkConfigType(typ); err != nil {
		return domain.Config{}, err
	}
	seq, fields, createdAt, err := s.scanOne(ctx, `
		SELECT seq, fields, (extract(epoch FROM created_at) * 1000)::bigint
		FROM config WHERE application_id = $1 AND type = $2
		ORDER BY seq DESC LIMIT 1`, appID, typ)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Config{
			ApplicationID: appID,
			Type:          typ,
			Seq:           0,
			Fields:        map[string]domain.ConfigField{},
		}, nil
	}
	if err != nil {
		return domain.Config{}, err
	}
	return domain.Config{
		ApplicationID: appID,
		Type:          typ,
		Seq:           seq,
		Fields:        fields,
		CreatedAt:     createdAt,
	}, nil
}

// Version 返回指定版本。不存在返回 domain.ErrNotFound 的包装。
func (s *ConfigService) Version(ctx context.Context, appID uuid.UUID, typ string, seq int64) (domain.Config, error) {
	if err := checkConfigType(typ); err != nil {
		return domain.Config{}, err
	}
	retSeq, fields, createdAt, err := s.scanOne(ctx, `
		SELECT seq, fields, (extract(epoch FROM created_at) * 1000)::bigint
		FROM config WHERE application_id = $1 AND type = $2 AND seq = $3`, appID, typ, seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Config{}, domain.Fail(domain.ErrNotFound, domain.CodeConfigVersionNotFound, "配置版本不存在").
			WithDesc("分区 %s 没有第 %d 版", typ, seq)
	}
	if err != nil {
		return domain.Config{}, err
	}
	return domain.Config{
		ApplicationID: appID,
		Type:          typ,
		Seq:           retSeq,
		Fields:        fields,
		CreatedAt:     createdAt,
	}, nil
}

// ListVersions 返回最近 limit 个版本的元信息。元素的 Fields 恒为 nil——
// 列表页只需要 seq 与时间，一次把 100 份完整快照读出来纯属浪费。
func (s *ConfigService) ListVersions(ctx context.Context, appID uuid.UUID, typ string, limit int) ([]domain.Config, error) {
	if err := checkConfigType(typ); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		SELECT seq, (extract(epoch FROM created_at) * 1000)::bigint
		FROM config WHERE application_id = $1 AND type = $2
		ORDER BY seq DESC LIMIT $3`, appID, typ, limit)
	if err != nil {
		return nil, fmt.Errorf("service: 查询配置版本列表: %w", err)
	}
	defer rows.Close()

	out := make([]domain.Config, 0, limit)
	for rows.Next() {
		c := domain.Config{ApplicationID: appID, Type: typ}
		if err := rows.Scan(&c.Seq, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("service: 扫描配置版本: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service: 遍历配置版本: %w", err)
	}
	return out, nil
}

// scanOne 跑一条只返回 (seq, fields, created_at) 的查询。
//
// 它**只负责这三个值**，不去回填 ApplicationID / Type——那两个是调用方
// 自己传进来的参数，调用方直接填即可。
//
// 没有行时原样返回 pgx.ErrNoRows（不包装），由调用方决定那是错误还是
// 正常状态。
func (s *ConfigService) scanOne(ctx context.Context, sql string, args ...any) (int64, map[string]domain.ConfigField, int64, error) {
	var (
		seq       int64
		raw       []byte
		createdAt int64
	)
	if err := s.pool.QueryRow(ctx, sql, args...).Scan(&seq, &raw, &createdAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, 0, err
		}
		return 0, nil, 0, fmt.Errorf("service: 查询配置: %w", err)
	}
	fields := map[string]domain.ConfigField{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return 0, nil, 0, fmt.Errorf("service: 解析配置 fields: %w", err)
	}
	return seq, fields, createdAt, nil
}
