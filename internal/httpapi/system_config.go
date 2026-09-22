package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

type systemConfigHandler struct {
	svc *service.SystemConfigService
}

// systemConfigDTO 是系统配置的一份快照——Value 是 YAML 原文，原样透传。
type systemConfigDTO struct {
	Seq   int64  `json:"seq"`
	Value string `json:"value"`
}

type systemConfigVersionDTO struct {
	Seq       int64 `json:"seq"`
	CreatedAt int64 `json:"createdAt"`
}

type saveSystemConfigRequest struct {
	Value string `json:"value"`
}

type rollbackSystemConfigRequest struct {
	Seq int64 `json:"seq"`
}

type saveSystemConfigResponse struct {
	Seq int64 `json:"seq"`
}

func toSystemConfigDTO(c domain.SystemConfig) systemConfigDTO {
	return systemConfigDTO{Seq: c.Seq, Value: c.Value}
}

func (h *systemConfigHandler) get(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.svc.Current(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSystemConfigDTO(cfg))
}

func (h *systemConfigHandler) save(w http.ResponseWriter, r *http.Request) {
	var req saveSystemConfigRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	seq, err := h.svc.Save(r.Context(), req.Value)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saveSystemConfigResponse{Seq: seq})
}

func (h *systemConfigHandler) listVersions(w http.ResponseWriter, r *http.Request) {
	vs, err := h.svc.ListVersions(r.Context(), 20)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]systemConfigVersionDTO, 0, len(vs))
	for _, v := range vs {
		out = append(out, systemConfigVersionDTO{Seq: v.Seq, CreatedAt: v.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *systemConfigHandler) getVersion(w http.ResponseWriter, r *http.Request) {
	seq, err := strconv.ParseInt(chi.URLParam(r, "seq"), 10, 64)
	if err != nil {
		writeError(w, domain.Fail(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "版本号不合法"))
		return
	}
	cfg, err := h.svc.Version(r.Context(), seq)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSystemConfigDTO(cfg))
}

func (h *systemConfigHandler) rollback(w http.ResponseWriter, r *http.Request) {
	var req rollbackSystemConfigRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	seq, err := h.svc.Rollback(r.Context(), req.Seq)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saveSystemConfigResponse{Seq: seq})
}
