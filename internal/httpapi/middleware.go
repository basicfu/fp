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

// xFrameOptionsDeny 给每个响应加 X-Frame-Options: DENY，禁止管理控制台被
// <iframe> 嵌套，防御 clickjacking。
//
// 全仓在这条分支之前没有任何浏览器 UI，缺这个头本身是基线遗留；但这条
// 分支第一次让 fp 有了能被点击的管理页面（停用应用、冻结账号……），缺
// 这个头就从"理论问题"变成了"已登录管理员访问一个恶意页面就可能被
// 诱导点击"的真实问题。之所以用中间件加在路由最外层而不是只在
// newStaticHandler 里加：/healthz 和 /admin/api 的 404 都不经过
// newStaticHandler，只有加在这里才能保证三类响应都带上。
func xFrameOptionsDeny(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

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
