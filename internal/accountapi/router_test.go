package accountapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/accountapi/captcha"
	"github.com/basicfu/fp/internal/domain"
)

func TestUnknownHostAndDisabledAppAre404(t *testing.T) {
	e := newEnv(t)
	wantErr(t, e.do(t, "GET", "/me", "", func(r *http.Request) { r.Host = "other.test.local" }),
		http.StatusNotFound, "not_found", "")
	// 带端口、大小写不同照样认得出。
	if w := e.do(t, "GET", "/captcha", "", func(r *http.Request) { r.Host = "API.Test.Local:443" }); w.Code != http.StatusOK {
		t.Fatalf("带端口的 Host: %d %s", w.Code, w.Body.String())
	}
	e.setAccount(t, func(c *domain.AccountConfig) { c.Enabled = false })
	wantErr(t, e.do(t, "GET", "/captcha", ""), http.StatusNotFound, "not_found", "")
}

func TestCaptchaBinaryNoStore(t *testing.T) {
	e := newEnv(t)
	w := e.do(t, "GET", "/captcha", "")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/octet-stream" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	c, err := captcha.DecodeCaptcha(w.Body.Bytes())
	if err != nil || c.ID != "ok" || c.TileY != 40 {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestRequireUser(t *testing.T) {
	e := newEnv(t)
	_, _, tok := e.loggedIn(t)

	wantErr(t, e.do(t, "GET", "/me", ""), http.StatusUnauthorized, "unauthorized", "登录已过期，请重新登录")
	wantErr(t, e.do(t, "GET", "/me", "", withToken("nope")), http.StatusUnauthorized, "unauthorized", "")
	wantErr(t, e.do(t, "GET", "/me", "", withToken(tok), noDevice()), http.StatusBadRequest, "version_too_low", "")
	// A 设备的 token 配 B 设备的头,B 的签名本身是对的。
	wantErr(t, e.do(t, "GET", "/me", "", withToken(tok), withDevice(newDevice(t), t)), http.StatusForbidden, "device_unverified", "")
	// 时钟偏得太多:不能被说成登录过期。
	skewed := func(r *http.Request) { r.Header.Set(deviceHeader, e.dev.header(t, time.Now().Add(-time.Hour))) }
	wantErr(t, e.do(t, "GET", "/me", "", withToken(tok), skewed), http.StatusForbidden, "device_unverified", "")
	// 同一个头重放。
	h := e.dev.header(t, time.Now())
	same := func(r *http.Request) { r.Header.Set(deviceHeader, h) }
	wantOK(t, e.do(t, "GET", "/me", "", withToken(tok), same))
	wantErr(t, e.do(t, "GET", "/me", "", withToken(tok), same), http.StatusForbidden, "device_unverified", "")

	if m := wantOK(t, e.do(t, "GET", "/me", "", withToken(tok))); m["token"] != nil {
		t.Fatalf("没轮换时不该带 token: %v", m)
	}
}

// 设备不对的请求不能触发轮换:否则偷到 token 的人能把真设备手里的 token 挤到过渡期之后失效。
func TestRotationReturnsTokenAndNeedsDevice(t *testing.T) {
	e := newEnv(t)
	_, _, tok := e.loggedIn(t)
	before, err := e.sessions.SessionByToken(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(25 * time.Hour)

	wantErr(t, e.do(t, "GET", "/me", "", withToken(tok), withDevice(newDevice(t), t)), http.StatusForbidden, "device_unverified", "")
	// 轮换会把旧 token 的空闲期限缩到过渡期,没变就说明没轮换。
	if after, err := e.sessions.SessionByToken(context.Background(), tok); err != nil || after.IdleExpiresAt != before.IdleExpiresAt {
		t.Fatalf("设备不对的请求动了会话: %+v %v", after, err)
	}

	m := wantOK(t, e.do(t, "GET", "/me", "", withToken(tok)))
	newTok, _ := m["token"].(string)
	if newTok == "" || newTok == tok {
		t.Fatalf("轮换后应带新 token: %v", m)
	}
	// 新 token 仍绑着同一台设备。
	wantOK(t, e.do(t, "GET", "/me", "", withToken(newTok)))
	wantErr(t, e.do(t, "GET", "/me", "", withToken(newTok), withDevice(newDevice(t), t)), http.StatusForbidden, "device_unverified", "")
}

// 客户端遇到 device_unverified 按响应头 Date 校正时钟后重签:真实服务器上必须带 Date。
func TestResponsesCarryDateHeader(t *testing.T) {
	e := newEnv(t)
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	for _, path := range []string{"/captcha", "/me"} {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Host = testHost
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		d, err := http.ParseTime(resp.Header.Get("Date"))
		if err != nil || time.Since(d) > time.Minute {
			t.Fatalf("%s: Date = %q (%v)", path, resp.Header.Get("Date"), err)
		}
	}
}

func TestMethodNotAllowed(t *testing.T) {
	e := newEnv(t)
	if w := e.do(t, "GET", "/login", ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET login = %d", w.Code)
	}
	if w := e.do(t, "GET", "/sms-code", "", legacy()); w.Code != http.StatusMethodNotAllowed || len(e.sms.Sent()) != 0 {
		t.Fatalf("GET sms-code = %d", w.Code)
	}
}
