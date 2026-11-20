package httpapi

import (
	"io"
	"net/http"

	"github.com/basicfu/fp/internal/service"
)

// 推送报文只有几百字节；给足余量，同时不让人拿大包把内存打满。
const wechatPushMaxBytes = 64 << 10

// wechatMPCallback 接服务号的「服务器配置」：GET 是填地址时的校验，POST 是事件推送。
// 对面是微信服务器，响应只有原样回写、success 或 400 三种。
func wechatMPCallback(svc *service.WechatMPService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var res *service.WechatPushResult
		switch r.Method {
		case http.MethodGet:
			res = svc.VerifyCallbackURL(r.Context(), r.URL.Query())
		case http.MethodPost:
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, wechatPushMaxBytes))
			if err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			res = svc.HandlePush(r.Context(), r.URL.Query(), body)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if res.BadRequest {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		ct := "text/plain; charset=utf-8"
		if len(res.Body) > 0 && res.Body[0] == '<' {
			ct = "application/xml; charset=utf-8"
		}
		w.Header().Set("Content-Type", ct)
		_, _ = w.Write(res.Body)
	}
}
