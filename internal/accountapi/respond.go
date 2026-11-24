// Package accountapi 是终端用户直接调用的账号 HTTP 接口(/api/account/*):按 Host 认应用,
// 复用 AuthService / UserService / AccountService / WechatMPService。错误码、状态码与文案照搬业务方原实现,
// 已发布客户端靠它们分支(比如只有 unauthorized 才清登录态),一个都不能改。
package accountapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/basicfu/fp/sdk/resp"
)

// errBody 与业务方原实现的 result.Result 同形。time 是服务端毫秒时间:钟偏的客户端在任何一次失败里都能顺手校正。
type errBody struct {
	Code   string `json:"code"`
	Msg    string `json:"msg"`
	Detail string `json:"detail,omitempty"`
	Time   int64  `json:"time"`
}

// writeJSON 与原实现逐字节一致:Content-Type 不带 charset,Encoder 末尾带换行。
func writeJSON(w http.ResponseWriter, status int, v any) {
	// 带 X-Resp-Format: 2 的请求成功响应统一成 {data, rid, time}; 不带则逐字节保持原样(已发布客户端依赖)。
	if status >= 200 && status < 300 && resp.IsV2(w) {
		resp.OK(w, v)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr 的 msg 为空时用错误码的默认文案。
func (h *handler) writeErr(w http.ResponseWriter, code, msg string) {
	e, ok := errTable[code]
	if !ok {
		code, e = "internal", errTable["internal"]
	}
	if msg == "" {
		msg = e.msg
	}
	if resp.IsV2(w) {
		resp.Fail(w, code, msg, nil, e.status)
		return
	}
	writeJSON(w, e.status, errBody{Code: code, Msg: msg, Time: h.now().UnixMilli()})
}

func ok(w http.ResponseWriter, v any) { writeJSON(w, http.StatusOK, v) }

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }

type empty struct{}

func (h *handler) now() time.Time {
	if h.clock != nil {
		return h.clock()
	}
	return time.Now()
}
