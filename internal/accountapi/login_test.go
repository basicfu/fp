package accountapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

func TestCodeLoginNeedsCaptchaAndGoesToTarget(t *testing.T) {
	e := newEnv(t)
	phone := newPhone()
	m := wantOK(t, e.do(t, "POST", "/code", smsBody(phone, "ok")))
	if m["cooldown_sec"] != float64(60) || e.lastTo() != phone {
		t.Fatalf("%v, sent to %s", m, e.lastTo())
	}
	wantErr(t, e.do(t, "POST", "/code", smsBody(phone, "bad")), 400, "captcha_failed", "")
	wantErr(t, e.do(t, "POST", "/code", smsBody(phone, "exp")), 400, "captcha_expired", "")
	// 格式不对的请求连滑块都不验,更不会发短信。
	n, v := len(e.sms.Sent()), e.captcha.verified.Load()
	for _, body := range []string{
		`{"purpose":"login","channel":"sms","target":"` + phone + `"}`,
		strings.Replace(smsBody(phone, "ok"), phone, "2380000000", 1),
		`{"purpose":"login","channel":"email","target":"a@b.com","captcha_id":"ok","x":1,"track":[[0,0,0]]}`,
		`{"purpose":"x","channel":"sms"}`, `nope`,
		smsBody(phone, "ok")[:len(smsBody(phone, "ok"))-1] + `,"pad":"` + strings.Repeat("x", 20000) + `"}`,
	} {
		wantErr(t, e.do(t, "POST", "/code", body), 400, "bad_request", "")
	}
	if len(e.sms.Sent()) != n || e.captcha.verified.Load() != v {
		t.Fatal("非法请求触达了发码或滑块")
	}
	// 登录方式沿用应用的启停:关了短信登录,还开着微信(首次扫码要绑手机)照样能发。
	e.setConnector(t, connector.TypeSMSCode, false, nil)
	wantOK(t, e.do(t, "POST", "/code", smsBody(newPhone(), "ok")))
	// 两个都关了:不许发登录短信,也不去验滑块。
	e.setConnector(t, connector.TypeWechatWeb, false, nil)
	n, v = len(e.sms.Sent()), e.captcha.verified.Load()
	wantErr(t, e.do(t, "POST", "/code", smsBody(newPhone(), "ok")), 400, "bad_request", "")
	wantErr(t, e.do(t, "POST", "/sms-code", legacySMS(newPhone(), "ok"), legacy()), 503, "unavailable", "")
	if len(e.sms.Sent()) != n || e.captcha.verified.Load() != v {
		t.Fatal("登录方式都关了还发了码或验了滑块")
	}
}

func TestCodeAccountPurposes(t *testing.T) {
	e := newEnv(t)
	_, phone, tok := e.loggedIn(t)
	other := newPhone()

	// 非 login 用途要登录。
	wantErr(t, e.do(t, "POST", "/code", `{"purpose":"verify","channel":"sms"}`), 401, "unauthorized", "")

	for _, c := range []struct {
		body, to string
	}{
		// verify / password 只发到当前手机号,不认客户端给的 target。
		{`{"purpose":"verify","channel":"sms","target":"` + other + `"}`, phone},
		{`{"purpose":"password","channel":"sms","target":"` + other + `"}`, phone},
		{`{"purpose":"bind","channel":"sms","target":"` + other + `"}`, other},
		{`{"purpose":"bind","channel":"email","target":"zhangwei@example.com"}`, "zhangwei@example.com"},
	} {
		m := wantOK(t, e.do(t, "POST", "/code", c.body, withToken(tok)))
		if m["cooldown_sec"] != float64(60) || e.lastTo() != c.to {
			t.Fatalf("%s: %v, sent to %s", c.body, m, e.lastTo())
		}
	}
	for _, body := range []string{
		`{"purpose":"verify","channel":"email"}`, `{"purpose":"password","channel":"email"}`,
		`{"purpose":"bind","channel":"sms","target":"123"}`, `{"purpose":"bind","channel":"email","target":"ab.com"}`,
		`{"purpose":"bind","channel":"fax","target":"x"}`, `{"purpose":"verify"}`,
	} {
		wantErr(t, e.do(t, "POST", "/code", body, withToken(tok)), 400, "bad_request", "")
	}
	// 目标已属别人。
	_, taken := e.newUser(t)
	wantErr(t, e.do(t, "POST", "/code", `{"purpose":"bind","channel":"sms","target":"`+taken+`"}`, withToken(tok)), 409, "phone_taken", "")
	// 没开密码登录:不许发设密码的码。
	e.setConnector(t, connector.TypePassword, false, nil)
	wantErr(t, e.do(t, "POST", "/code", `{"purpose":"password","channel":"sms"}`, withToken(tok)), 400, "bad_request", "")
}

func TestCodeVerifyTicketSingleUseAndExpires(t *testing.T) {
	e := newEnv(t)
	_, _, tok := e.loggedIn(t)
	verify := func() map[string]any {
		wantOK(t, e.do(t, "POST", "/code", `{"purpose":"verify","channel":"sms"}`, withToken(tok)))
		return wantOK(t, e.do(t, "POST", "/code/verify", `{"purpose":"verify","channel":"sms","code":"`+e.lastCode(t)+`"}`, withToken(tok)))
	}
	wantErr(t, e.do(t, "POST", "/code/verify", `{"purpose":"verify","channel":"sms","code":"000000"}`, withToken(tok)), 400, "code_invalid", "")
	wantErr(t, e.do(t, "POST", "/code/verify", `{"purpose":"verify","channel":"sms","code":"12"}`, withToken(tok)), 400, "bad_request", "")
	wantErr(t, e.do(t, "POST", "/code/verify", `{"purpose":"bind","channel":"sms","code":"123456"}`, withToken(tok)), 400, "bad_request", "")

	ticket, _ := verify()["ticket"].(string)
	if ticket == "" {
		t.Fatal("没签票据")
	}
	bind := func(ticket string) *httptest.ResponseRecorder {
		phone := newPhone()
		wantOK(t, e.do(t, "POST", "/code", `{"purpose":"bind","channel":"sms","target":"`+phone+`"}`, withToken(tok)))
		return e.do(t, "POST", "/identity/bind", `{"type":"phone","phone":"`+phone+`","code":"`+e.lastCode(t)+`","ticket":"`+ticket+`"}`, withToken(tok))
	}
	wantOK(t, bind(ticket))
	wantErr(t, bind(ticket), 400, "ticket_invalid", "")

	// 过期:直接删掉票据的键模拟 5 分钟到了。
	ticket, _ = verify()["ticket"].(string)
	sum := sha256.Sum256([]byte(ticket))
	if err := e.rdbDel("fp:acct:phone-ticket:" + hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	wantErr(t, bind(ticket), 400, "ticket_invalid", "")
}

func TestLoginSMSBindsDevice(t *testing.T) {
	e := newEnv(t)
	phone := newPhone()
	wantOK(t, e.do(t, "POST", "/code", smsBody(phone, "ok")))
	m := wantOK(t, e.do(t, "POST", "/login", `{"type":"sms","phone":"`+phone+`","code":"`+e.lastCode(t)+`"}`))
	tok, _ := m["token"].(string)
	u := userOf(t, m)
	if tok == "" || u["phone"] != phone[:3]+"****"+phone[7:] || u["name"] != u["phone"] || u["avatar"] != "" {
		t.Fatalf("登录: %v", m)
	}
	sess, err := e.sessions.SessionByToken(context.Background(), tok)
	if err != nil || sess.ClientRef != fakeClientRef || sess.DevicePub != e.dev.pubHex() {
		t.Fatalf("会话没绑设备: %+v %v", sess, err)
	}
	wantOK(t, e.do(t, "GET", "/me", "", withToken(tok)))

	wantErr(t, e.do(t, "POST", "/login", `{"type":"sms","phone":"`+phone+`","code":"000000"}`), 401, "login_failed", "")
	n := len(e.check.calls)
	for _, body := range []string{
		`{"type":"sms","phone":"1380013800","code":"123456"}`, `{"type":"sms","phone":"` + phone + `","code":"12345"}`,
		`{"type":"password","account":"","password":"pw"}`, `{"type":"password","account":"` + strings.Repeat("a", 49) + `","password":"pw"}`,
		`{"type":"password","account":"root","password":"` + strings.Repeat("p", 33) + `"}`,
		`{"type":"wechat"}`, `{"type":"wechat_phone","phone":"` + phone + `","code":"123456"}`, `{"type":"x"}`, `{`,
	} {
		wantErr(t, e.do(t, "POST", "/login", body), 400, "bad_request", "")
	}
	if len(e.check.calls) != n {
		t.Fatalf("非法请求问了应用: %d 次", len(e.check.calls)-n)
	}
}

func TestLoginPassword(t *testing.T) {
	e := newEnv(t)
	id, phone := e.newUser(t)
	if err := e.users.SetPassword(context.Background(), id, "Abcdefg1"); err != nil {
		t.Fatal(err)
	}
	m := wantOK(t, e.do(t, "POST", "/login", `{"type":"password","account":"`+phone+`","password":"Abcdefg1"}`))
	if u := userOf(t, m); u["id"] != id.String() || u["has_password"] != true {
		t.Fatalf("密码登录: %v", m)
	}
	wantErr(t, e.do(t, "POST", "/login", `{"type":"password","account":"`+phone+`","password":"wrong123"}`), 401, "login_failed", "")
	// 关了密码登录:拒掉,且不去问应用认不认设备。
	e.setConnector(t, connector.TypePassword, false, nil)
	n := len(e.check.calls)
	wantErr(t, e.do(t, "POST", "/login", `{"type":"password","account":"`+phone+`","password":"Abcdefg1"}`), 400, "bad_request", "")
	wantErr(t, e.do(t, "POST", "/login", `{"type":"password","account":"`+phone+`","password":"Abcdefg1"}`, legacy()), 503, "unavailable", "")
	if len(e.check.calls) != n {
		t.Fatal("登录方式关着还问了应用")
	}
}

func TestLoginDeviceChecks(t *testing.T) {
	e := newEnv(t)
	id, phone := e.newUser(t)
	if err := e.users.SetPassword(context.Background(), id, "Abcdefg1"); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"password","account":"` + phone + `","password":"Abcdefg1"}`

	wantErr(t, e.do(t, "POST", "/login", body, noDevice()), 400, "version_too_low", "")
	skewed := func(r *http.Request) { r.Header.Set(deviceHeader, e.dev.header(t, time.Now().Add(time.Hour))) }
	wantErr(t, e.do(t, "POST", "/login", body, skewed), 403, "device_unverified", "")

	// 应用拒绝:认得的码原样给客户端,不认得的按登录失败;超时、没有在线的流按不可用。
	e.check.err = domain.Fail(domain.ErrForbidden, "version_too_low", "版本太低")
	wantErr(t, e.do(t, "POST", "/login", body), 400, "version_too_low", "客户端版本过低，请升级到最新版本后再登录")
	e.check.err = domain.Fail(domain.ErrForbidden, "device_banned", "x")
	wantErr(t, e.do(t, "POST", "/login", body), 401, "login_failed", "")
	e.check.err = domain.Fail(domain.ErrInternal, "unavailable", "应用没有应答")
	wantErr(t, e.do(t, "POST", "/login", body), 503, "unavailable", "")
	e.check.err, e.check.ref = nil, ""
	wantErr(t, e.do(t, "POST", "/login", body), 503, "unavailable", "")

	// 没开设备绑定:不要设备头、不问应用,会话不带设备。
	e.check.ref = fakeClientRef
	e.setAccount(t, func(c *domain.AccountConfig) { c.DeviceBinding = false })
	n := len(e.check.calls)
	m := wantOK(t, e.do(t, "POST", "/login", body, noDevice()))
	tok, _ := m["token"].(string)
	if sess, _ := e.sessions.SessionByToken(context.Background(), tok); sess == nil || sess.DevicePub != "" || len(e.check.calls) != n {
		t.Fatalf("不绑设备: %+v", sess)
	}
}

func TestLoginCheckGetsClientIP(t *testing.T) {
	e := newEnv(t)
	phone := newPhone()
	wantOK(t, e.do(t, "POST", "/code", smsBody(phone, "ok")))
	wantOK(t, e.do(t, "POST", "/login", `{"type":"sms","phone":"`+phone+`","code":"`+e.lastCode(t)+`"}`,
		func(r *http.Request) { r.RemoteAddr = "203.0.113.9:1234"; r.Header.Set("X-Forwarded-For", "1.1.1.1") }))
	// 没配可信代理:XFF 不认。
	if got := e.check.calls[len(e.check.calls)-1]; got != "203.0.113.9" {
		t.Fatalf("ip = %s", got)
	}
}

func TestWechatLoginFirstBindsPhoneThenDirect(t *testing.T) {
	e := newEnv(t)
	start := func() map[string]any {
		return wantOK(t, e.do(t, "POST", "/wechat/start", `{"purpose":"login"}`))
	}
	st := start()
	// 回调走产品域名而不是 fp 的 public_url:微信只认登记过的授权回调域,也不该把 fp 的域名暴露给终端。
	if st["state"] == "" || !strings.Contains(st["auth_url"].(string), "state=") || st["expires_in"] != float64(300) ||
		st["redirect_uri"] != "https://"+testHost+"/oauth/callback" || !strings.Contains(st["auth_url"].(string), url.QueryEscape("https://"+testHost)) {
		t.Fatalf("start: %v", st)
	}
	ticket := e.wechatTicket(t, st["state"].(string), "oxA:unA:小白")
	m := wantOK(t, e.do(t, "POST", "/login", `{"type":"wechat","ticket":"`+ticket+`"}`))
	bt, _ := m["bind_ticket"].(string)
	if bt == "" || m["nickname"] != "小白" || m["token"] != nil {
		t.Fatalf("首次扫码: %v", m)
	}
	// 票据一次性。
	wantErr(t, e.do(t, "POST", "/login", `{"type":"wechat","ticket":"`+ticket+`"}`), 400, "wechat_expired", "")

	phone := newPhone()
	wantOK(t, e.do(t, "POST", "/code", smsBody(phone, "ok")))
	wantErr(t, e.do(t, "POST", "/login", `{"type":"wechat_phone","bind_ticket":"`+bt+`","phone":"`+phone+`","code":"000000"}`),
		401, "login_failed", "")
	m = wantOK(t, e.do(t, "POST", "/login", `{"type":"wechat_phone","bind_ticket":"`+bt+`","phone":"`+phone+`","code":"`+e.lastCode(t)+`"}`))
	if u := userOf(t, m); m["token"] == "" || u["wechat"] != "小白" || u["nickname"] != "小白" {
		t.Fatalf("绑手机登录: %v", m)
	}

	// 第二次扫码直接登录,微信昵称随之刷新。
	ticket = e.wechatTicket(t, start()["state"].(string), "oxA:unA:小黑")
	m = wantOK(t, e.do(t, "POST", "/login", `{"type":"wechat","ticket":"`+ticket+`"}`))
	tok, _ := m["token"].(string)
	if u := userOf(t, m); tok == "" || u["wechat"] != "小黑" {
		t.Fatalf("老用户扫码: %v", m)
	}
	if sess, _ := e.sessions.SessionByToken(context.Background(), tok); sess == nil || sess.ClientRef != fakeClientRef {
		t.Fatalf("扫码会话没绑设备: %+v", sess)
	}

	wantErr(t, e.do(t, "POST", "/wechat/start", `{"purpose":"x"}`), 400, "bad_request", "")
}

// 回跳地址取微信登录方式「允许的网页回跳地址」的第一项;白名单为空时不开放扫码。
func TestWechatReturnURLFromConnector(t *testing.T) {
	e := newEnv(t)
	wechatCfg := func(urls string) map[string]any {
		return map[string]any{"appId": "wxapp", "appSecret": "s3cret", "returnUrls": urls}
	}
	e.setConnector(t, connector.TypeWechatWeb, true, wechatCfg(" myapp://first , myapp://second"))
	st := wantOK(t, e.do(t, "POST", "/wechat/start", `{"purpose":"login"}`))
	cb := e.auth.HandleOAuthCallback(context.Background(), st["state"].(string), "oxR:unR:回跳")
	if !strings.HasPrefix(cb.Redirect, "myapp://first?") {
		t.Fatalf("回跳 = %q, 应是白名单第一项", cb.Redirect)
	}

	e.setConnector(t, connector.TypeWechatWeb, true, wechatCfg(""))
	wantErr(t, e.do(t, "POST", "/wechat/start", `{"purpose":"login"}`), 503, "unavailable", "微信登录暂未开放")
	_, _, tok := e.loggedIn(t)
	wantErr(t, e.do(t, "POST", "/wechat/start", `{"purpose":"bind"}`, withToken(tok)), 503, "unavailable", "微信登录暂未开放")

	// 关了微信登录:新接口照旧回 bad_request,老路径回暂未开放。
	e.setConnector(t, connector.TypeWechatWeb, false, wechatCfg(returnURL))
	wantErr(t, e.do(t, "POST", "/wechat/start", `{"purpose":"login"}`), 400, "bad_request", "")
	wantErr(t, e.do(t, "POST", "/wechat/start", `{}`, legacy()), 503, "unavailable", "微信登录暂未开放")
	wantErr(t, e.do(t, "POST", "/wechat/login", `{"ticket":"t"}`, legacy()), 503, "unavailable", "微信登录暂未开放")
	wantErr(t, e.do(t, "POST", "/login", `{"type":"wechat","ticket":"t"}`), 400, "bad_request", "")
}

// bind 票据只认发起人:换给别的用户、或拿登录票据来绑都不行。
func TestWechatBindTicketBoundToStarter(t *testing.T) {
	e := newEnv(t)
	_, _, tokA := e.loggedIn(t)
	_, _, tokB := e.loggedIn(t)
	wantErr(t, e.do(t, "POST", "/wechat/start", `{"purpose":"bind"}`), 401, "unauthorized", "")
	st := wantOK(t, e.do(t, "POST", "/wechat/start", `{"purpose":"bind"}`, withToken(tokA)))
	ticket := e.wechatTicket(t, st["state"].(string), "oxB:unB:阿福")
	wantErr(t, e.do(t, "POST", "/identity/bind", `{"type":"wechat","ticket":"`+ticket+`"}`, withToken(tokB)), 400, "wechat_expired", "")
	// 登录票据不能拿来绑。
	login := wantOK(t, e.do(t, "POST", "/wechat/start", `{"purpose":"login"}`))
	lt := e.wechatTicket(t, login["state"].(string), "oxC:unC:阿贵")
	wantErr(t, e.do(t, "POST", "/identity/bind", `{"type":"wechat","ticket":"`+lt+`"}`, withToken(tokA)), 400, "wechat_expired", "")
	// 绑定票据也不能拿来登录。
	st = wantOK(t, e.do(t, "POST", "/wechat/start", `{"purpose":"bind"}`, withToken(tokA)))
	bt := e.wechatTicket(t, st["state"].(string), "oxD:unD:阿三")
	wantErr(t, e.do(t, "POST", "/login", `{"type":"wechat","ticket":"`+bt+`"}`), 400, "wechat_expired", "")
}

func TestLogout(t *testing.T) {
	e := newEnv(t)
	_, _, tok := e.loggedIn(t)
	// 设备头对不上:照样 200,但不撤。
	if w := e.do(t, "POST", "/logout", "", withToken(tok), withDevice(newDevice(t), t)); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "{}" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	wantOK(t, e.do(t, "GET", "/me", "", withToken(tok)))
	for range 2 {
		if w := e.do(t, "POST", "/logout", "", withToken(tok)); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "{}" {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	wantErr(t, e.do(t, "GET", "/me", "", withToken(tok)), 401, "unauthorized", "")
	if w := e.do(t, "POST", "/logout", ""); w.Code != 200 {
		t.Fatalf("不带 token: %d", w.Code)
	}
}

// 开了设备绑定后，没记设备公钥的会话（开启前签发、没导入的）按登录过期处理，让客户端重新登录绑上设备；
// 登出照常撤掉它。
func TestSessionWithoutDeviceIsUnauthorizedWhenBindingOn(t *testing.T) {
	e := newEnv(t)
	id, _ := e.newUser(t)
	s, err := e.sessions.Issue(context.Background(), service.IssueInput{UserID: id, App: e.app})
	if err != nil {
		t.Fatal(err)
	}
	wantErr(t, e.do(t, "GET", "/me", "", withToken(s.Token)), 401, "unauthorized", "")
	wantOK(t, e.do(t, "POST", "/logout", "", withToken(s.Token)))
	if _, err := e.sessions.SessionByToken(context.Background(), s.Token); err == nil {
		t.Fatal("登出没撤掉没绑设备的会话")
	}
}
