// Package httpapi 是管理 UI 的 HTTP 传输层。它只做协议转换，不含业务逻辑。
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/basicfu/fp/internal/domain"
)

type errorBody struct {
	Error string `json:"error"`
}

// writeJSON 以 status 写出 JSON 响应。v 为 nil 时只写状态码。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("httpapi: 写出响应失败", "err", err)
	}
}

// writeError 把领域错误映射为 HTTP 状态码。
// 未识别的错误一律 500，且不把内部错误信息泄露给客户端。
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errorBody{Error: err.Error()})
	case errors.Is(err, domain.ErrInvalidCredential):
		writeJSON(w, http.StatusUnauthorized, errorBody{Error: err.Error()})
	case errors.Is(err, domain.ErrUnauthorized):
		writeJSON(w, http.StatusUnauthorized, errorBody{Error: err.Error()})
	case errors.Is(err, domain.ErrForbidden):
		writeJSON(w, http.StatusForbidden, errorBody{Error: err.Error()})
	case errors.Is(err, domain.ErrConflict):
		writeJSON(w, http.StatusConflict, errorBody{Error: err.Error()})
	case errors.Is(err, domain.ErrInvalidArgument):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: err.Error()})
	case errors.Is(err, domain.ErrRateLimited):
		writeJSON(w, http.StatusTooManyRequests, errorBody{Error: err.Error()})
	default:
		slog.Error("httpapi: 未处理的内部错误", "err", err)
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
	}
}

// decodeJSON 读取并解析请求体。解析失败返回 domain.ErrInvalidArgument 的包装。
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return domain.Errorf(domain.ErrInvalidArgument, "请求体解析失败: %v", err)
	}
	return nil
}
