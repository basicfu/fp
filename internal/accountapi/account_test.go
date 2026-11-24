package accountapi_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

var pngHeader = []byte("\x89PNG\r\n\x1a\n0000IHDR")

func TestMeAccountPanelFields(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, phone, tok := e.loggedIn(t)
	u := userOf(t, wantOK(t, e.do(t, "GET", "/me", "", withToken(tok))))
	if u["id"] != id.String() || u["phone"] != phone[:3]+"****"+phone[7:] || u["email"] != "" || u["wechat"] != "" ||
		u["has_password"] != false || u["notify"] != false || u["avatar"] != "" || u["display_id"] != nil {
		t.Fatalf("me.user = %v", u)
	}
	// 绑着微信却没记过昵称:不能是空串,那在契约里是未绑定。
	if _, err := e.users.AttachIdentity(ctx, id, service.EnsureIdentityInput{Type: domain.IdentityTypeWechatWeb, Subject: "ox1"}); err != nil {
		t.Fatal(err)
	}
	if u = userOf(t, wantOK(t, e.do(t, "GET", "/me", "", withToken(tok)))); u["wechat"] != "已绑定" {
		t.Fatalf("没昵称的微信: %v", u)
	}
	if st, err := e.users.ImportWechatNicknames(ctx, strings.NewReader(`{"user_id":"`+id.String()+`","nickname":"惊呆了小白"}`)); err != nil || st.Updated != 1 {
		t.Fatalf("写昵称: %+v %v", st, err)
	}
	if u = userOf(t, wantOK(t, e.do(t, "GET", "/me", "", withToken(tok)))); u["wechat"] != "惊呆了小白" {
		t.Fatalf("微信昵称: %v", u)
	}
	// 账号刚被删:按未登录。
	if err := e.db(ctx, `DELETE FROM app_user WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	wantErr(t, e.do(t, "GET", "/me", "", withToken(tok)), 401, "unauthorized", "")
}

func TestProfile(t *testing.T) {
	e := newEnv(t)
	_, _, tok := e.loggedIn(t)
	m := wantOK(t, e.do(t, "PUT", "/profile", `{"nickname":"阿福"}`, withToken(tok)))
	if u := userOf(t, m); u["nickname"] != "阿福" || u["name"] != "阿福" {
		t.Fatalf("改昵称: %v", m)
	}
	wantErr(t, e.do(t, "PUT", "/profile", `{"nickname":"  "}`, withToken(tok)), 400, "nickname_invalid", "")
	wantErr(t, e.do(t, "PUT", "/profile", `{"nickname":1}`, withToken(tok)), 400, "bad_request", "")
	wantErr(t, e.do(t, "PUT", "/profile", `{"nickname":null}`, withToken(tok)), 400, "bad_request", "")
	wantErr(t, e.do(t, "PUT", "/profile", `{}`, withToken(tok)), 400, "bad_request", "")
	// 不在开放字段里的一律拒,不静默忽略。
	wantErr(t, e.do(t, "PUT", "/profile", `{"nickname":"a","gender":"MALE"}`, withToken(tok)), 403, "forbidden", "")
	wantErr(t, e.do(t, "PUT", "/profile", `{"nickname":"阿贵"}`), 401, "unauthorized", "")
}

func TestAvatar(t *testing.T) {
	e := newEnv(t)
	_, _, tok := e.loggedIn(t)
	img := append(append([]byte{}, pngHeader...), bytes.Repeat([]byte{1}, 100)...)
	m := wantOK(t, e.doBytes(t, "PUT", "/avatar", img, withToken(tok)))
	url, _ := userOf(t, m)["avatar"].(string)
	if !strings.Contains(url, "/avatar/") || !strings.HasSuffix(url, ".png") {
		t.Fatalf("头像地址: %v", m)
	}
	wantErr(t, e.doBytes(t, "PUT", "/avatar", bytes.Repeat([]byte{1}, 256<<10+1), withToken(tok)), 413, "avatar_too_large", "")
	wantErr(t, e.doBytes(t, "PUT", "/avatar", []byte("not an image"), withToken(tok)), 400, "avatar_invalid", "")
	// 空体 = 清除。
	if u := userOf(t, wantOK(t, e.doBytes(t, "PUT", "/avatar", nil, withToken(tok)))); u["avatar"] != "" {
		t.Fatalf("清除: %v", u)
	}
}

func TestPassword(t *testing.T) {
	e := newEnv(t)
	_, phone, tok := e.loggedIn(t)
	send := func() string {
		wantOK(t, e.do(t, "POST", "/code", `{"purpose":"password","channel":"sms"}`, withToken(tok)))
		return e.lastCode(t)
	}
	code := send()
	// 先判规则再验码:不合规不该把码消耗掉。
	wantErr(t, e.do(t, "POST", "/password", `{"code":"`+code+`","password":"abcdefgh"}`, withToken(tok)), 400, "password_invalid", "")
	wantErr(t, e.do(t, "POST", "/password", `{"code":"000000","password":"Abcdefg1"}`, withToken(tok)), 400, "code_invalid", "")
	wantErr(t, e.do(t, "POST", "/password", `{"code":"","password":"Abcdefg1"}`, withToken(tok)), 400, "bad_request", "")
	m := wantOK(t, e.do(t, "POST", "/password", `{"code":"`+code+`","password":"Abcdefg1"}`, withToken(tok)))
	if u := userOf(t, m); u["has_password"] != true {
		t.Fatalf("设密码: %v", m)
	}
	wantOK(t, e.do(t, "POST", "/login", `{"type":"password","account":"`+phone+`","password":"Abcdefg1"}`))
}

func TestIdentityBindPhone(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, _, tok := e.loggedIn(t)
	sendBind := func(phone string) string {
		wantOK(t, e.do(t, "POST", "/code", `{"purpose":"bind","channel":"sms","target":"`+phone+`"}`, withToken(tok)))
		return e.lastCode(t)
	}
	bind := func(phone, code, ticket string) *httptest.ResponseRecorder {
		return e.do(t, "POST", "/identity/bind", `{"type":"phone","phone":"`+phone+`","code":"`+code+`","ticket":"`+ticket+`"}`, withToken(tok))
	}
	// 已有手机号:没有票据不让换。
	p1 := newPhone()
	wantErr(t, bind(p1, sendBind(p1), ""), 400, "ticket_invalid", "")
	wantErr(t, bind(p1, "123456", "forged"), 400, "ticket_invalid", "")

	wantOK(t, e.do(t, "POST", "/code", `{"purpose":"verify","channel":"sms"}`, withToken(tok)))
	ticket, _ := wantOK(t, e.do(t, "POST", "/code/verify", `{"purpose":"verify","channel":"sms","code":"`+e.lastCode(t)+`"}`, withToken(tok)))["ticket"].(string)
	// 新号的码输错:票据放回去,改对了还能用。
	code := sendBind(p1)
	wantErr(t, bind(p1, "000000", ticket), 400, "code_invalid", "")
	m := wantOK(t, bind(p1, code, ticket))
	if userOf(t, m)["phone"] != p1[:3]+"****"+p1[7:] {
		t.Fatalf("换绑: %v", m)
	}
	// 别人的号。
	_, taken := e.newUser(t)
	wantErr(t, e.do(t, "POST", "/code", `{"purpose":"bind","channel":"sms","target":"`+taken+`"}`, withToken(tok)), 409, "phone_taken", "")

	// 没有手机号(微信注册的号)首次绑定不要票据。
	u, _, _, err := e.users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{Type: domain.IdentityTypeWechatWeb, Subject: "oxW"})
	if err != nil {
		t.Fatal(err)
	}
	tok = e.issue(t, u.ID)
	p2 := newPhone()
	if got := userOf(t, wantOK(t, bind(p2, sendBind(p2), "")))["phone"]; got != p2[:3]+"****"+p2[7:] {
		t.Fatalf("首次绑定: %v", got)
	}
	for _, b := range []string{`{"type":"phone","phone":"1390000000","code":"123456"}`, `{"type":"phone","phone":"13900000000","code":"1"}`,
		`{"type":"x"}`, `x`} {
		wantErr(t, e.do(t, "POST", "/identity/bind", b, withToken(tok)), 400, "bad_request", "")
	}
}

func TestIdentityBindEmail(t *testing.T) {
	e := newEnv(t)
	_, _, tok := e.loggedIn(t)
	wantOK(t, e.do(t, "POST", "/code", `{"purpose":"bind","channel":"email","target":"zhangwei@example.com"}`, withToken(tok)))
	code := e.lastCode(t)
	wantErr(t, e.do(t, "POST", "/identity/bind", `{"type":"email","email":"zhangwei@example.com","code":"000000"}`, withToken(tok)), 400, "code_invalid", "")
	m := wantOK(t, e.do(t, "POST", "/identity/bind", `{"type":"email","email":"zhangwei@example.com","code":"`+code+`"}`, withToken(tok)))
	if userOf(t, m)["email"] != "zh***@example.com" {
		t.Fatalf("绑邮箱: %v", m)
	}
	_, _, tok2 := e.loggedIn(t)
	wantErr(t, e.do(t, "POST", "/code", `{"purpose":"bind","channel":"email","target":"zhangwei@example.com"}`, withToken(tok2)), 409, "email_taken", "")
	wantErr(t, e.do(t, "POST", "/identity/bind", `{"type":"email","email":"ab.com","code":"123456"}`, withToken(tok2)), 400, "bad_request", "")
}

// 换绑邮箱:先验身份(当前邮箱或当前手机号)拿票据;验邮箱换来的票据换不了手机号;新号填自己当前的直接拦。
func TestIdentityChangeEmail(t *testing.T) {
	e := newEnv(t)
	_, phone, tok := e.loggedIn(t)
	mail := func() string { return fmt.Sprintf("u%d@example.com", phoneSeq.Add(1)) }
	sendBind := func(addr string) string {
		wantOK(t, e.do(t, "POST", "/code", `{"purpose":"bind","channel":"email","target":"`+addr+`"}`, withToken(tok)))
		return e.lastCode(t)
	}
	bind := func(addr, code, ticket string) *httptest.ResponseRecorder {
		return e.do(t, "POST", "/identity/bind", `{"type":"email","email":"`+addr+`","code":"`+code+`","ticket":"`+ticket+`"}`, withToken(tok))
	}
	verify := func(channel string) string {
		wantOK(t, e.do(t, "POST", "/code", `{"purpose":"verify","channel":"`+channel+`"}`, withToken(tok)))
		m := wantOK(t, e.do(t, "POST", "/code/verify", `{"purpose":"verify","channel":"`+channel+`","code":"`+e.lastCode(t)+`"}`, withToken(tok)))
		tk, _ := m["ticket"].(string)
		if tk == "" {
			t.Fatalf("验 %s 没给票据: %v", channel, m)
		}
		return tk
	}
	// 还没绑邮箱:验不了「当前邮箱」;首次绑定不要票据。
	wantErr(t, e.do(t, "POST", "/code", `{"purpose":"verify","channel":"email"}`, withToken(tok)), 400, "no_email", "")
	m1 := mail()
	wantOK(t, bind(m1, sendBind(m1), ""))

	// 已绑定:没票据、伪造票据都不让换。
	m2 := mail()
	wantErr(t, bind(m2, sendBind(m2), ""), 400, "ticket_invalid", "")
	wantErr(t, bind(m2, "123456", "forged"), 400, "ticket_invalid", "")

	// 验当前邮箱换来的票据:换不了手机号,放回去后照样能换邮箱;新邮箱的码输错票据也放回。
	tk := verify("email")
	p1 := newPhone()
	wantOK(t, e.do(t, "POST", "/code", `{"purpose":"bind","channel":"sms","target":"`+p1+`"}`, withToken(tok)))
	wantErr(t, e.do(t, "POST", "/identity/bind", `{"type":"phone","phone":"`+p1+`","code":"`+e.lastCode(t)+`","ticket":"`+tk+`"}`, withToken(tok)), 400, "ticket_invalid", "")
	code := sendBind(m2)
	wantErr(t, bind(m2, "000000", tk), 400, "code_invalid", "")
	if got := userOf(t, wantOK(t, bind(m2, code, tk)))["email"]; got != m2[:2]+"***"+m2[strings.Index(m2, "@"):] {
		t.Fatalf("验邮箱后换绑: %v", got)
	}
	// 票据一次性。
	m3 := mail()
	wantErr(t, bind(m3, sendBind(m3), tk), 400, "ticket_invalid", "")

	// 验当前手机号换来的票据也能换邮箱。
	tk = verify("sms")
	wantOK(t, bind(m3, sendBind(m3), tk))

	// 新号填的就是自己当前的:发码就拦,不消耗验证码。
	wantErr(t, e.do(t, "POST", "/code", `{"purpose":"bind","channel":"email","target":"`+m3+`"}`, withToken(tok)), 400, "email_same", "")
	wantErr(t, e.do(t, "POST", "/code", `{"purpose":"bind","channel":"sms","target":"`+phone+`"}`, withToken(tok)), 400, "phone_same", "")
}

func TestIdentityWechatBindUnbind(t *testing.T) {
	e := newEnv(t)
	_, _, tok := e.loggedIn(t)
	st := wantOK(t, e.do(t, "POST", "/wechat/start", `{"purpose":"bind"}`, withToken(tok)))
	ticket := e.wechatTicket(t, st["state"].(string), "oxB:unB:阿福")
	m := wantOK(t, e.do(t, "POST", "/identity/bind", `{"type":"wechat","ticket":"`+ticket+`"}`, withToken(tok)))
	if userOf(t, m)["wechat"] != "阿福" {
		t.Fatalf("绑微信: %v", m)
	}
	wantErr(t, e.do(t, "POST", "/identity/bind", `{"type":"wechat","ticket":""}`, withToken(tok)), 400, "bad_request", "")

	// 同一个微信绑给另一个人。
	_, _, tok2 := e.loggedIn(t)
	st = wantOK(t, e.do(t, "POST", "/wechat/start", `{"purpose":"bind"}`, withToken(tok2)))
	ticket = e.wechatTicket(t, st["state"].(string), "oxB:unB:阿福")
	wantErr(t, e.do(t, "POST", "/identity/bind", `{"type":"wechat","ticket":"`+ticket+`"}`, withToken(tok2)), 409, "wechat_taken", "")

	m = wantOK(t, e.do(t, "POST", "/identity/unbind", `{"type":"wechat"}`, withToken(tok)))
	if userOf(t, m)["wechat"] != "" {
		t.Fatalf("解绑: %v", m)
	}
	// 本来就没绑:结果就是用户要的。
	wantOK(t, e.do(t, "POST", "/identity/unbind", `{"type":"wechat"}`, withToken(tok)))
	wantErr(t, e.do(t, "POST", "/identity/unbind", `{"type":"phone"}`, withToken(tok)), 400, "bad_request", "")

	// 只有微信的号解绑后没法登录。
	u, _, _, err := e.users.EnsureUserWithIdentity(context.Background(), service.EnsureIdentityInput{Type: domain.IdentityTypeWechatWeb, Subject: "oxOnly"})
	if err != nil {
		t.Fatal(err)
	}
	wantErr(t, e.do(t, "POST", "/identity/unbind", `{"type":"wechat"}`, withToken(e.issue(t, u.ID))), 409, "last_identity", "")
}

func TestNotify(t *testing.T) {
	e := newEnv(t)
	_, _, tok := e.loggedIn(t)
	w := e.do(t, "GET", "/notify/qr", "", withToken(tok))
	m := wantOK(t, w)
	if w.Header().Get("Cache-Control") != "no-store" || m["content"] != "http://weixin.qq.com/q/x" || m["expires_in"] != float64(600) {
		t.Fatalf("notify/qr: %v %v", w.Header(), m)
	}
	wantErr(t, e.do(t, "GET", "/notify/qr", ""), 401, "unauthorized", "")
	if u := userOf(t, wantOK(t, e.do(t, "POST", "/notify/off", "", withToken(tok)))); u["notify"] != false {
		t.Fatalf("notify/off: %v", u)
	}
}

func TestDeleteRevokesToken(t *testing.T) {
	e := newEnv(t)
	_, _, tok := e.loggedIn(t)
	m := wantOK(t, e.do(t, "POST", "/delete", "", withToken(tok)))
	if p, _ := m["purge_at"].(float64); p <= 0 {
		t.Fatalf("delete: %v", m)
	}
	wantErr(t, e.do(t, "GET", "/me", "", withToken(tok)), 401, "unauthorized", "")
	wantErr(t, e.do(t, "POST", "/delete", ""), 401, "unauthorized", "")
}
