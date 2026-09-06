package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

type configHandler struct {
	svc *service.ConfigService
}

// configFieldDTO 是 fields 里的一项。
//
// Value 用 json.RawMessage：值可能是数字、布尔、数组、对象中的任何一种，
// 转成 any 再转回来会把整数变成 float64、把字段顺序打乱。原样透传。
type configFieldDTO struct {
	Type  string          `json:"type"`
	Desc  string          `json:"desc"`
	Value json.RawMessage `json:"value"`
}

type configDTO struct {
	Seq    int64                     `json:"seq"`
	Fields map[string]configFieldDTO `json:"fields"`
}

type configVersionDTO struct {
	Seq       int64 `json:"seq"`
	CreatedAt int64 `json:"createdAt"`
}

type saveConfigRequest struct {
	Type   string                    `json:"type"`
	Fields map[string]configFieldDTO `json:"fields"`
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
	// 必须是非 nil 的空 map：nil 会被 encoding/json 编码成 null，
	// 前端 Object.entries(null) 直接抛异常。
	fields := make(map[string]configFieldDTO, len(c.Fields))
	for k, f := range c.Fields {
		fields[k] = configFieldDTO{Type: f.Type, Desc: f.Desc, Value: f.Value}
	}
	return configDTO{Seq: c.Seq, Fields: fields}
}

func toDomainFields(in map[string]configFieldDTO) map[string]domain.ConfigField {
	out := make(map[string]domain.ConfigField, len(in))
	for k, f := range in {
		out[k] = domain.ConfigField{Type: f.Type, Desc: f.Desc, Value: f.Value}
	}
	return out
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
	seq, err := h.svc.Save(r.Context(), appID, req.Type, toDomainFields(req.Fields), req.Push)
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
