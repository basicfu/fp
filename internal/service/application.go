package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/basicfu/fp/internal/domain"
)

// pgUniqueViolation 是 PostgreSQL 唯一约束冲突的 SQLSTATE。
const pgUniqueViolation = "23505"

// rowScanner 同时被 pgx.Row 与 pgx.Rows 满足，让扫描逻辑只写一遍。
type rowScanner interface {
	Scan(dest ...any) error
}

// applicationColumns 是所有读取 application 的查询共用的列清单，保证 scanApplication 能复用。
// 时间统一转成毫秒，与 Go 侧的 int64 约定一致。
const applicationColumns = `
	id, name, slug, app_id, status,
	idle_timeout_seconds, idle_timeout_mobile_seconds, max_lifetime_seconds,
	rotate_interval_seconds, extend_interval_seconds, token_cache_ttl_seconds,
	cookie_domain, redirect_uris, grant_types,
	(extract(epoch from created_at) * 1000)::bigint,
	(extract(epoch from updated_at) * 1000)::bigint`

// ApplicationService 管理接入端应用及其登录方式配置。
type ApplicationService struct {
	pool *pgxpool.Pool
}

// NewApplicationService 构造 ApplicationService。
func NewApplicationService(pool *pgxpool.Pool) *ApplicationService {
	return &ApplicationService{pool: pool}
}

// Create 新建应用，返回应用与仅此一次可见的明文 appSecret。
// 明文 secret 不落库，只存 bcrypt 哈希。
func (s *ApplicationService) Create(ctx context.Context, name, slug string) (*domain.Application, string, error) {
	if name == "" || slug == "" {
		return nil, "", domain.Errorf(domain.ErrInvalidArgument, "name 与 slug 不能为空")
	}

	appID, err := randomToken()
	if err != nil {
		return nil, "", err
	}
	secret, err := randomToken()
	if err != nil {
		return nil, "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(secret), bcryptCost)
	if err != nil {
		return nil, "", fmt.Errorf("service: 计算 appSecret 哈希: %w", err)
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO application (name, slug, app_id, app_secret_hash)
		VALUES ($1, $2, $3, $4)
		RETURNING `+applicationColumns, name, slug, appID, string(hash))

	app, err := scanApplication(row)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return nil, "", domain.Errorf(domain.ErrConflict, "slug %q 已被占用", slug)
		}
		return nil, "", fmt.Errorf("service: 创建应用: %w", err)
	}
	return app, secret, nil
}

// List 返回全部应用，按创建时间倒序。永不返回 nil 切片。
func (s *ApplicationService) List(ctx context.Context) ([]domain.Application, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+applicationColumns+` FROM application ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("service: 查询应用列表: %w", err)
	}
	defer rows.Close()

	out := []domain.Application{}
	for rows.Next() {
		app, err := scanApplication(rows)
		if err != nil {
			return nil, fmt.Errorf("service: 扫描应用: %w", err)
		}
		out = append(out, *app)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service: 遍历应用: %w", err)
	}
	return out, nil
}

// GetByID 按内部 ID 查应用。
func (s *ApplicationService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Application, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+applicationColumns+` FROM application WHERE id = $1`, id)
	app, err := scanApplication(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Errorf(domain.ErrNotFound, "应用不存在")
	}
	if err != nil {
		return nil, fmt.Errorf("service: 查询应用: %w", err)
	}
	return app, nil
}

// GetByAppID 按对外的 appId 查应用。
func (s *ApplicationService) GetByAppID(ctx context.Context, appID string) (*domain.Application, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+applicationColumns+` FROM application WHERE app_id = $1`, appID)
	app, err := scanApplication(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Errorf(domain.ErrNotFound, "应用不存在")
	}
	if err != nil {
		return nil, fmt.Errorf("service: 按 appId 查询应用: %w", err)
	}
	return app, nil
}

// VerifySecret 校验 appId + appSecret，成功返回对应应用。
// appId 不存在与 secret 错误返回同一错误，避免 appId 枚举。
func (s *ApplicationService) VerifySecret(ctx context.Context, appID, plainSecret string) (*domain.Application, error) {
	var hash string
	err := s.pool.QueryRow(ctx, `SELECT app_secret_hash FROM application WHERE app_id = $1`, appID).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Errorf(domain.ErrInvalidCredential, "appId 或 appSecret 不正确")
	}
	if err != nil {
		return nil, fmt.Errorf("service: 读取 appSecret 哈希: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plainSecret)); err != nil {
		return nil, domain.Errorf(domain.ErrInvalidCredential, "appId 或 appSecret 不正确")
	}
	return s.GetByAppID(ctx, appID)
}

// UpdateSessionPolicy 更新应用的会话策略。策略非法时不写库。
func (s *ApplicationService) UpdateSessionPolicy(ctx context.Context, id uuid.UUID, p domain.SessionPolicy) (*domain.Application, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	row := s.pool.QueryRow(ctx, `
		UPDATE application SET
			idle_timeout_seconds        = $2,
			idle_timeout_mobile_seconds = $3,
			max_lifetime_seconds        = $4,
			rotate_interval_seconds     = $5,
			extend_interval_seconds     = $6,
			token_cache_ttl_seconds     = $7,
			updated_at                  = now()
		WHERE id = $1
		RETURNING `+applicationColumns,
		id,
		p.IdleTimeoutSeconds, p.IdleTimeoutMobileSeconds, p.MaxLifetimeSeconds,
		p.RotateIntervalSeconds, p.ExtendIntervalSeconds, p.TokenCacheTTLSeconds)

	app, err := scanApplication(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Errorf(domain.ErrNotFound, "应用不存在")
	}
	if err != nil {
		return nil, fmt.Errorf("service: 更新会话策略: %w", err)
	}
	return app, nil
}

// SetConnector 写入或覆盖某个应用的某种登录方式配置。
func (s *ApplicationService) SetConnector(ctx context.Context, appID uuid.UUID, connectorType string, enabled bool, config map[string]any) error {
	if connectorType == "" {
		return domain.Errorf(domain.ErrInvalidArgument, "connector 类型不能为空")
	}
	if config == nil {
		config = map[string]any{}
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return domain.Errorf(domain.ErrInvalidArgument, "connector 配置无法序列化: %v", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO application_connector (application_id, connector_type, enabled, config)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (application_id, connector_type)
		DO UPDATE SET enabled = EXCLUDED.enabled, config = EXCLUDED.config, updated_at = now()`,
		appID, connectorType, enabled, raw)
	if err != nil {
		return fmt.Errorf("service: 写入 connector 配置: %w", err)
	}
	return nil
}

// GetConnector 返回单个 connector 配置。未配置时返回 domain.ErrNotFound。
func (s *ApplicationService) GetConnector(ctx context.Context, appID uuid.UUID, connectorType string) (*domain.ApplicationConnector, error) {
	var (
		c   domain.ApplicationConnector
		raw []byte
	)
	err := s.pool.QueryRow(ctx, `
		SELECT connector_type, enabled, config
		FROM application_connector
		WHERE application_id = $1 AND connector_type = $2`, appID, connectorType).
		Scan(&c.Type, &c.Enabled, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Errorf(domain.ErrNotFound, "该应用未配置 %s 登录方式", connectorType)
	}
	if err != nil {
		return nil, fmt.Errorf("service: 查询 connector 配置: %w", err)
	}
	if err := json.Unmarshal(raw, &c.Config); err != nil {
		return nil, fmt.Errorf("service: 解析 connector 配置: %w", err)
	}
	return &c, nil
}

// ListConnectors 返回某个应用已配置的全部登录方式。永不返回 nil 切片。
func (s *ApplicationService) ListConnectors(ctx context.Context, appID uuid.UUID) ([]domain.ApplicationConnector, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT connector_type, enabled, config
		FROM application_connector
		WHERE application_id = $1
		ORDER BY connector_type`, appID)
	if err != nil {
		return nil, fmt.Errorf("service: 查询 connector 配置: %w", err)
	}
	defer rows.Close()

	out := []domain.ApplicationConnector{}
	for rows.Next() {
		var (
			c   domain.ApplicationConnector
			raw []byte
		)
		if err := rows.Scan(&c.Type, &c.Enabled, &raw); err != nil {
			return nil, fmt.Errorf("service: 扫描 connector 配置: %w", err)
		}
		if err := json.Unmarshal(raw, &c.Config); err != nil {
			return nil, fmt.Errorf("service: 解析 connector 配置: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service: 遍历 connector 配置: %w", err)
	}
	return out, nil
}

func scanApplication(r rowScanner) (*domain.Application, error) {
	var app domain.Application
	err := r.Scan(
		&app.ID, &app.Name, &app.Slug, &app.AppID, &app.Status,
		&app.Session.IdleTimeoutSeconds, &app.Session.IdleTimeoutMobileSeconds, &app.Session.MaxLifetimeSeconds,
		&app.Session.RotateIntervalSeconds, &app.Session.ExtendIntervalSeconds, &app.Session.TokenCacheTTLSeconds,
		&app.CookieDomain, &app.RedirectURIs, &app.GrantTypes,
		&app.CreatedAt, &app.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &app, nil
}
