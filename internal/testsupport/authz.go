package testsupport

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewUserID 在库里插一个最小的用户并返回它的 ID。
//
// 授权相关的测试只需要一个合法的 user_id 用来挂角色，不需要走完整的注册
// 流程——那会牵进 connector、验证码、会话一大串东西，而这些测试跟它们无关。
func NewUserID(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO app_user (nickname) VALUES ('测试用户') RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("testsupport: 创建测试用户: %v", err)
	}
	return id
}
