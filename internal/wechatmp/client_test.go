package wechatmp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeMP struct {
	tokens   atomic.Int32
	invalid  atomic.Int32 // 大于 0 时，下一次业务调用回 40001
	lastBody map[string]any
}

func newFakeMP(t *testing.T) (*fakeMP, *Client) {
	t.Helper()
	f := &fakeMP{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi-bin/stable_token" {
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["secret"] != "sec" {
				_, _ = io.WriteString(w, `{"errcode":40125,"errmsg":"invalid appsecret"}`)
				return
			}
			n := f.tokens.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok" + string(rune('0'+n)), "expires_in": 7200})
			return
		}
		if f.invalid.Load() > 0 {
			f.invalid.Add(-1)
			_, _ = io.WriteString(w, `{"errcode":40001,"errmsg":"invalid credential"}`)
			return
		}
		f.lastBody = nil
		_ = json.NewDecoder(r.Body).Decode(&f.lastBody)
		switch r.URL.Path {
		case "/cgi-bin/qrcode/create":
			_, _ = io.WriteString(w, `{"ticket":"T+1","expire_seconds":600,"url":"http://weixin.qq.com/q/x"}`)
		case "/cgi-bin/user/info":
			_, _ = io.WriteString(w, `{"subscribe":1,"openid":"o1","unionid":"u1"}`)
		case "/cgi-bin/message/template/send":
			if f.lastBody["touser"] == "gone" {
				_, _ = io.WriteString(w, `{"errcode":43004,"errmsg":"require subscribe"}`)
				return
			}
			_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok","msgid":1}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return f, New(Config{AppID: "wxmp", AppSecret: "sec", APIBase: srv.URL})
}

func TestTokenCachedAndRefreshedOnInvalid(t *testing.T) {
	f, c := newFakeMP(t)
	ctx := context.Background()
	if _, err := c.UserInfo(ctx, "o1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UserInfo(ctx, "o1"); err != nil {
		t.Fatal(err)
	}
	if n := f.tokens.Load(); n != 1 {
		t.Fatalf("取了 %d 次凭证，应只取 1 次", n)
	}
	f.invalid.Store(1)
	info, err := c.UserInfo(ctx, "o1")
	if err != nil || !info.Subscribed || info.UnionID != "u1" {
		t.Fatalf("info = %+v, err = %v", info, err)
	}
	if n := f.tokens.Load(); n != 2 {
		t.Fatalf("凭证失效后应换新一次，实际共取 %d 次", n)
	}
}

func TestCreateSceneQR(t *testing.T) {
	f, c := newFakeMP(t)
	qr, err := c.CreateSceneQR(context.Background(), "fpsub:abc", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if qr.ImageURL != "https://mp.weixin.qq.com/cgi-bin/showqrcode?ticket=T%2B1" || qr.Content != "http://weixin.qq.com/q/x" || qr.ExpiresIn != 10*time.Minute {
		t.Fatalf("qr = %+v", qr)
	}
	info := f.lastBody["action_info"].(map[string]any)["scene"].(map[string]any)
	if info["scene_str"] != "fpsub:abc" || f.lastBody["action_name"] != "QR_STR_SCENE" {
		t.Fatalf("body = %+v", f.lastBody)
	}
}

func TestSendTemplate(t *testing.T) {
	f, c := newFakeMP(t)
	ctx := context.Background()
	err := c.SendTemplate(ctx, TemplateMessage{ToUser: "o1", TemplateID: "tpl", URL: "https://a.com", Data: map[string]string{"thing1": "张三的电脑"}})
	if err != nil {
		t.Fatal(err)
	}
	data := f.lastBody["data"].(map[string]any)["thing1"].(map[string]any)
	if data["value"] != "张三的电脑" || f.lastBody["url"] != "https://a.com" || f.lastBody["template_id"] != "tpl" {
		t.Fatalf("body = %+v", f.lastBody)
	}
	err = c.SendTemplate(ctx, TemplateMessage{ToUser: "gone", TemplateID: "tpl"})
	if !IsNotFollowing(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestBadSecretErrorHasNoSecret(t *testing.T) {
	_, c := newFakeMP(t)
	c.cfg.AppSecret = "wrong-secret"
	_, err := c.UserInfo(context.Background(), "o1")
	if err == nil || strings.Contains(err.Error(), "wrong-secret") {
		t.Fatalf("err = %v", err)
	}
}
