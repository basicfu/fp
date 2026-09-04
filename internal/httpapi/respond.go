// Package httpapi 是管理 UI 的 HTTP 传输层。它只做协议转换，不含业务逻辑。
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/basicfu/fp/internal/domain"
)

// errorBody 是所有错误响应的形状。
//
// 三个字段的分工见 domain.Error 的注释：code 机器可读，msg 面向终端用户
// 可直接展示，detail 是 JSON 字符串、供接入方排查。
//
// detail 用 omitempty：没有细节时该字段直接不出现，让"没有细节"在线上是
// 一个明确的空值，而不是一个需要再解析一层才能确认为空的 "{}"。
type errorBody struct {
	Code   string `json:"code"`
	Msg    string `json:"msg"`
	Detail string `json:"detail,omitempty"`
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

// StatusFor 把领域哨兵映射成 HTTP 状态码。
//
// 导出的理由：它是**协议契约**而非传输层内部细节，需要被跨包的一致性
// 测试与 grpcapi.CodeFor 对照。internal/ 已经限定了影响范围。
//
// 与 grpcapi.CodeFor 一一对应——同一个领域错误在两个传输层必须表达同一
// 件事，否则同一个失败经 HTTP 是 403、经 gRPC 是 NotFound，排障时没人能
// 把两边的日志对上。这条对应关系由 TestTransportsAgreeOnEveryCode 断言，
// 不再只靠注释约定。
func StatusFor(sentinel error) int {
	switch {
	case errors.Is(sentinel, domain.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(sentinel, domain.ErrInvalidCredential):
		return http.StatusUnauthorized
	case errors.Is(sentinel, domain.ErrUnauthorized):
		return http.StatusUnauthorized
	case errors.Is(sentinel, domain.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(sentinel, domain.ErrConflict):
		return http.StatusConflict
	case errors.Is(sentinel, domain.ErrInvalidArgument):
		return http.StatusBadRequest
	case errors.Is(sentinel, domain.ErrRateLimited):
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}

// writeError 把领域错误映射为 HTTP 响应。
//
// 取不到 *domain.Error 的错误（来自 pgx / redis / 标准库）一律 500 且
// **丢掉原始消息**：它们的消息里可能带连接串、SQL、表名。服务端日志留
// 全文，线上只回一句通用说明、detail 为空。
func writeError(w http.ResponseWriter, err error) {
	var de *domain.Error
	if !errors.As(err, &de) {
		slog.Error("httpapi: 未处理的内部错误", "err", err)
		writeJSON(w, http.StatusInternalServerError, errorBody{
			Code: domain.CodeInternal,
			Msg:  "服务器内部错误",
		})
		return
	}
	writeJSON(w, StatusFor(de.Unwrap()), errorBody{
		Code:   de.Code,
		Msg:    de.Msg,
		Detail: de.DetailJSON(),
	})
}

// decodeJSON 读取并解析请求体。解析失败返回 domain.ErrInvalidArgument 的包装。
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return domain.Fail(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "请求体格式不正确").
			WithDesc("解析失败: %v", err)
	}
	return nil
}
