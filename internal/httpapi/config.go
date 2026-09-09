package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

type configHandler struct {
	svc *service.ConfigService
}

// configDTO 是一个分区的一份快照——Value 是 YAML 原文，原样透传。
type configDTO struct {
	Seq   int64  `json:"seq"`
	Value string `json:"value"`
}

type configVersionDTO struct {
	Seq       int64 `json:"seq"`
	CreatedAt int64 `json:"createdAt"`
}

type saveConfigRequest struct {
	Type  string `json:"type"`
	Value string `json:"value"`
	// Push 为 true 立即推送；为 false 就是「仅落库，实例重启后生效」。
	Push bool `json:"push"`
}

type rollbackConfigRequest struct {
	Type string `json:"type"`
	Seq  int64  `json:"seq"`
	Push bool   `json:"push"`
}

type saveConfigResponse struct {
	Seq int64 `json:"seq"`
}

func toConfigDTO(c domain.Config) configDTO {
	return configDTO{Seq: c.Seq, Value: c.Value}
}

func (h *configHandler) get(w http.ResponseWriter, r *http.Request) {
	appID, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	cfg, err := h.svc.Current(r.Context(), appID, r.URL.Query().Get("type"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toConfigDTO(cfg))
}

func (h *configHandler) save(w http.ResponseWriter, r *http.Request) {
	appID, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	var req saveConfigRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	seq, err := h.svc.Save(r.Context(), appID, req.Type, req.Value, req.Push)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saveConfigResponse{Seq: seq})
}

func (h *configHandler) listVersions(w http.ResponseWriter, r *http.Request) {
	appID, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	vs, err := h.svc.ListVersions(r.Context(), appID, r.URL.Query().Get("type"), 20)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]configVersionDTO, 0, len(vs))
	for _, v := range vs {
		out = append(out, configVersionDTO{Seq: v.Seq, CreatedAt: v.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *configHandler) getVersion(w http.ResponseWriter, r *http.Request) {
	appID, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	// strconv.ParseInt 而不是 fmt.Sscanf：Sscanf 只要求前缀匹配 %d，
	// "12abc" 会被它当成合法输入静默截成 12，ParseInt 才会把整串校验一遍。
	seq, err := strconv.ParseInt(chi.URLParam(r, "seq"), 10, 64)
	if err != nil {
		writeError(w, domain.Fail(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "版本号不合法"))
		return
	}
	cfg, err := h.svc.Version(r.Context(), appID, r.URL.Query().Get("type"), seq)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toConfigDTO(cfg))
}

func (h *configHandler) rollback(w http.ResponseWriter, r *http.Request) {
	appID, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	var req rollbackConfigRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	seq, err := h.svc.Rollback(r.Context(), appID, req.Type, req.Seq, req.Push)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saveConfigResponse{Seq: seq})
}

// listTypes 列出这个应用下保存过至少一个版本的分区名，供控制台画标签页
// 用。DEFAULT 标签页永远展示这条 UI 规则由前端自己兜底，这里如实返回
// 数据库里有什么。
func (h *configHandler) listTypes(w http.ResponseWriter, r *http.Request) {
	appID, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	types, err := h.svc.ListTypes(r.Context(), appID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, types)
}

// deleteType 删除该分区**全部**版本，包括历史——控制台在调用前必须先
// 弹二次确认，说清楚这是不可撤销的。
func (h *configHandler) deleteType(w http.ResponseWriter, r *http.Request) {
	appID, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	if err := h.svc.DeleteType(r.Context(), appID, r.URL.Query().Get("type")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}
