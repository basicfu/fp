package accountapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/accountapi"
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
	// 不再有单独的开关:去掉域名就不对外,应用停用同样认不出。
	if _, err := e.apps.SetAccount(context.Background(), e.app.ID, nil, e.app.Account); err != nil {
		t.Fatal(err)
	}
	wantErr(t, e.do(t, "GET", "/captcha", ""), http.StatusNotFound, "not_found", "")
	e.setAccount(t, func(*domain.AccountConfig) {})
	if _, err := e.apps.SetStatus(context.Background(), e.app.ID, domain.ApplicationStatusDisabled); err != nil {
		t.Fatal(err)
	}
	wantErr(t, e.do(t, "GET", "/captcha", ""), http.StatusNotFound, "not_found", "")
}

// 生产库里的 account 还带着旧版的开关字段:照常解析,旧 enabled/login_types/editable_fields 不再起作用,
// device_binding 保留生效。
func TestOldAccountJSONStillReads(t *testing.T) {
	e := newEnv(t)
	old := `{"enabled":false,"login_types":[],"editable_fields":[],"device_binding":true,` +
		`"device_header":"X-Old","events":false,"wechat_return_url":"myapp://old"}`
	if err := e.db(context.Background(), `UPDATE application SET account = $2::jsonb WHERE id = $1`, e.app.ID, old); err != nil {
		t.Fatal(err)
	}
	id, phone := e.newUser(t)
	if err := e.users.SetPassword(context.Background(), id, "Abcdefg1"); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"password","account":"` + phone + `","password":"Abcdefg1"}`
	wantErr(t, e.do(t, "POST", "/login", body, noDevice()), 400, "version_too_low", "")
	tok, _ := wantOK(t, e.do(t, "POST", "/login", body))["token"].(string)
	wantOK(t, e.do(t, "PUT", "/profile", `{"nickname":"阿福"}`, withToken(tok)))
}

// 设备签名头固定为 X-Sign。新接口只认它;老路径(不带 X-Account-Api)还认已发版客户端用的 X-KKdesk-Device。
func TestSignHeader(t *testing.T) {
	e := newEnv(t)
	id, phone, tok := e.loggedIn(t)
	if err := e.users.SetPassword(context.Background(), id, "Abcdefg1"); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"password","account":"` + phone + `","password":"Abcdefg1"}`
	oldName := func(r *http.Request) {
		r.Header.Del(deviceHeader)
		r.Header.Set("X-KKdesk-Device", e.dev.header(t, time.Now()))
	}
	wantErr(t, e.do(t, "GET", "/me", "", withToken(tok), oldName), 400, "version_too_low", "")
	wantErr(t, e.do(t, "POST", "/login", body, oldName), 400, "version_too_low", "")

	L := legacy()
	wantOK(t, e.do(t, "GET", "/me", "", L, withToken(tok), oldName))
	wantOK(t, e.do(t, "GET", "/me", "", L, withToken(tok)))
	wantOK(t, e.do(t, "POST", "/login", body, L, oldName))
	wantOK(t, e.do(t, "POST", "/login", body, L))
	// 两个都带时以 X-Sign 为准。
	both := func(r *http.Request) { r.Header.Set("X-KKdesk-Device", "garbage") }
	wantOK(t, e.do(t, "GET", "/me", "", L, withToken(tok), both))
}

// 头名不分大小写:经真实连接发小写的 x-sign 也认。
func TestSignHeaderCaseInsensitive(t *testing.T) {
	e := newEnv(t)
	_, _, tok := e.loggedIn(t)
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/me", nil)
	req.Host = testHost
	req.Header.Set(accountapi.APIHeader, accountapi.APIVersion)
	req.Header.Set("Authorization", "Bearer "+tok)
	// 直接写 map 绕过 Set 的规范化,线上就是小写原样发出。
	req.Header["x-sign"] = []string{e.dev.header(t, time.Now())}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("小写 x-sign: %d", resp.StatusCode)
	}
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
