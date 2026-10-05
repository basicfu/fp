package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

type notifyHandler struct {
	svc *service.NotifyService
}

// ---------------------------------------------------------------------------
// DTO
// ---------------------------------------------------------------------------

type notifyProviderDTO struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Channel     string `json:"channel"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	// Config 里的 secret 字段已经脱敏成掩码；写回时原样传回掩码表示保持原值。
	Config    map[string]any `json:"config"`
	CreatedAt int64          `json:"createdAt"`
	UpdatedAt int64          `json:"updatedAt"`
}

type notifyProviderListDTO struct {
	notifyProviderDTO
	TemplateCount int `json:"templateCount"`
}

func (h *notifyHandler) providerDTO(p domain.NotifyProvider) notifyProviderDTO {
	return notifyProviderDTO{
		ID: p.ID.String(), Type: p.Type, Channel: string(h.svc.ProviderChannel(p)),
		Description: p.Description, Enabled: p.Enabled, Config: h.svc.MaskConfig(p),
		CreatedAt: p.CreatedAt.UnixMilli(), UpdatedAt: p.UpdatedAt.UnixMilli(),
	}
}

type notifyTemplateDTO struct {
	Code        string               `json:"code"`
	Channel     string               `json:"channel"`
	Mode        string               `json:"mode"`
	Content     domain.NotifyContent `json:"content"`
	Description string               `json:"description"`
	Enabled     bool                 `json:"enabled"`
	CreatedAt   int64                `json:"createdAt"`
	UpdatedAt   int64                `json:"updatedAt"`
}

func toNotifyTemplateDTO(t domain.NotifyTemplate) notifyTemplateDTO {
	return notifyTemplateDTO{
		Code: t.Code, Channel: string(t.Channel), Mode: string(t.Mode), Content: t.Content,
		Description: t.Description, Enabled: t.Enabled,
		CreatedAt: t.CreatedAt.UnixMilli(), UpdatedAt: t.UpdatedAt.UnixMilli(),
	}
}

type notifyLinkDTO struct {
	ProviderID          string `json:"providerId"`
	ProviderType        string `json:"providerType"`
	ProviderDescription string `json:"providerDescription"`
	ProviderEnabled     bool   `json:"providerEnabled"`
	ProviderTemplateID  string `json:"providerTemplateId"`
	Enabled             bool   `json:"enabled"`
	Priority            int    `json:"priority"`
}

type notifyTemplateDetailDTO struct {
	notifyTemplateDTO
	Providers []notifyLinkDTO `json:"providers"`
}

// ---------------------------------------------------------------------------
// 供应商
// ---------------------------------------------------------------------------

func (h *notifyHandler) providerTypes(w http.ResponseWriter, _ *http.Request) {
	types := h.svc.ProviderTypes()
	out := make([]map[string]any, 0, len(types))
	for _, t := range types {
		out = append(out, map[string]any{"type": t.Type, "channel": string(t.Channel), "fields": t.Fields})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *notifyHandler) listProviders(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListProviders(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]notifyProviderListDTO, 0, len(list))
	for _, v := range list {
		out = append(out, notifyProviderListDTO{notifyProviderDTO: h.providerDTO(v.NotifyProvider), TemplateCount: v.TemplateCount})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *notifyHandler) createProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type        string         `json:"type"`
		Description string         `json:"description"`
		Enabled     *bool          `json:"enabled"`
		Config      map[string]any `json:"config"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	p, err := h.svc.CreateProvider(r.Context(), service.CreateNotifyProviderInput{
		Type: req.Type, Description: req.Description, Enabled: req.Enabled == nil || *req.Enabled, Config: req.Config,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, h.providerDTO(*p))
}

func (h *notifyHandler) getProvider(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	p, err := h.svc.GetProvider(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.providerDTO(*p))
}

// updateProvider 是局部更新：省略或为 null 的字段不改；config 省略表示不改配置，
// 给了就是整份替换（secret 字段传回掩码表示保持原值）。
func (h *notifyHandler) updateProvider(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	var req struct {
		Description *string        `json:"description"`
		Enabled     *bool          `json:"enabled"`
		Config      map[string]any `json:"config"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	p, err := h.svc.UpdateProvider(r.Context(), id, service.UpdateNotifyProviderInput{
		Description: req.Description, Enabled: req.Enabled, Config: req.Config,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.providerDTO(*p))
}

func (h *notifyHandler) deleteProvider(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	if err := h.svc.DeleteProvider(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func (h *notifyHandler) providerTemplates(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	usages, err := h.svc.ProviderUsages(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(usages))
	for _, u := range usages {
		out = append(out, map[string]any{
			"code": u.Code, "channel": string(u.Channel), "templateEnabled": u.TemplateEnabled,
			"providerTemplateId": u.ProviderTemplateID, "enabled": u.LinkEnabled, "priority": u.Priority,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// 模板
// ---------------------------------------------------------------------------

func (h *notifyHandler) listTemplates(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListTemplates(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, v := range list {
		out = append(out, map[string]any{
			"code": v.Code, "channel": string(v.Channel), "mode": string(v.Mode), "description": v.Description,
			"enabled": v.Enabled, "providerCount": v.ProviderCount, "updatedAt": v.UpdatedAt.UnixMilli(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *notifyHandler) createTemplate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code        string               `json:"code"`
		Channel     domain.NotifyChannel `json:"channel"`
		Mode        domain.NotifyMode    `json:"mode"`
		Content     domain.NotifyContent `json:"content"`
		Description string               `json:"description"`
		Enabled     *bool                `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	t, err := h.svc.CreateTemplate(r.Context(), service.CreateNotifyTemplateInput{
		Code: req.Code, Channel: req.Channel, Mode: req.Mode, Content: req.Content,
		Description: req.Description, Enabled: req.Enabled == nil || *req.Enabled,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toNotifyTemplateDTO(*t))
}

func (h *notifyHandler) getTemplate(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.GetTemplate(r.Context(), chi.URLParam(r, "code"))
	if err != nil {
		writeError(w, err)
		return
	}
	links := make([]notifyLinkDTO, 0, len(d.Links))
	for _, l := range d.Links {
		links = append(links, notifyLinkDTO{
			ProviderID: l.ProviderID.String(), ProviderType: l.ProviderType,
			ProviderDescription: l.ProviderDescription, ProviderEnabled: l.ProviderEnabled,
			ProviderTemplateID: l.ProviderTemplateID, Enabled: l.Enabled, Priority: l.Priority,
		})
	}
	writeJSON(w, http.StatusOK, notifyTemplateDetailDTO{notifyTemplateDTO: toNotifyTemplateDTO(d.Template), Providers: links})
}

// updateTemplate 是局部更新：省略或为 null 的字段不改。code、channel、mode 不可改，
// 请求体里带这些字段会被 DisallowUnknownFields 当成 400。
func (h *notifyHandler) updateTemplate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content     *domain.NotifyContent `json:"content"`
		Description *string               `json:"description"`
		Enabled     *bool                 `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	t, err := h.svc.UpdateTemplate(r.Context(), chi.URLParam(r, "code"), service.UpdateNotifyTemplateInput{
		Content: req.Content, Description: req.Description, Enabled: req.Enabled,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toNotifyTemplateDTO(*t))
}

func (h *notifyHandler) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteTemplate(r.Context(), chi.URLParam(r, "code")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// setLink 是该关联的全量替换（新增与修改都走它）。
func (h *notifyHandler) setLink(w http.ResponseWriter, r *http.Request) {
	providerID, err := pathUUID(r, "providerId")
	if err != nil {
		writeError(w, err)
		return
	}
	var req struct {
		ProviderTemplateID string `json:"providerTemplateId"`
		Enabled            bool   `json:"enabled"`
		Priority           int    `json:"priority"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := h.svc.SetTemplateProvider(r.Context(), chi.URLParam(r, "code"), providerID, service.SetNotifyLinkInput{
		ProviderTemplateID: req.ProviderTemplateID, Enabled: req.Enabled, Priority: req.Priority,
	}); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func (h *notifyHandler) removeLink(w http.ResponseWriter, r *http.Request) {
	providerID, err := pathUUID(r, "providerId")
	if err != nil {
		writeError(w, err)
		return
	}
	if err := h.svc.RemoveTemplateProvider(r.Context(), chi.URLParam(r, "code"), providerID); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// testSend 从控制台试发一条：走与业务方调用完全相同的 Send 路径，只是 appId 为空。
// 失败时只回通用错误，具体原因在「发送记录」里。
func (h *notifyHandler) testSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		To     string            `json:"to"`
		Params map[string]string `json:"params"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := h.svc.Send(r.Context(), service.NotifySendInput{
		Code: chi.URLParam(r, "code"), To: req.To, Params: req.Params,
	}); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// ---------------------------------------------------------------------------
// 发送记录
// ---------------------------------------------------------------------------

func (h *notifyHandler) listLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := service.NotifyLogFilter{Code: q.Get("code"), Limit: limit, Offset: offset}
	switch q.Get("success") {
	case "true":
		t := true
		f.Success = &t
	case "false":
		t := false
		f.Success = &t
	}
	logs, total, err := h.svc.ListLogs(r.Context(), f)
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(logs))
	for _, l := range logs {
		providerID := ""
		if l.ProviderID != nil {
			providerID = l.ProviderID.String()
		}
		items = append(items, map[string]any{
			"id": l.ID.String(), "channel": l.Channel, "target": l.Target, "code": l.Code,
			"provider": l.Provider, "providerId": providerID, "appId": l.AppID,
			"success": l.Success, "error": l.Error, "createdAt": l.CreatedAt.UnixMilli(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}
