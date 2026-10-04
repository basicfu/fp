package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/notify"
)

// NotifyService 管理通知的供应商实例、模板与关联，并负责发送编排（见 notify_send.go）。
//
// 供应商"类型"是代码里写死的（notify.Registry），数据库里存的只是实例：一份凭据一行。
type NotifyService struct {
	pool    *pgxpool.Pool
	rdb     *redis.Client
	reg     *notify.Registry
	isProd  bool
	shuffle func(n int, swap func(i, j int))

	mu    sync.Mutex
	cache map[string]notify.Provider // 已构造的供应商，key 是 id@updated_at
}

type NotifyOption func(*NotifyService)

// WithNotifyProd 让 DevOnly 的供应商类型（log）在 prod 环境不可建、不暴露。
func WithNotifyProd(isProd bool) NotifyOption { return func(s *NotifyService) { s.isProd = isProd } }

// WithNotifyShuffle 注入洗牌函数，测试用它得到确定的"随机"顺序。
func WithNotifyShuffle(f func(n int, swap func(i, j int))) NotifyOption {
	return func(s *NotifyService) { s.shuffle = f }
}

func NewNotifyService(pool *pgxpool.Pool, rdb *redis.Client, reg *notify.Registry, opts ...NotifyOption) *NotifyService {
	s := &NotifyService{pool: pool, rdb: rdb, reg: reg, shuffle: rand.Shuffle, cache: map[string]notify.Provider{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// ---------------------------------------------------------------------------
// 供应商类型
// ---------------------------------------------------------------------------

// NotifyProviderType 是控制台"新建供应商"要用的类型描述。
type NotifyProviderType struct {
	Type string
	// Channel 为空表示由实例配置里的 channel 决定（log 类型）。
	Channel domain.NotifyChannel
	Fields  []domain.Field
}

func (s *NotifyService) ProviderTypes() []NotifyProviderType {
	specs := s.reg.Types(s.isProd)
	out := make([]NotifyProviderType, 0, len(specs))
	for _, sp := range specs {
		out = append(out, NotifyProviderType{Type: sp.Type, Channel: sp.Channel, Fields: sp.ConfigSchema})
	}
	return out
}

func providerInvalid(format string, a ...any) *domain.Error {
	return domain.Failf(domain.ErrInvalidArgument, domain.CodeNotifyProviderInvalid, format, a...)
}

// spec 取供应商类型；prod 环境下 DevOnly 的类型当作不存在。
func (s *NotifyService) spec(typ string) (notify.TypeSpec, error) {
	sp, ok := s.reg.Spec(typ)
	if !ok || (s.isProd && sp.DevOnly) {
		return notify.TypeSpec{}, providerInvalid("未知的供应商类型 %q", typ)
	}
	return sp, nil
}

// checkConfig 按 schema 规整配置，并用构造函数做一次试构造——配置错了在保存时就报，
// 不是等到第一次发送。
func (s *NotifyService) checkConfig(sp notify.TypeSpec, in map[string]any) (map[string]any, error) {
	cfg, ferr := domain.NormalizeConfig(sp.ConfigSchema, in)
	if ferr != nil {
		return nil, providerInvalid("配置项 %s：%s", ferr.Key, ferr.Msg).WithField("field", ferr.Key)
	}
	if _, err := sp.New(notify.Config(cfg)); err != nil {
		return nil, providerInvalid("配置不合法：%v", err)
	}
	return cfg, nil
}

// ---------------------------------------------------------------------------
// 供应商实例
// ---------------------------------------------------------------------------

const notifyProviderColumns = `id, type, description, enabled, config, created_at, updated_at`

func scanNotifyProvider(row pgx.Row) (*domain.NotifyProvider, error) {
	var (
		p   domain.NotifyProvider
		raw []byte
	)
	if err := row.Scan(&p.ID, &p.Type, &p.Description, &p.Enabled, &raw, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &p.Config); err != nil {
		return nil, fmt.Errorf("service: 解析供应商配置: %w", err)
	}
	return &p, nil
}

type CreateNotifyProviderInput struct {
	Type        string
	Description string
	Enabled     bool
	Config      map[string]any
}

func (s *NotifyService) CreateProvider(ctx context.Context, in CreateNotifyProviderInput) (*domain.NotifyProvider, error) {
	sp, err := s.spec(in.Type)
	if err != nil {
		return nil, err
	}
	cfg, err := s.checkConfig(sp, in.Config)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("service: 序列化供应商配置: %w", err)
	}
	p, err := scanNotifyProvider(s.pool.QueryRow(ctx, `
		INSERT INTO notify_provider (type, description, enabled, config)
		VALUES ($1, $2, $3, $4) RETURNING `+notifyProviderColumns,
		in.Type, strings.TrimSpace(in.Description), in.Enabled, raw))
	if err != nil {
		return nil, fmt.Errorf("service: 创建供应商: %w", err)
	}
	return p, nil
}

func (s *NotifyService) GetProvider(ctx context.Context, id uuid.UUID) (*domain.NotifyProvider, error) {
	p, err := scanNotifyProvider(s.pool.QueryRow(ctx,
		`SELECT `+notifyProviderColumns+` FROM notify_provider WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Failf(domain.ErrNotFound, domain.CodeNotifyProviderNotFound, "供应商不存在")
	}
	if err != nil {
		return nil, fmt.Errorf("service: 查询供应商: %w", err)
	}
	return p, nil
}

// NotifyProviderView 是列表里的一行：供应商加上被多少个模板引用。
type NotifyProviderView struct {
	domain.NotifyProvider
	TemplateCount int
}

func (s *NotifyService) ListProviders(ctx context.Context) ([]NotifyProviderView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.type, p.description, p.enabled, p.config, p.created_at, p.updated_at,
		       (SELECT count(*) FROM notify_template_provider tp WHERE tp.provider_id = p.id)
		FROM notify_provider p ORDER BY p.created_at DESC, p.id`)
	if err != nil {
		return nil, fmt.Errorf("service: 列出供应商: %w", err)
	}
	defer rows.Close()
	out := []NotifyProviderView{}
	for rows.Next() {
		var (
			v   NotifyProviderView
			raw []byte
		)
		if err := rows.Scan(&v.ID, &v.Type, &v.Description, &v.Enabled, &raw, &v.CreatedAt, &v.UpdatedAt, &v.TemplateCount); err != nil {
			return nil, fmt.Errorf("service: 扫描供应商: %w", err)
		}
		if err := json.Unmarshal(raw, &v.Config); err != nil {
			return nil, fmt.Errorf("service: 解析供应商配置: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

type UpdateNotifyProviderInput struct {
	Description *string
	Enabled     *bool
	// Config 为 nil 表示不改配置。secret 字段传回 domain.SecretMask 表示保持原值。
	Config map[string]any
}

func (s *NotifyService) UpdateProvider(ctx context.Context, id uuid.UUID, in UpdateNotifyProviderInput) (*domain.NotifyProvider, error) {
	cur, err := s.GetProvider(ctx, id)
	if err != nil {
		return nil, err
	}
	desc, enabled, cfg := cur.Description, cur.Enabled, cur.Config
	if in.Description != nil {
		desc = strings.TrimSpace(*in.Description)
	}
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	if in.Config != nil {
		sp, err := s.spec(cur.Type)
		if err != nil {
			return nil, err
		}
		if cfg, err = s.checkConfig(sp, domain.MergeSecrets(sp.ConfigSchema, in.Config, cur.Config)); err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("service: 序列化供应商配置: %w", err)
	}
	p, err := scanNotifyProvider(s.pool.QueryRow(ctx, `
		UPDATE notify_provider SET description = $2, enabled = $3, config = $4, updated_at = now()
		WHERE id = $1 RETURNING `+notifyProviderColumns, id, desc, enabled, raw))
	if err != nil {
		return nil, fmt.Errorf("service: 更新供应商: %w", err)
	}
	return p, nil
}

// DeleteProvider 删除供应商实例。仍被模板引用时拒绝：外键保护，不靠应用层先查再删。
func (s *NotifyService) DeleteProvider(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM notify_provider WHERE id = $1`, id)
	if isForeignKeyViolation(err) {
		return domain.Fail(domain.ErrConflict, domain.CodeNotifyProviderInUse, "供应商仍被模板引用，请先在模板里解除关联")
	}
	if err != nil {
		return fmt.Errorf("service: 删除供应商: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.Fail(domain.ErrNotFound, domain.CodeNotifyProviderNotFound, "供应商不存在")
	}
	return nil
}

// MaskConfig 返回供控制台展示的配置：secret 字段换成掩码。
// 类型已从代码里下线的供应商不返回任何配置——认不出哪些字段是 secret，宁可不给。
func (s *NotifyService) MaskConfig(p domain.NotifyProvider) map[string]any {
	sp, ok := s.reg.Spec(p.Type)
	if !ok {
		return map[string]any{}
	}
	return domain.MaskSecrets(sp.ConfigSchema, p.Config)
}

// ProviderChannel 返回该实例服务的渠道；类型未知时返回空串。
func (s *NotifyService) ProviderChannel(p domain.NotifyProvider) domain.NotifyChannel {
	sp, ok := s.reg.Spec(p.Type)
	if !ok {
		return ""
	}
	return sp.ChannelOf(p.Config)
}

// NotifyProviderUsage 是"某个供应商被哪个模板引用"的一行。
type NotifyProviderUsage struct {
	Code               string
	Channel            domain.NotifyChannel
	TemplateEnabled    bool
	ProviderTemplateID string
	LinkEnabled        bool
	Priority           int
}

func (s *NotifyService) ProviderUsages(ctx context.Context, id uuid.UUID) ([]NotifyProviderUsage, error) {
	if _, err := s.GetProvider(ctx, id); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT tp.code, t.channel, t.enabled, tp.provider_template_id, tp.enabled, tp.priority
		FROM notify_template_provider tp JOIN notify_template t ON t.code = tp.code
		WHERE tp.provider_id = $1 ORDER BY tp.code`, id)
	if err != nil {
		return nil, fmt.Errorf("service: 查询供应商引用: %w", err)
	}
	defer rows.Close()
	out := []NotifyProviderUsage{}
	for rows.Next() {
		var u NotifyProviderUsage
		if err := rows.Scan(&u.Code, &u.Channel, &u.TemplateEnabled, &u.ProviderTemplateID, &u.LinkEnabled, &u.Priority); err != nil {
			return nil, fmt.Errorf("service: 扫描供应商引用: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// 模板
// ---------------------------------------------------------------------------

var notifyCodeRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

const notifyTemplateColumns = `code, channel, template_mode, template, description, enabled, created_at, updated_at`

func scanNotifyTemplate(row pgx.Row) (*domain.NotifyTemplate, error) {
	var (
		t   domain.NotifyTemplate
		raw []byte
	)
	if err := row.Scan(&t.Code, &t.Channel, &t.Mode, &raw, &t.Description, &t.Enabled, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &t.Content); err != nil {
		return nil, fmt.Errorf("service: 解析模板内容: %w", err)
	}
	return &t, nil
}

type CreateNotifyTemplateInput struct {
	Code        string
	Channel     domain.NotifyChannel
	Mode        domain.NotifyMode
	Content     domain.NotifyContent
	Description string
	Enabled     bool
}

func (s *NotifyService) CreateTemplate(ctx context.Context, in CreateNotifyTemplateInput) (*domain.NotifyTemplate, error) {
	if !notifyCodeRE.MatchString(in.Code) {
		return nil, domain.Fail(domain.ErrInvalidArgument, domain.CodeNotifyTemplateInvalid,
			"code 只能包含字母、数字、下划线、点和横线，以字母或数字开头，长度 1–64")
	}
	content, err := notify.ValidateContent(in.Channel, in.Mode, in.Content)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("service: 序列化模板内容: %w", err)
	}
	t, err := scanNotifyTemplate(s.pool.QueryRow(ctx, `
		INSERT INTO notify_template (code, channel, template_mode, template, description, enabled)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+notifyTemplateColumns,
		in.Code, string(in.Channel), string(in.Mode), raw, strings.TrimSpace(in.Description), in.Enabled))
	if isUniqueViolation(err) {
		return nil, domain.Failf(domain.ErrConflict, domain.CodeNotifyTemplateCodeTaken, "code %q 已被占用", in.Code)
	}
	if err != nil {
		return nil, fmt.Errorf("service: 创建模板: %w", err)
	}
	return t, nil
}

func (s *NotifyService) loadTemplate(ctx context.Context, code string) (*domain.NotifyTemplate, error) {
	t, err := scanNotifyTemplate(s.pool.QueryRow(ctx,
		`SELECT `+notifyTemplateColumns+` FROM notify_template WHERE code = $1`, code))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Failf(domain.ErrNotFound, domain.CodeNotifyTemplateNotFound, "通知模板 %q 不存在", code)
	}
	if err != nil {
		return nil, fmt.Errorf("service: 查询模板: %w", err)
	}
	return t, nil
}

// NotifyTemplateView 是列表里的一行：模板加上关联了几个供应商实例。
type NotifyTemplateView struct {
	domain.NotifyTemplate
	ProviderCount int
}

func (s *NotifyService) ListTemplates(ctx context.Context) ([]NotifyTemplateView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.code, t.channel, t.template_mode, t.template, t.description, t.enabled, t.created_at, t.updated_at,
		       (SELECT count(*) FROM notify_template_provider tp WHERE tp.code = t.code)
		FROM notify_template t ORDER BY t.code`)
	if err != nil {
		return nil, fmt.Errorf("service: 列出模板: %w", err)
	}
	defer rows.Close()
	out := []NotifyTemplateView{}
	for rows.Next() {
		var (
			v   NotifyTemplateView
			raw []byte
		)
		if err := rows.Scan(&v.Code, &v.Channel, &v.Mode, &raw, &v.Description, &v.Enabled, &v.CreatedAt, &v.UpdatedAt, &v.ProviderCount); err != nil {
			return nil, fmt.Errorf("service: 扫描模板: %w", err)
		}
		if err := json.Unmarshal(raw, &v.Content); err != nil {
			return nil, fmt.Errorf("service: 解析模板内容: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// NotifyLinkView 是模板详情里的一行关联：关联本身加上供应商实例的摘要。
type NotifyLinkView struct {
	domain.NotifyTemplateProvider
	ProviderType        string
	ProviderDescription string
	ProviderEnabled     bool
}

type NotifyTemplateDetail struct {
	Template domain.NotifyTemplate
	Links    []NotifyLinkView
}

func (s *NotifyService) GetTemplate(ctx context.Context, code string) (*NotifyTemplateDetail, error) {
	t, err := s.loadTemplate(ctx, code)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT tp.provider_id, tp.provider_template_id, tp.enabled, tp.priority, p.type, p.description, p.enabled
		FROM notify_template_provider tp JOIN notify_provider p ON p.id = tp.provider_id
		WHERE tp.code = $1 ORDER BY tp.priority DESC, p.created_at, p.id`, code)
	if err != nil {
		return nil, fmt.Errorf("service: 查询模板关联: %w", err)
	}
	defer rows.Close()
	links := []NotifyLinkView{}
	for rows.Next() {
		l := NotifyLinkView{NotifyTemplateProvider: domain.NotifyTemplateProvider{Code: code}}
		if err := rows.Scan(&l.ProviderID, &l.ProviderTemplateID, &l.Enabled, &l.Priority,
			&l.ProviderType, &l.ProviderDescription, &l.ProviderEnabled); err != nil {
			return nil, fmt.Errorf("service: 扫描模板关联: %w", err)
		}
		links = append(links, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &NotifyTemplateDetail{Template: *t, Links: links}, nil
}

// UpdateNotifyTemplateInput 里为 nil 的字段不改。code、channel、mode 创建后不可改：
// code 被业务代码硬引用；channel 与 mode 一变，已有的供应商关联就全部失效。
type UpdateNotifyTemplateInput struct {
	Content     *domain.NotifyContent
	Description *string
	Enabled     *bool
}

func (s *NotifyService) UpdateTemplate(ctx context.Context, code string, in UpdateNotifyTemplateInput) (*domain.NotifyTemplate, error) {
	cur, err := s.loadTemplate(ctx, code)
	if err != nil {
		return nil, err
	}
	content, desc, enabled := cur.Content, cur.Description, cur.Enabled
	if in.Content != nil {
		if content, err = notify.ValidateContent(cur.Channel, cur.Mode, *in.Content); err != nil {
			return nil, err
		}
	}
	if in.Description != nil {
		desc = strings.TrimSpace(*in.Description)
	}
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("service: 序列化模板内容: %w", err)
	}
	t, err := scanNotifyTemplate(s.pool.QueryRow(ctx, `
		UPDATE notify_template SET template = $2, description = $3, enabled = $4, updated_at = now()
		WHERE code = $1 RETURNING `+notifyTemplateColumns, code, raw, desc, enabled))
	if err != nil {
		return nil, fmt.Errorf("service: 更新模板: %w", err)
	}
	return t, nil
}

// DeleteTemplate 删除模板，关联行随外键级联删除。
func (s *NotifyService) DeleteTemplate(ctx context.Context, code string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM notify_template WHERE code = $1`, code)
	if err != nil {
		return fmt.Errorf("service: 删除模板: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.Failf(domain.ErrNotFound, domain.CodeNotifyTemplateNotFound, "通知模板 %q 不存在", code)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 模板 ↔ 供应商实例
// ---------------------------------------------------------------------------

type SetNotifyLinkInput struct {
	// ProviderTemplateID 是该实例在供应商那边审核通过的模板 ID：vendor 模式必填，custom 模式必须为空。
	ProviderTemplateID string
	Enabled            bool
	// Priority 越大越优先；0 表示无偏好，同优先级的参与随机。
	Priority int
}

// SetTemplateProvider 新增或修改一条关联。
func (s *NotifyService) SetTemplateProvider(ctx context.Context, code string, providerID uuid.UUID, in SetNotifyLinkInput) error {
	tpl, err := s.loadTemplate(ctx, code)
	if err != nil {
		return err
	}
	prov, err := s.GetProvider(ctx, providerID)
	if err != nil {
		return err
	}
	linkInvalid := func(format string, a ...any) error {
		return domain.Failf(domain.ErrInvalidArgument, domain.CodeNotifyLinkInvalid, format, a...)
	}
	sp, ok := s.reg.Spec(prov.Type)
	if !ok {
		return linkInvalid("供应商类型 %q 已不受支持", prov.Type)
	}
	if ch := sp.ChannelOf(prov.Config); ch != tpl.Channel {
		return linkInvalid("供应商的渠道（%s）与模板的渠道（%s）不一致", ch, tpl.Channel)
	}
	ptid := strings.TrimSpace(in.ProviderTemplateID)
	switch tpl.Mode {
	case domain.NotifyModeVendor:
		if ptid == "" {
			return linkInvalid("vendor 模板必须填写该供应商侧审核通过的模板 ID")
		}
	case domain.NotifyModeCustom:
		if ptid != "" {
			return linkInvalid("custom 模板不需要供应商侧模板 ID")
		}
	}
	// IM 与 webhook 先约定一个模板只挂一个实例（应用层限制，不是库约束：日后要一对多，放开这里即可）。
	if !tpl.Channel.NeedsRecipient() {
		var others int
		if err := s.pool.QueryRow(ctx,
			`SELECT count(*) FROM notify_template_provider WHERE code = $1 AND provider_id <> $2`,
			code, providerID).Scan(&others); err != nil {
			return fmt.Errorf("service: 统计模板关联: %w", err)
		}
		if others > 0 {
			return linkInvalid("%s 渠道的模板只能关联一个供应商实例", tpl.Channel)
		}
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO notify_template_provider (code, provider_id, provider_template_id, enabled, priority)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (code, provider_id) DO UPDATE
		SET provider_template_id = EXCLUDED.provider_template_id,
		    enabled = EXCLUDED.enabled, priority = EXCLUDED.priority`,
		code, providerID, ptid, in.Enabled, in.Priority); err != nil {
		return fmt.Errorf("service: 保存模板关联: %w", err)
	}
	return nil
}

func (s *NotifyService) RemoveTemplateProvider(ctx context.Context, code string, providerID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM notify_template_provider WHERE code = $1 AND provider_id = $2`, code, providerID)
	if err != nil {
		return fmt.Errorf("service: 解除模板关联: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.Failf(domain.ErrNotFound, domain.CodeNotifyProviderNotFound, "模板 %q 没有关联这个供应商", code)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 发送记录
// ---------------------------------------------------------------------------

type NotifyLogFilter struct {
	Code    string
	Success *bool
	Limit   int
	Offset  int
}

func (s *NotifyService) ListLogs(ctx context.Context, f NotifyLogFilter) ([]domain.NotifyLog, int, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := max(f.Offset, 0)

	where, args := "WHERE true", []any{}
	if f.Code != "" {
		args = append(args, f.Code)
		where += fmt.Sprintf(" AND code = $%d", len(args))
	}
	if f.Success != nil {
		args = append(args, *f.Success)
		where += fmt.Sprintf(" AND success = $%d", len(args))
	}

	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM notify_log `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("service: 统计发送记录: %w", err)
	}
	args = append(args, limit, offset)
	rows, err := s.pool.Query(ctx, `
		SELECT id, channel, target, code, provider, provider_id, app_id, success, error, created_at
		FROM notify_log `+where+fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)),
		args...)
	if err != nil {
		return nil, 0, fmt.Errorf("service: 查询发送记录: %w", err)
	}
	defer rows.Close()
	out := []domain.NotifyLog{}
	for rows.Next() {
		var l domain.NotifyLog
		if err := rows.Scan(&l.ID, &l.Channel, &l.Target, &l.Code, &l.Provider, &l.ProviderID,
			&l.AppID, &l.Success, &l.Error, &l.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("service: 扫描发送记录: %w", err)
		}
		out = append(out, l)
	}
	return out, total, rows.Err()
}
