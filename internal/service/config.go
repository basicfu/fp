// Package service 的配置中心部分：配置项由人在控制台创建，SDK 只读。
package service

import (
	"context"
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

// checkConfigType 校验分区名的字符集（字母开头、字母数字下划线、不超过
// 64 字符）。分区名本身是任意的——不再局限于 DEFAULT/WEB 两个固定值，
// 这里只挡明显不合法的输入（空字符串、带斜杠问号这类会把 URL 查询参数
// 弄乱的字符）。
func checkConfigType(typ string) error {
	if !domain.IsConfigType(typ) {
		return domain.Fail(domain.ErrInvalidArgument, domain.CodeConfigTypeInvalid, "配置分区不合法").
			WithDesc("分区名 %q 不合法：必须以字母开头，只能包含字母、数字、下划线，且不超过 64 个字符", typ)
	}
	return nil
}

// Current 返回该分区当前版本。
//
// 一个版本都没有时返回 Seq=0 与空 Value，**不是** ErrNotFound：
// "这个应用还没配过任何东西"是正常状态，不是错误。控制台第一次打开、
// SDK 第一次 Bind 走的都是这条路径。
func (s *ConfigService) Current(ctx context.Context, appID uuid.UUID, typ string) (domain.Config, error) {
	if err := checkConfigType(typ); err != nil {
		return domain.Config{}, err
	}
	seq, value, createdAt, err := s.scanOne(ctx, `
		SELECT seq, value, (extract(epoch FROM created_at) * 1000)::bigint
		FROM config WHERE application_id = $1 AND type = $2
		ORDER BY seq DESC LIMIT 1`, appID, typ)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Config{
			ApplicationID: appID,
			Type:          typ,
			Seq:           0,
			Value:         "",
		}, nil
	}
	if err != nil {
		return domain.Config{}, err
	}
	return domain.Config{
		ApplicationID: appID,
		Type:          typ,
		Seq:           seq,
		Value:         value,
		CreatedAt:     createdAt,
	}, nil
}

// Version 返回指定版本。不存在返回 domain.ErrNotFound 的包装。
func (s *ConfigService) Version(ctx context.Context, appID uuid.UUID, typ string, seq int64) (domain.Config, error) {
	if err := checkConfigType(typ); err != nil {
		return domain.Config{}, err
	}
	retSeq, value, createdAt, err := s.scanOne(ctx, `
		SELECT seq, value, (extract(epoch FROM created_at) * 1000)::bigint
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
		Value:         value,
		CreatedAt:     createdAt,
	}, nil
}

// ListVersions 返回最近 limit 个版本的元信息。元素的 Value 恒为空字符串——
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

// scanOne 跑一条只返回 (seq, value, created_at) 的查询。
//
// 它**只负责这三个值**，不去回填 ApplicationID / Type——那两个是调用方
// 自己传进来的参数，调用方直接填即可。
//
// 没有行时原样返回 pgx.ErrNoRows（不包装），由调用方决定那是错误还是
// 正常状态。
func (s *ConfigService) scanOne(ctx context.Context, sql string, args ...any) (int64, string, int64, error) {
	var (
		seq       int64
		value     string
		createdAt int64
	)
	if err := s.pool.QueryRow(ctx, sql, args...).Scan(&seq, &value, &createdAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, "", 0, err
		}
		return 0, "", 0, fmt.Errorf("service: 查询配置: %w", err)
	}
	return seq, value, createdAt, nil
}

// Save 把 value（管理端提交的 YAML 原文）存成该分区的一个新版本，返回
// 新版本的 seq。value 是**全量替换**不是合并：整份 YAML 文本原样落库，
// 新建、改值、加注释、删掉某一行全都只是"这次提交的文本长什么样"，
// 不需要区分"新建/编辑/删除"这几种动作——它们本来就是同一件事
// （提交一份新文本）在语义上被强行拆成的几种前端操作。
//
// 提交前先跑 domain.NormalizeConfigYAML 补齐"key:value"漏掉的那个空格
// （最常见的手填笔误），再校验：value 必须是能解析、且顶层是映射的
// YAML，否则整份拒绝——这与旧版"逐字段转换、任何一项转不过去就整批拒绝"
// 是同一条原则的延续，只是校验粒度从"每个字段的类型"变成了"整份文本的
// 语法"。落库的是补完空格之后的文本，不是调用方原样传进来的那份——
// 保存回显因此不再保证逐字节相同，这是 NormalizeConfigYAML 文档里写清楚
// 的已知取舍。
//
// push 为 true 时保存后广播一次 ConfigChanged；为 false 就是控制台上的
// 「仅落库，实例重启后生效」——它专门解决"发布前必须先改值、但一改旧实例
// 立刻就会拿到"这个矛盾（设计文档第七节）。
func (s *ConfigService) Save(
	ctx context.Context, appID uuid.UUID, typ string,
	value string, push bool,
) (int64, error) {
	if err := checkConfigType(typ); err != nil {
		return 0, err
	}

	value = domain.NormalizeConfigYAML(value)
	if _, err := domain.ParseConfigYAML(value); err != nil {
		return 0, err
	}

	// seq 在事务里取 MAX+1，靠主键约束兜并发。管理操作低频，冲突了让调用方
	// 重试即可，不值得为它引入序列或咨询锁。
	var seq int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(MAX(seq), 0) + 1 FROM config
			WHERE application_id = $1 AND type = $2`, appID, typ).Scan(&seq); err != nil {
			return fmt.Errorf("service: 取下一个配置版本号: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO config (application_id, type, seq, value)
			VALUES ($1, $2, $3, $4)`, appID, typ, seq, value); err != nil {
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

// Rollback 把 seq 那一版的 value 复制成一个新版本，返回新版本号。
//
// 复制而不是删除：v7 出了问题回滚到 v6，产出的是 v8，v7 原样留在历史里。
// 这让"回滚本身"也可被回滚，审计链完整。
//
// 走 Save 而不是直接 INSERT ... SELECT：修剪、推送、以及"一次保存 = 一个
// 版本"这套语义只该有一处实现。多出来的代价只是把 value 这个字符串在
// 进程内绕一圈，可以忽略。
//
// 例外：目标版本的内容与当前版本完全一致时不调用 Save——这种回滚不会
// 让任何东西发生变化，不该单纯因为点了一次"回滚"就凭空多出一个版本号，
// 把历史记录撑得比实际发生过的变更还长。此时原样返回当前版本号，不落
// 库也不推送。
func (s *ConfigService) Rollback(
	ctx context.Context, appID uuid.UUID, typ string, seq int64, push bool,
) (int64, error) {
	old, err := s.Version(ctx, appID, typ, seq)
	if err != nil {
		return 0, err
	}
	current, err := s.Current(ctx, appID, typ)
	if err != nil {
		return 0, err
	}
	if current.Value == old.Value {
		return current.Seq, nil
	}
	return s.Save(ctx, appID, typ, old.Value, push)
}

// ListTypes 返回这个应用下**保存过至少一个版本**的分区名，升序排列。
//
// DEFAULT 在不在这份列表里如实反映数据库——它是不是"应用天然就有、UI
// 永远展示"这条 UI 规则，由调用方（管理端）自己在展示层兜底，service
// 层不负责伪造一个从未真实存在过的分区。
func (s *ConfigService) ListTypes(ctx context.Context, appID uuid.UUID) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT type FROM config WHERE application_id = $1 ORDER BY type`, appID)
	if err != nil {
		return nil, fmt.Errorf("service: 查询配置分区列表: %w", err)
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("service: 扫描配置分区: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service: 遍历配置分区: %w", err)
	}
	return out, nil
}

// DeleteType 删除该分区**全部**版本，包括历史——不是新建一个空版本，是把
// 这个分区从数据库里彻底抹掉，删完之后 Version 对它的任何 seq 都会返回
// ErrNotFound，没有任何一版可以回滚回去。管理端必须在调用前二次确认，
// 且要在提示里说清楚"这是不可撤销的"。
//
// 故意不广播 ConfigChanged：删除是这个页面里最危险的操作，不该在没人
// 明确要求"立即生效"的情况下让运行中的实例瞬间失去这个分区的值——这里
// 采用与"仅落库"相同的保守默认，运行中的实例保留最后一次成功加载的值，
// 直到重启或下次重新拉取才会感知到分区已经没了。
func (s *ConfigService) DeleteType(ctx context.Context, appID uuid.UUID, typ string) error {
	if err := checkConfigType(typ); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM config WHERE application_id = $1 AND type = $2`, appID, typ); err != nil {
		return fmt.Errorf("service: 删除配置分区: %w", err)
	}
	return nil
}
