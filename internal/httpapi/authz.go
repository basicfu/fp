package httpapi

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

type authzHandler struct {
	svc  *service.AuthzService
	apps *service.ApplicationService
}

// ---------------------------------------------------------------------------
// 角色
// ---------------------------------------------------------------------------

type roleDTO struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
	// ParentID 为空串表示没有父角色。
	ParentID  string `json:"parentId"`
	CreatedAt int64  `json:"createdAt"`
}

func toRoleDTO(r domain.Role) roleDTO {
	out := roleDTO{ID: r.ID.String(), Key: r.Key, Name: r.Name, CreatedAt: r.CreatedAt}
	if r.ParentID != nil {
		out.ParentID = r.ParentID.String()
	}
	return out
}

func (h *authzHandler) listRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := h.svc.ListRoles(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]roleDTO, 0, len(roles))
	for _, x := range roles {
		out = append(out, toRoleDTO(x))
	}
	writeJSON(w, http.StatusOK, out)
}

type roleRequest struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	ParentID string `json:"parentId"`
}

func (h *authzHandler) createRole(w http.ResponseWriter, r *http.Request) {
	var req roleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	parent, err := optionalUUID(req.ParentID)
	if err != nil {
		writeError(w, err)
		return
	}
	role, err := h.svc.CreateRole(r.Context(), req.Key, req.Name, parent)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toRoleDTO(*role))
}

// updateRole 只改显示名与父角色。
//
// **key 不在可改之列**——user_role.roles 按字符串引用它，且已签发的会话里
// 刻着它。请求体里即使带了 key 也会被 DisallowUnknownFields 拒掉。
func (h *authzHandler) updateRole(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	var req struct {
		Name     string `json:"name"`
		ParentID string `json:"parentId"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	parent, err := optionalUUID(req.ParentID)
	if err != nil {
		writeError(w, err)
		return
	}
	role, err := h.svc.UpdateRole(r.Context(), id, req.Name, parent)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toRoleDTO(*role))
}

func (h *authzHandler) deleteRole(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	if err := h.svc.DeleteRole(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// ---------------------------------------------------------------------------
// 权限点
// ---------------------------------------------------------------------------

type permissionDTO struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Source 为 app（SDK 上报）或 manual（人手动加）。
	Source string `json:"source"`
	// Status 为 normal / stale（过渡中）/ manual。它是**算出来的**，不存库。
	Status string `json:"status"`
	// StaleForMs 是"过渡中"已经持续了多久，其余状态为 0。
	// 控制台据此显示"已过渡 N 小时"，让人判断该不该删。
	StaleForMs int64 `json:"staleForMs"`
	LastSeenAt int64 `json:"lastSeenAt"`
	CreatedAt  int64 `json:"createdAt"`
}

func (h *authzHandler) listPermissions(w http.ResponseWriter, r *http.Request) {
	appID, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	items, err := h.svc.ListPermissions(r.Context(), appID)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]permissionDTO, 0, len(items))
	for _, it := range items {
		out = append(out, permissionDTO{
			ID: it.Permission.ID.String(), Key: it.Permission.Key,
			Name: it.Permission.Name, Kind: it.Permission.Kind,
			Source: it.Permission.Source, Status: string(it.Status),
			StaleForMs: it.StaleFor.Milliseconds(),
			LastSeenAt: it.Permission.LastSeenAt, CreatedAt: it.Permission.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *authzHandler) createPermission(w http.ResponseWriter, r *http.Request) {
	appID, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	var req struct {
		Key  string `json:"key"`
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	p, err := h.svc.CreatePermission(r.Context(), appID, req.Key, req.Name, req.Kind)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, permissionDTO{
		ID: p.ID.String(), Key: p.Key, Name: p.Name, Kind: p.Kind,
		Source: p.Source, Status: string(domain.PermissionStatusManual),
		CreatedAt: p.CreatedAt,
	})
}

// updatePermission 改权限点的 key 与显示名。
//
// key **可以改**——role_permission 按 id 引用，授权关系自动跟随。
func (h *authzHandler) updatePermission(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "pid")
	if err != nil {
		writeError(w, err)
		return
	}
	var req struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	p, err := h.svc.UpdatePermission(r.Context(), id, req.Key, req.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	now := time.Now()
	writeJSON(w, http.StatusOK, permissionDTO{
		ID: p.ID.String(), Key: p.Key, Name: p.Name, Kind: p.Kind,
		Source: p.Source, Status: string(p.Status(now, domain.DefaultStaleAfter)),
		LastSeenAt: p.LastSeenAt, CreatedAt: p.CreatedAt,
	})
}

func (h *authzHandler) deletePermission(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "pid")
	if err != nil {
		writeError(w, err)
		return
	}
	if err := h.svc.DeletePermission(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// permissionHolders 返回持有某个权限点的角色。
//
// 控制台在删除前必须先调它并提示"当前有 N 个角色持有它"——这是唯一的安全网，
// 因为设计上没有可逆的"停用"中间态，删除就是真删（连带删授权关系）。
func (h *authzHandler) permissionHolders(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "pid")
	if err != nil {
		writeError(w, err)
		return
	}
	keys, err := h.svc.RolesHolding(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": keys})
}

// ---------------------------------------------------------------------------
// 授权与用户角色
// ---------------------------------------------------------------------------

func (h *authzHandler) setRolePermission(w http.ResponseWriter, r *http.Request) {
	roleID, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	permID, err := pathUUID(r, "pid")
	if err != nil {
		writeError(w, err)
		return
	}
	var req struct {
		// Effect 为 allow / deny；空串表示收回授权。
		Effect string `json:"effect"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := h.svc.SetRolePermission(r.Context(), roleID, permID, req.Effect); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func (h *authzHandler) getUserRoles(w http.ResponseWriter, r *http.Request) {
	userID, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	roles, err := h.svc.UserRoles(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": roles})
}

func (h *authzHandler) setUserRoles(w http.ResponseWriter, r *http.Request) {
	userID, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	var req struct {
		Roles []string `json:"roles"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := h.svc.SetUserRoles(r.Context(), userID, req.Roles); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// setDefaultRole 设置应用的默认角色。
//
// 有效角色 = 用户的全局角色 ∪ 该应用的默认角色。空串表示不设默认角色。
func (h *authzHandler) setDefaultRole(w http.ResponseWriter, r *http.Request) {
	appID, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	var req struct {
		RoleKey string `json:"roleKey"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	app, err := h.apps.SetDefaultRole(r.Context(), appID, req.RoleKey)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toApplicationDTO(*app))
}

// optionalUUID 把可选的 UUID 字符串解析成指针。空串返回 nil。
func optionalUUID(s string) (*uuid.UUID, error) {
	if s == "" {
		return nil, nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return nil, domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument,
			"%q 不是合法的 UUID", s)
	}
	return &id, nil
}

// roleGrants 返回某个角色直接持有的授权关系，供控制台的授权编辑器回显。
//
// 返回的是 permissionId → effect，不含权限点本身的字段：控制台已经通过
// /applications/{id}/permissions 拿到了那个应用的权限点列表，这里只补
// "哪些被授了、是 allow 还是 deny"。
func (h *authzHandler) roleGrants(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	grants, err := h.svc.RoleGrants(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]map[string]string, 0, len(grants))
	for _, g := range grants {
		out = append(out, map[string]string{
			"permissionId": g.PermissionID.String(),
			"effect":       g.Effect,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"grants": out})
}
