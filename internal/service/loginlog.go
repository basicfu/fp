package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/domain"
)

const loginLogColumns = `
	id, user_id, application_id, identity_type, subject,
	event, success, reason, ip, ua, session_id,
	(extract(epoch from created_at) * 1000)::bigint`

// LoginLogService 读写登录审计记录。
type LoginLogService struct {
	pool *pgxpool.Pool
}

// NewLoginLogService 构造 LoginLogService。
func NewLoginLogService(pool *pgxpool.Pool) *LoginLogService {
	return &LoginLogService{pool: pool}
}

// Write 写入一条审计记录。
func (s *LoginLogService) Write(ctx context.Context, e domain.LoginLog) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO login_log
			(user_id, application_id, identity_type, subject, event, success, reason, ip, ua, session_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		e.UserID, e.ApplicationID, e.IdentityType, e.Subject,
		e.Event, e.Success, e.Reason, e.IP, e.UA, e.SessionID)
	if err != nil {
		return fmt.Errorf("service: 写入登录日志: %w", err)
	}
	return nil
}

// ListByUser 返回该用户最近的审计记录，按时间倒序。
func (s *LoginLogService) ListByUser(ctx context.Context, userID uuid.UUID, limit int) ([]domain.LoginLog, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+loginLogColumns+` FROM login_log
		 WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("service: 查询登录日志: %w", err)
	}
	defer rows.Close()

	out := []domain.LoginLog{}
	for rows.Next() {
		var e domain.LoginLog
		if err := rows.Scan(&e.ID, &e.UserID, &e.ApplicationID, &e.IdentityType, &e.Subject,
			&e.Event, &e.Success, &e.Reason, &e.IP, &e.UA, &e.SessionID, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("service: 扫描登录日志: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service: 遍历登录日志: %w", err)
	}
	return out, nil
}

// MaskSubject 按标识类型脱敏。审计日志里保留可辨识度即可，不需要完整值。
func MaskSubject(identityType, subject string) string {
	if subject == "" {
		return ""
	}
	switch identityType {
	case domain.IdentityTypePhone:
		if len(subject) != 11 {
			return maskTail(subject)
		}
		return subject[:3] + "****" + subject[7:]
	case domain.IdentityTypeEmail:
		at := strings.LastIndex(subject, "@")
		if at <= 0 {
			return maskTail(subject)
		}
		local, domainPart := subject[:at], subject[at:]
		if len(local) <= 1 {
			return "*" + domainPart
		}
		return local[:1] + "****" + local[len(local)-1:] + domainPart
	default:
		return maskTail(subject)
	}
}

// maskTail 保留前两个字符，其余以 * 代替；长度不足 2 时全部替换。
func maskTail(s string) string {
	r := []rune(s)
	if len(r) <= 2 {
		return strings.Repeat("*", len(r))
	}
	return string(r[:2]) + strings.Repeat("*", len(r)-2)
}
