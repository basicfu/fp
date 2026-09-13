package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

type accessKeyJSON struct {
	ID          string   `json:"id"`
	AccessKeyID string   `json:"accessKeyId"`
	Remark      string   `json:"remark"`
	AllowedIPs  []string `json:"allowedIps"`
	State       string   `json:"state"`
	ExpiresAt   int64    `json:"expiresAt"`
}

func TestAccessKeyCRUDOverHTTP(t *testing.T) {
	h, token, _ := newAdminEnv(t)

	rec := do(t, h, token, http.MethodPost, "/admin/api/access-keys",
		`{"remark":"顺丰","roleKey":"","validDays":7,"allowedIps":["1.2.3.4"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		AccessKey accessKeyJSON `json:"accessKey"`
		Secret    string        `json:"secret"`
	}
	decode(t, rec, &created)
	if created.Secret == "" || created.AccessKey.State != "active" ||
		len(created.AccessKey.AllowedIPs) != 1 || created.AccessKey.AllowedIPs[0] != "1.2.3.4/32" {
		t.Fatalf("创建响应不对: %s", rec.Body.String())
	}
	base := "/admin/api/access-keys/" + created.AccessKey.ID

	rec = do(t, h, token, http.MethodGet, "/admin/api/access-keys", "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), created.Secret) {
		t.Fatalf("列表不能带 SK: %d %s", rec.Code, rec.Body.String())
	}

	// 【辨别力】PATCH 里省略的字段不改。
	rec = do(t, h, token, http.MethodPatch, base, `{"remark":"顺丰速运"}`)
	var updated accessKeyJSON
	decode(t, rec, &updated)
	if updated.Remark != "顺丰速运" || updated.ExpiresAt != created.AccessKey.ExpiresAt || len(updated.AllowedIPs) != 1 {
		t.Fatalf("只改备注却动了别的字段: %s", rec.Body.String())
	}

	rec = do(t, h, token, http.MethodPatch, base+"/status", `{"status":"DISABLED"}`)
	var disabled accessKeyJSON
	decode(t, rec, &disabled)
	if disabled.State != "disabled" {
		t.Fatalf("停用后 state = %q", disabled.State)
	}

	if rec = do(t, h, token, http.MethodDelete, base, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, token, http.MethodGet, base, "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), domain.CodeAccessKeyNotFound) {
		t.Fatalf("删除后 get = %d %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteRoleBoundToAccessKeyReturns409(t *testing.T) {
	h, token, _ := newAdminEnv(t)
	rec := do(t, h, token, http.MethodPost, "/admin/api/roles", `{"key":"合作方","name":"合作方","parentId":""}`)
	var role struct {
		ID string `json:"id"`
	}
	decode(t, rec, &role)
	if rec = do(t, h, token, http.MethodPost, "/admin/api/access-keys",
		`{"remark":"r","roleKey":"合作方","validDays":0,"allowedIps":[]}`); rec.Code != http.StatusCreated {
		t.Fatalf("create key = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, token, http.MethodDelete, "/admin/api/roles/"+role.ID, "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), domain.CodeRoleInUse) {
		t.Fatalf("删除被绑定的角色 = %d %s", rec.Code, rec.Body.String())
	}
}

func TestAccessKeyPermissionsGroupedByApp(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	ctx := context.Background()
	appID := createAppID(t, h, token)
	role, err := deps.Authz.CreateRole(ctx, "合作方", "合作方", nil)
	if err != nil {
		t.Fatal(err)
	}
	perm, err := deps.Authz.CreatePermission(ctx, uuid.MustParse(appID), "GET:/orders/{id}", "查看订单", domain.PermissionKindAPI)
	if err != nil {
		t.Fatal(err)
	}
	if err := deps.Authz.SetRolePermission(ctx, role.ID, perm.ID, domain.EffectAllow); err != nil {
		t.Fatal(err)
	}
	k, err := deps.AccessKeys.Create(ctx, service.CreateAccessKeyInput{Remark: "r", RoleKey: role.Key})
	if err != nil {
		t.Fatal(err)
	}

	rec := do(t, h, token, http.MethodGet, "/admin/api/access-keys/"+k.ID.String()+"/permissions", "")
	var got []struct {
		AppID  string `json:"appId"`
		Points []struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"points"`
	}
	decode(t, rec, &got)
	if len(got) != 1 || got[0].AppID != appID || len(got[0].Points) != 1 ||
		got[0].Points[0].Key != "GET:/orders/{id}" || got[0].Points[0].Name != "查看订单" {
		t.Fatalf("按应用分组的接口不对: %s", rec.Body.String())
	}
}
