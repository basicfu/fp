package httpapi

import (
	"net/http"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
)

type connectorHandler struct {
	registry *connector.Registry
}

type connectorSchemaDTO struct {
	Type   string         `json:"type"`
	Fields []domain.Field `json:"fields"`
}

// list 返回全部已注册登录方式的配置元数据。
// 管理 UI 靠它渲染动态表单——新增登录方式后前端零改动。
func (h *connectorHandler) list(w http.ResponseWriter, _ *http.Request) {
	schemas := h.registry.Schemas()
	out := make([]connectorSchemaDTO, 0, len(schemas))
	for _, typ := range h.registry.Types() {
		fields := schemas[typ]
		if fields == nil {
			fields = []domain.Field{}
		}
		out = append(out, connectorSchemaDTO{Type: typ, Fields: fields})
	}
	writeJSON(w, http.StatusOK, out)
}
