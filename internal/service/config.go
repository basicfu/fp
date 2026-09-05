// Package service 的配置中心部分：配置项由人在控制台创建，SDK 只读。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// ConfigMaxVersions 是每个分区保留的版本数上限，超出的从最老的开始删。
//
// 整版快照的代价是每次保存都抄一遍全部配置项（50 项约 7KB），这个上限
// 把它兜住：100 版约 700KB，一个应用两个分区 1.4MB。修剪在整版快照下是
// 安全的——每一行自包含，删掉老版本的后果就一句话：那些版本回滚不了，
// 其余一切照常。（换成"只存变更点"的时态行就不成立了：按 seq 删老行会
// 把"某个字段最后一次修改恰好落在老版本里"的那行删掉，那个字段会凭空
// 消失。这是当初选整版快照的两条理由之一。）
const ConfigMaxVersions = 100

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

// Save 用 fields 生成该分区的一个新版本，返回新版本的 seq。
//
// fields 是**全量替换**不是合并：新建、改值、改类型、改备注、删除配置项
// 全都走这一个入口。删除就是"新的 fields 里没有那个 key"。
//
// push 为 true 时保存后广播一次 ConfigChanged；为 false 就是控制台上的
// 「仅落库，实例重启后生效」——它专门解决"发布前必须先改值、但一改旧实例
// 立刻就会拿到"这个矛盾（设计文档第七节）。
func (s *ConfigService) Save(
	ctx context.Context, appID uuid.UUID, typ string,
	fields map[string]domain.ConfigField, push bool,
) (int64, error) {
	if err := checkConfigType(typ); err != nil {
		return 0, err
	}

	// 先把每一项按自己声明的类型规范化。任何一项转不过去就整批拒绝——
	// 一次保存是一个版本，不能出现"一半字段生效了"的版本。
	normalized := make(map[string]domain.ConfigField, len(fields))
	for k, f := range fields {
		if !domain.IsConfigValueType(f.Type) {
			return 0, domain.Fail(domain.ErrInvalidArgument, domain.CodeConfigTypeInvalid, "配置项类型不合法").
				WithDesc("配置项 %q 的类型 %q 未知", k, f.Type)
		}
		v, err := domain.CoerceConfigValue(f.Type, f.Value)
		if err != nil {
			return 0, err
		}
		normalized[k] = domain.ConfigField{Type: f.Type, Desc: f.Desc, Value: v}
	}

	raw, err := json.Marshal(normalized)
	if err != nil {
		return 0, fmt.Errorf("service: 序列化配置 fields: %w", err)
	}

	// seq 在事务里取 MAX+1，靠主键约束兜并发。管理操作低频，冲突了让调用方
	// 重试即可，不值得为它引入序列或咨询锁。
	var seq int64
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(MAX(seq), 0) + 1 FROM config
			WHERE application_id = $1 AND type = $2`, appID, typ).Scan(&seq); err != nil {
			return fmt.Errorf("service: 取下一个配置版本号: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO config (application_id, type, seq, fields)
			VALUES ($1, $2, $3, $4)`, appID, typ, seq, raw); err != nil {
			// INSERT 的主键冲突表示有并发的 Save 抢输了。转换为可被识别的
			// domain.ErrConflict，让调用方据此重试而不是去解析 pgconn 错误。
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
				return domain.Failf(domain.ErrConflict, domain.CodeConfigVersionConflict, "配置版本冲突，请重试")
			}
			return fmt.Errorf("service: 写入配置版本: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM config
			WHERE application_id = $1 AND type = $2 AND seq <= $3`,
			appID, typ, seq-ConfigMaxVersions); err != nil {
			return fmt.Errorf("service: 修剪配置版本: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	// 推送失败不回滚保存：值已经落库了，那是权威事实；推送只是把生效延迟
	// 从"下次重启"压到近乎实时的加速手段（与 store.RevokePublisher 同一逻辑）。
	if push && s.pub != nil {
		if err := s.pub.Publish(ctx, appID, typ, seq); err != nil {
			slog.Warn("service: 广播配置变更失败，新值要等实例重启才生效",
				"appID", appID, "type", typ, "seq", seq, "err", err)
		}
	}
	return seq, nil
}
