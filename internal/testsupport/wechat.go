package testsupport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// NewFakeWechat 起一个假的 api.weixin.qq.com。
//
// code 写成 "openid:unionid:昵称"（后两段可省），换出来的身份就是这几个值；
// code 为 "bad" 时换 token 返回 errcode。AppSecret 必须是 "s3cret"。
func NewFakeWechat(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/sns/oauth2/access_token":
			code := q.Get("code")
			if q.Get("secret") != "s3cret" || code == "bad" {
				_, _ = w.Write([]byte(`{"errcode":40029,"errmsg":"invalid code"}`))
				return
			}
			parts := strings.SplitN(code, ":", 3)
			for len(parts) < 3 {
				parts = append(parts, "")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				// access_token 带上昵称，userinfo 那一步就不用另存状态。
				"access_token": parts[2], "openid": parts[0], "unionid": parts[1],
			})
		case "/sns/userinfo":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"nickname": q.Get("access_token"), "headimgurl": "https://img/" + q.Get("openid"),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}
