package httpapi

import (
	"net/http"
	"time"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

type accessKeyHandler struct {
	svc   *service.AccessKeyService
	authz *service.AuthzService
}

type accessKeyDTO struct {
	ID          string   `json:"id"`
	AccessKeyID string   `json:"accessKeyId"`
	Remark      string   `json:"remark"`
	RoleKey     string   `json:"roleKey"`
	AllowedIPs  []string `json:"allowedIps"`
	Status      string   `json:"status"`
	// State 是算出来的展示状态：active / disabled / expired，停用优先于过期。
	State      string `json:"state"`
	ExpiresAt  int64  `json:"expiresAt"`
	LastUsedAt int64  `json:"lastUsedAt"`
	CreatedAt  int64  `json:"createdAt"`
	UpdatedAt  int64  `json:"updatedAt"`
}

func toAccessKeyDTO(k domain.AccessKey, now time.Time) accessKeyDTO {
	ips := make([]string, 0, len(k.AllowedIPs))
	for _, p := range k.AllowedIPs {
		ips = append(ips, p.String())
	}
	return accessKeyDTO{
		ID: k.ID.String(), AccessKeyID: k.AccessKeyID, Remark: k.Remark, RoleKey: k.RoleKey,
		AllowedIPs: ips, Status: k.Status, State: string(k.State(now)),
		ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt, CreatedAt: k.CreatedAt, UpdatedAt: k.UpdatedAt,
	}
}

func (h *accessKeyHandler) list(w http.ResponseWriter, r *http.Request) {
	keys, err := h.svc.List(r.Context(), r.URL.Query().Get("roleKey"))
	if err != nil {
		writeError(w, err)
		return
	}
	now := time.Now()
	out := make([]accessKeyDTO, 0, len(keys))
	for _, k := range keys {
		out = append(out, toAccessKeyDTO(k, now))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *accessKeyHandler) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Remark     string   `json:"remark"`
		RoleKey    string   `json:"roleKey"`
		ValidDays  int      `json:"validDays"`
		AllowedIPs []string `json:"allowedIps"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	k, err := h.svc.Create(r.Context(), service.CreateAccessKeyInput{
		Remark: req.Remark, RoleKey: req.RoleKey, ValidDays: req.ValidDays, AllowedIPs: req.AllowedIPs,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	// SK 只在创建响应里出现这一次。
	writeJSON(w, http.StatusCreated, map[string]any{"accessKey": toAccessKeyDTO(*k, time.Now()), "secret": k.Secret})
}

func (h *accessKeyHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	k, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toAccessKeyDTO(*k, time.Now()))
}

// update 是局部更新：省略或为 null 的字段不改。roleKey 传空串表示解绑。
func (h *accessKeyHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	var req struct {
		Remark     *string   `json:"remark"`
		RoleKey    *string   `json:"roleKey"`
		ValidDays  *int      `json:"validDays"`
		AllowedIPs *[]string `json:"allowedIps"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	k, err := h.svc.Update(r.Context(), id, service.UpdateAccessKeyInput{
		Remark: req.Remark, RoleKey: req.RoleKey, ValidDays: req.ValidDays, AllowedIPs: req.AllowedIPs,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toAccessKeyDTO(*k, time.Now()))
}

func (h *accessKeyHandler) setStatus(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	k, err := h.svc.SetStatus(r.Context(), id, req.Status)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toAccessKeyDTO(*k, time.Now()))
}

func (h *accessKeyHandler) remove(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

type permissionRefDTO struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type appPermissionsDTO struct {
	AppID   string             `json:"appId"`
	AppName string             `json:"appName"`
	Points  []permissionRefDTO `json:"points"`
}

// permissions 列出这把 key 当前能调用的接口，按应用分组、继承已展开。
// 给"key 全局可用"配的可见性：角色在别的应用多了授权，这里一眼能看到。
func (h *accessKeyHandler) permissions(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	k, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	groups, err := h.authz.RolePermissionsByApp(r.Context(), k.RoleKey)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]appPermissionsDTO, 0, len(groups))
	for _, g := range groups {
		pts := make([]permissionRefDTO, 0, len(g.Points))
		for _, p := range g.Points {
			pts = append(pts, permissionRefDTO{Key: p.Key, Name: p.Name})
		}
		out = append(out, appPermissionsDTO{AppID: g.AppID.String(), AppName: g.AppName, Points: pts})
	}
	writeJSON(w, http.StatusOK, out)
}
