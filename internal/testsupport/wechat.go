package testsupport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/domain"
)

// FakeWechatAvatar 是假微信头像地址返回的图片（最小的 PNG 文件头即可通过头像校验）。
var FakeWechatAvatar = []byte("\x89PNG\r\n\x1a\nfake-wechat-avatar")

// FakeWechatSlowAvatar 是 openid 以 "slow" 开头时头像地址的响应延迟。
const FakeWechatSlowAvatar = 1500 * time.Millisecond

// NewFakeWechat 起一个假的 api.weixin.qq.com，同时兼当头像域名。
//
// code 写成 "openid:unionid:昵称"（后两段可省），换出来的身份就是这几个值；
// code 为 "bad" 时换 token 返回 errcode。AppSecret 必须是 "s3cret"。
//
// headimgurl 默认是 {srv.URL}/headimg/{openid}，返回 FakeWechatAvatar。openid 前缀决定异常情形：
// noimg → 404，text → Content-Type 不是图片，big → 超过头像上限，slow → 延迟 FakeWechatSlowAvatar，
// evil → 地址换成 localhost（不在放行 127.0.0.1 的白名单里），dflt → 微信默认头像地址，empty → 空。
func NewFakeWechat(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if openid, ok := strings.CutPrefix(r.URL.Path, "/headimg/"); ok {
			serveFakeAvatar(w, r, openid)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/mmopen/") {
			serveFakeAvatar(w, r, "")
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
				"nickname": q.Get("access_token"), "headimgurl": fakeHeadimgURL(srv.URL, q.Get("openid")),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeWechatDefaultAvatarID 与 connector.WechatDefaultAvatarID 相同；testsupport 不依赖 connector，这里抄一份。
const fakeWechatDefaultAvatarID = "POgEwh4mIHO4nibH0KlMECNjjGxQUq24ZEaGT4poC6icRiccVGKSyXwibcPq4BWmiaIGuG1icwxaQX6grC9VemZoJ8rg"

func fakeHeadimgURL(base, openid string) string {
	switch {
	case strings.HasPrefix(openid, "empty"):
		return ""
	case strings.HasPrefix(openid, "evil"):
		return strings.Replace(base, "127.0.0.1", "localhost", 1) + "/headimg/" + openid
	case strings.HasPrefix(openid, "dflt"):
		return base + "/mmopen/vi_32/" + fakeWechatDefaultAvatarID + "/132"
	}
	return base + "/headimg/" + openid
}

func serveFakeAvatar(w http.ResponseWriter, r *http.Request, openid string) {
	switch {
	case strings.HasPrefix(openid, "noimg"):
		http.NotFound(w, r)
		return
	case strings.HasPrefix(openid, "text"):
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(FakeWechatAvatar)
		return
	case strings.HasPrefix(openid, "big"):
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(append(append([]byte{}, FakeWechatAvatar...), make([]byte, domain.AvatarMaxBytes)...))
		return
	case strings.HasPrefix(openid, "slow"):
		time.Sleep(FakeWechatSlowAvatar)
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(FakeWechatAvatar)
}
