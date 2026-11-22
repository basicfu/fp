package testsupport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// FakeWechatAvatar 是假微信头像地址返回的图片（最小的 PNG 文件头即可通过头像校验）。
var FakeWechatAvatar = []byte("\x89PNG\r\n\x1a\nfake-wechat-avatar")

// NewFakeWechat 起一个假的 api.weixin.qq.com，同时兼当头像域名。
//
// code 写成 "openid:unionid:昵称"（后两段可省），换出来的身份就是这几个值；
// code 为 "bad" 时换 token 返回 errcode。AppSecret 必须是 "s3cret"。
// headimgurl 是 {srv.URL}/headimg/{openid}；openid 以 "noimg" 开头时头像地址返回 404。
func NewFakeWechat(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if openid, ok := strings.CutPrefix(r.URL.Path, "/headimg/"); ok {
			if strings.HasPrefix(openid, "noimg") {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(FakeWechatAvatar)
			return
		}
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
				"nickname": q.Get("access_token"), "headimgurl": srv.URL + "/headimg/" + q.Get("openid"),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}
