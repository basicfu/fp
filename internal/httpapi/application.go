package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

type applicationHandler struct {
	svc *service.ApplicationService
}

type sessionPolicyDTO struct {
	IdleTimeoutSeconds       int32 `json:"idleTimeoutSeconds"`
	IdleTimeoutMobileSeconds int32 `json:"idleTimeoutMobileSeconds"`
	MaxLifetimeSeconds       int32 `json:"maxLifetimeSeconds"`
	RotateIntervalSeconds    int32 `json:"rotateIntervalSeconds"`
	ExtendIntervalSeconds    int32 `json:"extendIntervalSeconds"`
	TokenCacheTTLSeconds     int32 `json:"tokenCacheTtlSeconds"`
}

type applicationDTO struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Slug         string           `json:"slug"`
	AppID        string           `json:"appId"`
	Status       string           `json:"status"`
	CookieDomain string           `json:"cookieDomain"`
	Session      sessionPolicyDTO `json:"session"`
	CreatedAt    int64            `json:"createdAt"`
	UpdatedAt    int64            `json:"updatedAt"`
}

func toApplicationDTO(a domain.Application) applicationDTO {
	return applicationDTO{
		ID: a.ID.String(), Name: a.Name, Slug: a.Slug, AppID: a.AppID,
		Status: a.Status, CookieDomain: a.CookieDomain,
		Session: sessionPolicyDTO{
			IdleTimeoutSeconds:       a.Session.IdleTimeoutSeconds,
			IdleTimeoutMobileSeconds: a.Session.IdleTimeoutMobileSeconds,
			MaxLifetimeSeconds:       a.Session.MaxLifetimeSeconds,
			RotateIntervalSeconds:    a.Session.RotateIntervalSeconds,
			ExtendIntervalSeconds:    a.Session.ExtendIntervalSeconds,
			TokenCacheTTLSeconds:     a.Session.TokenCacheTTLSeconds,
		},
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
}

func (h *applicationHandler) list(w http.ResponseWriter, r *http.Request) {
	apps, err := h.svc.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]applicationDTO, 0, len(apps))
	for _, a := range apps {
		out = append(out, toApplicationDTO(a))
	}
	writeJSON(w, http.StatusOK, out)
}

type createApplicationRequest struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type createApplicationResponse struct {
	Application applicationDTO `json:"application"`
	// AppSecret 是明文密钥，只在创建时返回这一次，之后无法读回。
	AppSecret string `json:"appSecret"`
}

func (h *applicationHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createApplicationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	app, secret, err := h.svc.Create(r.Context(), req.Name, req.Slug)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, createApplicationResponse{
		Application: toApplicationDTO(*app),
		AppSecret:   secret,
	})
}

func (h *applicationHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	app, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toApplicationDTO(*app))
}

// updateSession 全量替换应用的会话策略。六个字段必须全部给出——
// 少给任何一项都会被 SessionPolicy.Validate 拒绝，所以路由用的是 PUT 而非 PATCH。
func (h *applicationHandler) updateSession(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	var req sessionPolicyDTO
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	app, err := h.svc.UpdateSessionPolicy(r.Context(), id, domain.SessionPolicy{
		IdleTimeoutSeconds:       req.IdleTimeoutSeconds,
		IdleTimeoutMobileSeconds: req.IdleTimeoutMobileSeconds,
		MaxLifetimeSeconds:       req.MaxLifetimeSeconds,
		RotateIntervalSeconds:    req.RotateIntervalSeconds,
		ExtendIntervalSeconds:    req.ExtendIntervalSeconds,
		TokenCacheTTLSeconds:     req.TokenCacheTTLSeconds,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toApplicationDTO(*app))
}

// updateApplicationRequest 两个字段都用指针：json 包对缺失字段/显式 null
// 都会把指针留成 nil，只有真正带了值（哪怕是空字符串）才会分配非 nil
// 指针——借此把"没传，不改这个字段"和"传了空字符串，把它清空"区分开，
// 原样透传给 service.Update，不在这一层补默认值或做转换。
type updateApplicationRequest struct {
	Name         *string `json:"name"`
	CookieDomain *string `json:"cookieDomain"`
}

// update 局部修改应用展示名与/或 cookie 作用域。
// 会话策略与启停各有自己的接口，这里不受理——理由见 service.Update 的注释。
func (h *applicationHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	var req updateApplicationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	app, err := h.svc.Update(r.Context(), id, req.Name, req.CookieDomain)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toApplicationDTO(*app))
}

type setApplicationStatusRequest struct {
	Status string `json:"status"`
}

func (h *applicationHandler) setStatus(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	var req setApplicationStatusRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	app, err := h.svc.SetStatus(r.Context(), id, req.Status)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toApplicationDTO(*app))
}

type connectorDTO struct {
	Type    string         `json:"type"`
	Enabled bool           `json:"enabled"`
	Config  map[string]any `json:"config"`
}

func (h *applicationHandler) listConnectors(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	list, err := h.svc.ListConnectors(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]connectorDTO, 0, len(list))
	for _, c := range list {
		out = append(out, connectorDTO{Type: c.Type, Enabled: c.Enabled, Config: c.Config})
	}
	writeJSON(w, http.StatusOK, out)
}

type putConnectorRequest struct {
	Enabled bool           `json:"enabled"`
	Config  map[string]any `json:"config"`
}

func (h *applicationHandler) putConnector(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	var req putConnectorRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := h.svc.SetConnector(r.Context(), id, chi.URLParam(r, "type"), req.Enabled, req.Config); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// pathUUID 从 chi 路径参数解析 UUID。
func pathUUID(r *http.Request, key string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, key))
	if err != nil {
		return uuid.Nil, domain.Errorf(domain.ErrInvalidArgument, "路径参数 %s 不是合法 UUID", key)
	}
	return id, nil
}
