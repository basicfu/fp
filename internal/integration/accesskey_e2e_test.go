package integration_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	fpsdk "github.com/basicfu/fp/sdk"
	"github.com/basicfu/fp/sdk/aksign"
	"github.com/basicfu/fp/sdk/authzcore"
	"github.com/basicfu/fp/sdk/fpchi"
)

// newBizServer 起一个"业务服务"：SDK 认证 + fpchi 鉴权，所有路由都挂鉴权（spec 的部署约定）。
func newBizServer(t *testing.T, client *fpsdk.Client) *httptest.Server {
	t.Helper()
	r := chi.NewRouter()
	r.Use(client.Auth().Middleware)
	r.Use(fpchi.New(client.Authz()).Middleware())
	r.Get("/orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := fpsdk.IdentityFrom(r.Context())
		_, _ = io.WriteString(w, id.AccessKeyID)
	})
	r.Post("/orders", func(http.ResponseWriter, *http.Request) {})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

// callBiz 发一个请求并返回状态码；ak 非空时用 aksign 签名。
func callBiz(t *testing.T, srv *httptest.Server, method, path, ak, sk string) int {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if ak != "" {
		if err := aksign.Sign(req, ak, sk); err != nil {
			t.Fatal(err)
		}
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// partnerKey 建角色「合作方」、授权 GET:/orders/{id}，再建一把绑定它、白名单为本机的 key。
func partnerKey(t *testing.T, e *phase2Env) (*domain.Role, *domain.Permission, *domain.AccessKey) {
	t.Helper()
	ctx := context.Background()
	role, err := e.authz.CreateRole(ctx, "合作方", "合作方", nil)
	if err != nil {
		t.Fatal(err)
	}
	perm, err := e.authz.CreatePermission(ctx, e.app.ID, "GET:/orders/{id}", "查看订单", domain.PermissionKindAPI)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.authz.SetRolePermission(ctx, role.ID, perm.ID, domain.EffectAllow); err != nil {
		t.Fatal(err)
	}
	k, err := e.accessKeys.Create(ctx, service.CreateAccessKeyInput{Remark: "顺丰", RoleKey: role.Code, AllowedIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	return role, perm, k
}

func TestAccessKeyEndToEnd(t *testing.T) {
	e := newPhase2Env(t)
	ctx := context.Background()
	role, perm, k := partnerKey(t, e)
	srv := newBizServer(t, e.sdk)
	get := func() int { return callBiz(t, srv, http.MethodGet, "/orders/1", k.AccessKeyID, k.Secret) }

	waitUntil(t, func() bool { return get() == http.StatusOK }, "授权后应能调通")

	// 【辨别力】收回授权不重启就生效：只有 PolicyChanged 推送能解释。
	if err := e.authz.SetRolePermission(ctx, role.ID, perm.ID, ""); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return get() == http.StatusForbidden }, "收回授权后应 403")
	if err := e.authz.SetRolePermission(ctx, role.ID, perm.ID, domain.EffectAllow); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return get() == http.StatusOK }, "重新授权后应恢复")

	// 【辨别力】停用在 30 秒缓存过期之前就生效：只有 AccessKeyChanged 推送能解释。
	if _, err := e.accessKeys.SetStatus(ctx, k.ID, domain.AccessKeyStatusDisabled); err != nil {
		t.Fatal(err)
	}
	waitUntilTimeout(t, 5*time.Second, func() bool { return get() == http.StatusForbidden }, "停用后应立即 403")
}

func TestAccessKeyLastUsedReportedOnClose(t *testing.T) {
	e := newPhase2Env(t)
	_, _, k := partnerKey(t, e)
	client := e.dial(t) // 单独一个客户端，Close 时会把使用时间报掉
	waitPolicy(t, client.Authz())
	srv := newBizServer(t, client)
	waitUntil(t, func() bool {
		return callBiz(t, srv, http.MethodGet, "/orders/1", k.AccessKeyID, k.Secret) == http.StatusOK
	}, "应能调通")
	_ = client.Close()

	got, err := e.accessKeys.Get(context.Background(), k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastUsedAt == 0 || got.LastUsedAt%60_000 != 0 {
		t.Fatalf("最后使用时间应已上报且精确到分钟，got %d", got.LastUsedAt)
	}
}

func TestGuestAndAccessKeyBoundaries(t *testing.T) {
	e := newPhase2Env(t)
	ctx := context.Background()
	// 测试库每次清空，迁移插入的 GUEST 不在，需要自己建。
	guest, err := e.authz.CreateRole(ctx, authzcore.GuestRoleKey, "访客", nil)
	if err != nil {
		t.Fatal(err)
	}
	view, err := e.authz.CreatePermission(ctx, e.app.ID, "GET:/orders/{id}", "查看订单", domain.PermissionKindAPI)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.authz.SetRolePermission(ctx, guest.ID, view.ID, domain.EffectAllow); err != nil {
		t.Fatal(err)
	}
	noRole, err := e.accessKeys.Create(ctx, service.CreateAccessKeyInput{Remark: "没绑角色"})
	if err != nil {
		t.Fatal(err)
	}
	srv := newBizServer(t, e.sdk)

	waitUntil(t, func() bool {
		return callBiz(t, srv, http.MethodGet, "/orders/1", "", "") == http.StatusOK
	}, "匿名请求应能调 GUEST 授权的接口")
	if code := callBiz(t, srv, http.MethodPost, "/orders", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("匿名调未授权接口应 401，got %d", code)
	}
	// 【辨别力】GUEST 开放的接口：没绑角色的 key 是 403，签错的 key 是 401，都不降级成匿名。
	if code := callBiz(t, srv, http.MethodGet, "/orders/1", noRole.AccessKeyID, noRole.Secret); code != http.StatusForbidden {
		t.Fatalf("没绑角色的 key 应 403，got %d", code)
	}
	if code := callBiz(t, srv, http.MethodGet, "/orders/1", noRole.AccessKeyID, "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("签错的 key 应 401，got %d", code)
	}
}
