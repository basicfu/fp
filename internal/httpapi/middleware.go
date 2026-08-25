package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/service"
)

type ctxKey int

const (
	ctxKeyAdminID ctxKey = iota
	ctxKeyAdminName
	ctxKeyAdminToken
)

// adminTokenCookie 是管理端会话 cookie 名。与业务侧的 token 完全分离。
const adminTokenCookie = "fp_admin"

// requireAdmin 校验管理端凭据，通过后把管理员信息放进请求 context。
// 凭据优先取 Authorization: Bearer，其次取 cookie。
func requireAdmin(admin *service.AdminService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerToken(r)
			if token == "" {
				if c, err := r.Cookie(adminTokenCookie); err == nil {
					token = c.Value
				}
			}
			id, name, err := admin.Authenticate(r.Context(), token)
			if err != nil {
				writeError(w, err)
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeyAdminID, id)
			ctx = context.WithValue(ctx, ctxKeyAdminName, name)
			ctx = context.WithValue(ctx, ctxKeyAdminToken, token)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	return ""
}

func adminIDFrom(ctx context.Context) uuid.UUID {
	id, _ := ctx.Value(ctxKeyAdminID).(uuid.UUID)
	return id
}

func adminNameFrom(ctx context.Context) string {
	name, _ := ctx.Value(ctxKeyAdminName).(string)
	return name
}

func adminTokenFrom(ctx context.Context) string {
	token, _ := ctx.Value(ctxKeyAdminToken).(string)
	return token
}
