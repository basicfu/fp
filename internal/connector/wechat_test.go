package connector_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
)

func fakeWechat(t *testing.T, token, user string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sns/oauth2/access_token":
			if r.URL.Query().Get("secret") != "s3cret" || r.URL.Query().Get("code") == "" {
				_, _ = w.Write([]byte(`{"errcode":40029,"errmsg":"invalid code"}`))
				return
			}
			_, _ = w.Write([]byte(token))
		case "/sns/userinfo":
			_, _ = w.Write([]byte(user))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

var wechatCfg = map[string]any{"appId": "wxapp", "appSecret": "s3cret"}

func TestWechatExchange(t *testing.T) {
	srv := fakeWechat(t,
		`{"access_token":"at","openid":"o1","unionid":"u1"}`,
		`{"nickname":"小明","headimgurl":"https://img/1"}`)
	c := connector.NewWechatWeb(connector.WithWechatAPIBase(srv.URL))

	p, err := c.Exchange(context.Background(), wechatCfg, "code1")
	if err != nil {
		t.Fatal(err)
	}
	want := connector.OAuthProfile{IdentityType: domain.IdentityTypeWechatWeb, Subject: "o1", UnionKey: "u1", Nickname: "小明", AvatarURL: "https://img/1"}
	if *p != want {
		t.Fatalf("profile = %+v, want %+v", *p, want)
	}
}

func TestWechatExchangeUnionIDFromUserInfo(t *testing.T) {
	srv := fakeWechat(t, `{"access_token":"at","openid":"o1"}`, `{"nickname":"n","unionid":"u2"}`)
	c := connector.NewWechatWeb(connector.WithWechatAPIBase(srv.URL))
	p, err := c.Exchange(context.Background(), wechatCfg, "code1")
	if err != nil || p.UnionKey != "u2" {
		t.Fatalf("p = %+v, err = %v", p, err)
	}
}

// 用户信息拿不到时 openid 仍然有效，登录不应失败。
func TestWechatExchangeUserInfoFailureDegrades(t *testing.T) {
	srv := fakeWechat(t, `{"access_token":"at","openid":"o1","unionid":"u1"}`, `{"errcode":40001,"errmsg":"x"}`)
	c := connector.NewWechatWeb(connector.WithWechatAPIBase(srv.URL))
	p, err := c.Exchange(context.Background(), wechatCfg, "code1")
	if err != nil || p.Subject != "o1" || p.Nickname != "" {
		t.Fatalf("p = %+v, err = %v", p, err)
	}
}

func TestWechatExchangeUpstreamError(t *testing.T) {
	srv := fakeWechat(t, `{}`, `{}`)
	c := connector.NewWechatWeb(connector.WithWechatAPIBase(srv.URL))
	cfg := map[string]any{"appId": "wxapp", "appSecret": "wrong"}
	_, err := c.Exchange(context.Background(), cfg, "code1")
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeWechatUpstreamFailed {
		t.Fatalf("err = %v, want %s", err, domain.CodeWechatUpstreamFailed)
	}
	if strings.Contains(de.Msg, "invalid code") {
		t.Fatalf("微信原始报错不能进 Msg：%q", de.Msg)
	}
}

func TestWechatAuthorizeURLParamOrder(t *testing.T) {
	c := connector.NewWechatWeb()
	got := c.AuthorizeURL(wechatCfg, "https://fp.example.com/oauth/callback", "st")
	want := "https://open.weixin.qq.com/connect/qrconnect?appid=wxapp&redirect_uri=" +
		url.QueryEscape("https://fp.example.com/oauth/callback") +
		"&response_type=code&scope=snsapi_login&state=st#wechat_redirect"
	if got != want {
		t.Fatalf("url = %s\nwant  %s", got, want)
	}
}

func TestWechatRejectsDirectLogin(t *testing.T) {
	_, err := connector.NewWechatWeb().Authenticate(context.Background(), wechatCfg, connector.Credentials{"code": "x"})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v", err)
	}
}

func TestReturnURLAllowed(t *testing.T) {
	allowed := connector.OAuthReturnURLs(map[string]any{"returnUrls": "https://a.com/login, https://b.com/\n"})
	cases := map[string]bool{
		"https://a.com/login":                   true,
		"https://a.com/login?x=1":               true,
		"https://a.com/login/wechat":            true,
		"https://a.com/loginx":                  false,
		"https://a.com.evil.com/login":          false,
		"https://b.com/anything":                true,
		"https://evil.com/?https://a.com/login": false,
	}
	for u, want := range cases {
		if got := connector.ReturnURLAllowed(allowed, u); got != want {
			t.Errorf("%s: got %v, want %v", u, got, want)
		}
	}
}
