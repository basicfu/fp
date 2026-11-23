package accountapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/domain"
)

// 老路径的期望形状逐个抄自业务方原实现的 handler 与其测试(account_test / manage_test / wechat_test /
// profile_test / delete_test):键集合必须一模一样、类型一样,老客户端按 Go 的 Unmarshal 语义严格解析。

var (
	legacyUserKeys    = []string{"avatar_hash", "id", "name", "nickname"}
	legacyAcctKeys    = []string{"avatar_hash", "email", "has_password", "id", "name", "nickname", "notify", "phone", "wechat"}
	wechatStartKeys   = []string{"auth_url", "expires_in", "redirect_uri", "state"}
	hex32             = regexp.MustCompile(`^[0-9a-f]{32}$`)
	legacyUserTypes   = map[string]string{"id": "string", "name": "string", "nickname": "string", "avatar_hash": "string"}
	legacyAcctTypes   = map[string]string{"phone": "string", "email": "string", "wechat": "string", "has_password": "bool", "notify": "bool"}
	legacyNumberTypes = map[string]string{"cooldown_sec": "number", "expires_in": "number", "purge_at": "number"}
)

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func typeOf(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "bool"
	case float64:
		return "number"
	case map[string]any:
		return "object"
	}
	return "other"
}

// shape 校验一个老响应:顶层键集合、user 的键集合与各字段类型。
type shape struct {
	top  []string
	user []string
}

func (s shape) check(t *testing.T, name string, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%s: %d %s %s", name, w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	m := decode(t, w)
	if got := keys(m); !reflect.DeepEqual(got, s.top) {
		t.Fatalf("%s: 顶层键 %v, want %v (%s)", name, got, s.top, w.Body.String())
	}
	for k, v := range m {
		if want, ok := legacyNumberTypes[k]; ok && typeOf(v) != want {
			t.Fatalf("%s: %s 类型 %s", name, k, typeOf(v))
		}
		if k == "token" || k == "ticket" || k == "user_id" || k == "bind_ticket" || k == "state" {
			if s, _ := v.(string); s == "" {
				t.Fatalf("%s: %s 应为非空字符串: %v", name, k, v)
			}
		}
	}
	if s.user == nil {
		return m
	}
	u := userOf(t, m)
	if got := keys(u); !reflect.DeepEqual(got, s.user) {
		t.Fatalf("%s: user 键 %v, want %v", name, got, s.user)
	}
	for k, v := range u {
		want := legacyUserTypes[k]
		if want == "" {
			want = legacyAcctTypes[k]
		}
		if typeOf(v) != want {
			t.Fatalf("%s: user.%s 类型 %s, want %s", name, k, typeOf(v), want)
		}
	}
	if h, _ := u["avatar_hash"].(string); h != "" && !hex32.MatchString(h) {
		t.Fatalf("%s: avatar_hash = %q", name, h)
	}
	if u["name"] == "" {
		t.Fatalf("%s: name 不能为空(客户端拿它判断已登录)", name)
	}
	return m
}

func wantEmptyObject(t *testing.T, name string, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusOK || w.Body.String() != "{}\n" {
		t.Fatalf("%s: got %d %q, want {}", name, w.Code, w.Body.String())
	}
}

func legacySMS(phone, captchaID string) string {
	return `{"phone":"` + phone + `","captcha_id":"` + captchaID + `","x":300,"track":[[0,0,0],[300,1,500]]}`
}

func TestLegacyLoginShapes(t *testing.T) {
	e := newEnv(t)
	L := legacy()
	phone := newPhone()

	wantEmptyObject(t, "sms-code", e.do(t, "POST", "/sms-code", legacySMS(phone, "ok"), L))
	m := shape{top: []string{"token", "user"}, user: legacyUserKeys}.check(t, "login sms",
		e.do(t, "POST", "/login", `{"type":"sms","phone":"`+phone+`","code":"`+e.lastCode(t)+`"}`, L))
	tok := m["token"].(string)
	id := userOf(t, m)["id"].(string)
	masked := phone[:3] + "****" + phone[7:]
	// 字段顺序也照原实现(结构体声明顺序),逐字节比一次。
	wantOK(t, e.do(t, "POST", "/sms-code", legacySMS(phone, "ok"), L))
	w := e.do(t, "POST", "/login", `{"type":"sms","phone":"`+phone+`","code":"`+e.lastCode(t)+`"}`, L)
	tok2 := decode(t, w)["token"].(string)
	if want := `{"token":"` + tok2 + `","user":{"id":"` + id + `","name":"` + masked + `","nickname":"","avatar_hash":""}}` + "\n"; w.Body.String() != want {
		t.Fatalf("login 字节:\n got %s\nwant %s", w.Body.String(), want)
	}
	w = e.do(t, "GET", "/me", "", L, withToken(tok2))
	if want := `{"user_id":"` + id + `","user":{"id":"` + id + `","name":"` + masked + `","nickname":"","avatar_hash":"",` +
		`"phone":"` + masked + `","email":"","wechat":"","has_password":false,"notify":false}}` + "\n"; w.Body.String() != want {
		t.Fatalf("me 字节:\n got %s\nwant %s", w.Body.String(), want)
	}

	me := shape{top: []string{"user", "user_id"}, user: legacyAcctKeys}.check(t, "me", e.do(t, "GET", "/me", "", L, withToken(tok)))
	if me["user_id"] != userOf(t, m)["id"] {
		t.Fatalf("me.user_id: %v", me)
	}
	wantEmptyObject(t, "logout", e.do(t, "POST", "/logout", "", L, withToken(tok)))
	wantEmptyObject(t, "logout 无 token", e.do(t, "POST", "/logout", "", L))
	wantErr(t, e.do(t, "GET", "/me", "", L, withToken(tok)), 401, "unauthorized", "登录已过期，请重新登录")
}

func TestLegacyLoginErrors(t *testing.T) {
	e := newEnv(t)
	L := legacy()
	wantErr(t, e.do(t, "POST", "/sms-code", legacySMS("13800138000", "bad"), L), 400, "captcha_failed", "")
	wantErr(t, e.do(t, "POST", "/sms-code", legacySMS("13800138000", "exp"), L), 400, "captcha_expired", "")
	const capt = `"captcha_id":"ok","x":300,"track":[[0,0,0]]`
	for _, body := range []string{
		`{"phone":"1380013800",` + capt + `}`, `{"phone":"23800138000",` + capt + `}`, `{"phone":"1380013800a",` + capt + `}`,
		`{"phone":"138001380001",` + capt + `}`, `{"phone":"13800138000"}`, `{}`, `not json`,
		`{"phone":"13800138000",` + capt + `,"pad":"` + strings.Repeat("x", 20000) + `"}`,
	} {
		wantErr(t, e.do(t, "POST", "/sms-code", body, L), 400, "bad_request", "")
	}
	for _, body := range []string{
		`{"type":"sms","phone":"1380013800","code":"123456"}`, `{"type":"sms","phone":"13800138000","code":"12345"}`,
		`{"type":"sms","phone":"13800138000","code":"12345a"}`, `{"type":"password","account":"","password":"pw"}`,
		`{"type":"password","account":"root","password":""}`,
		`{"type":"password","account":"` + strings.Repeat("a", 49) + `","password":"pw"}`,
		`{"type":"password","account":"root","password":"` + strings.Repeat("p", 33) + `"}`,
		// 老 /login 只有短信与密码两种。
		`{"type":"wechat","ticket":"t"}`, `{"phone":"13800138000","code":"123456"}`, `{`,
	} {
		wantErr(t, e.do(t, "POST", "/login", body, L), 400, "bad_request", "")
	}
	if len(e.sms.Sent()) != 0 || len(e.check.calls) != 0 {
		t.Fatal("非法请求触达了发码或登录校验")
	}
	wantErr(t, e.do(t, "POST", "/login", `{"type":"sms","phone":"13800138000","code":"000000"}`, L), 401, "login_failed", "")
	wantErr(t, e.do(t, "POST", "/login", `{"type":"password","account":"root","password":"x"}`, L), 401, "login_failed", "")
	wantErr(t, e.do(t, "POST", "/login", `{"type":"password","account":"root","password":"x"}`, L, noDevice()), 400, "version_too_low", "")
	wantErr(t, e.do(t, "GET", "/me", "", L), 401, "unauthorized", "")
	wantErr(t, e.do(t, "GET", "/me", "", L, withToken("tok-expired")), 401, "unauthorized", "")
}

func TestLegacyWechatShapes(t *testing.T) {
	e := newEnv(t)
	L := legacy()
	st := shape{top: wechatStartKeys}.check(t, "wechat/start", e.do(t, "POST", "/wechat/start", "", L))
	ticket := e.wechatTicket(t, st["state"].(string), "oxL:unL:小白")
	shape{top: []string{"bind_ticket", "nickname"}}.check(t, "wechat/login 首次", e.do(t, "POST", "/wechat/login", `{"ticket":"`+ticket+`"}`, L))
	bt := decode(t, e.do(t, "POST", "/wechat/login", `{"ticket":"`+e.wechatTicket(t,
		decode(t, e.do(t, "POST", "/wechat/start", "", L))["state"].(string), "oxL:unL:小白")+`"}`, L))["bind_ticket"].(string)

	phone := newPhone()
	wantEmptyObject(t, "sms-code", e.do(t, "POST", "/sms-code", legacySMS(phone, "ok"), L))
	m := shape{top: []string{"token", "user"}, user: legacyUserKeys}.check(t, "wechat/bind",
		e.do(t, "POST", "/wechat/bind", `{"bind_ticket":"`+bt+`","phone":"`+phone+`","code":"`+e.lastCode(t)+`"}`, L))
	if userOf(t, m)["nickname"] != "小白" {
		t.Fatalf("wechat/bind: %v", m)
	}
	ticket = e.wechatTicket(t, decode(t, e.do(t, "POST", "/wechat/start", "", L))["state"].(string), "oxL:unL:小白")
	shape{top: []string{"token", "user"}, user: legacyUserKeys}.check(t, "wechat/login 老用户",
		e.do(t, "POST", "/wechat/login", `{"ticket":"`+ticket+`"}`, L))

	wantErr(t, e.do(t, "POST", "/wechat/login", `{"ticket":"`+ticket+`"}`, L), 400, "wechat_expired", "")
	wantErr(t, e.do(t, "POST", "/wechat/login", `{"ticket":""}`, L), 400, "bad_request", "")
	wantErr(t, e.do(t, "POST", "/wechat/bind", `{"bind_ticket":"x","phone":"1","code":"123456"}`, L), 400, "bad_request", "")
}

func TestLegacyProfileAvatarShapes(t *testing.T) {
	e := newEnv(t)
	L := legacy()
	_, _, tok := e.loggedIn(t)
	m := shape{top: []string{"user"}, user: legacyUserKeys}.check(t, "profile",
		e.do(t, "PUT", "/profile", `{"nickname":"阿福"}`, L, withToken(tok)))
	if u := userOf(t, m); u["name"] != "阿福" || u["nickname"] != "阿福" {
		t.Fatalf("profile: %v", u)
	}
	wantErr(t, e.do(t, "PUT", "/profile", `{}`, L, withToken(tok)), 400, "bad_request", "")
	wantErr(t, e.do(t, "PUT", "/profile", `{"nickname":"   "}`, L, withToken(tok)), 400, "nickname_invalid", "")

	img := append(append([]byte{}, pngHeader...), bytes.Repeat([]byte{2}, 64)...)
	m = shape{top: []string{"user"}, user: legacyUserKeys}.check(t, "avatar", e.doBytes(t, "PUT", "/avatar", img, L, withToken(tok)))
	hash := userOf(t, m)["avatar_hash"].(string)
	if hash == "" {
		t.Fatalf("avatar_hash 为空: %v", m)
	}
	wantErr(t, e.doBytes(t, "PUT", "/avatar", bytes.Repeat([]byte{1}, 256<<10+1), L, withToken(tok)), 413, "avatar_too_large", "头像图片不能超过 256 KB")

	// 老客户端按 hash 取头像:302 到对象存储上的新地址,不要登录。
	w := e.do(t, "GET", "/avatar/"+hash, "", L, noDevice())
	newURL := userOf(t, wantOK(t, e.do(t, "GET", "/me", "", withToken(tok))))["avatar"]
	// 302 不让缓存:换了头像或迁移回滚时,缓存的跳转会一直指向旧地址。
	if w.Code != http.StatusFound || w.Header().Get("Location") != newURL || newURL == "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("avatar/{hash}: %d %v, want → %v", w.Code, w.Header(), newURL)
	}
	wantErr(t, e.do(t, "GET", "/avatar/"+strings.Repeat("0", 32), "", L), 404, "not_found", "")
	wantErr(t, e.do(t, "GET", "/avatar/xyz", "", L), 400, "bad_request", "")
	// 还没迁到对象存储的老头像:照老样子直接回图片。
	if err := e.db(context.Background(), `UPDATE user_avatar SET object_key = '', data = $2 WHERE hash = $1`, hash, img); err != nil {
		t.Fatal(err)
	}
	w = e.do(t, "GET", "/avatar/"+hash, "", L)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), img) || w.Header().Get("Content-Type") != "image/png" ||
		w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("未迁移的头像: %d %v", w.Code, w.Header())
	}
}

func TestLegacyManageShapes(t *testing.T) {
	e := newEnv(t)
	L := legacy()
	_, phone, tok := e.loggedIn(t)
	T := withToken(tok)
	code := shape{top: []string{"cooldown_sec"}}

	for _, c := range []struct{ body, to string }{
		{`{"purpose":"verify_phone","target":"13900000000"}`, phone},
		{`{"purpose":"password","target":""}`, phone},
		{`{"purpose":"new_phone","target":"13900000000"}`, "13900000000"},
		{`{"purpose":"email","target":"a@b.com"}`, "a@b.com"},
	} {
		if m := code.check(t, "code "+c.body, e.do(t, "POST", "/code", c.body, L, T)); m["cooldown_sec"] != float64(60) || e.lastTo() != c.to {
			t.Fatalf("%s: %v → %s", c.body, m, e.lastTo())
		}
	}
	for _, b := range []string{`{"purpose":"new_phone","target":""}`, `{"purpose":"new_phone","target":"2390000000"}`,
		`{"purpose":"email","target":"no-at"}`, `{"purpose":"email","target":"a@b"}`, `{"purpose":"x"}`, `nope`,
		// 新接口的用途不认:老客户端不会发,发了也不能走进新逻辑。
		`{"purpose":"verify","channel":"sms"}`} {
		wantErr(t, e.do(t, "POST", "/code", b, L, T), 400, "bad_request", "")
	}
	wantErr(t, e.do(t, "POST", "/code", `{"purpose":"verify_phone"}`, L), 401, "unauthorized", "")

	// 换绑手机号。
	wantOK(t, e.do(t, "POST", "/code", `{"purpose":"verify_phone"}`, L, T))
	wantErr(t, e.do(t, "POST", "/phone/verify", `{"code":"12"}`, L, T), 400, "bad_request", "")
	m := shape{top: []string{"ticket"}}.check(t, "phone/verify", e.do(t, "POST", "/phone/verify", `{"code":"`+e.lastCode(t)+`"}`, L, T))
	newPhone := "13900000001"
	wantOK(t, e.do(t, "POST", "/code", `{"purpose":"new_phone","target":"`+newPhone+`"}`, L, T))
	wantErr(t, e.do(t, "POST", "/phone/bind", `{"phone":"`+newPhone+`","code":"`+e.lastCode(t)+`"}`, L, T), 400, "ticket_invalid", "")
	acct := shape{top: []string{"user"}, user: legacyAcctKeys}
	m = acct.check(t, "phone/bind", e.do(t, "POST", "/phone/bind",
		`{"phone":"`+newPhone+`","code":"`+e.lastCode(t)+`","ticket":"`+m["ticket"].(string)+`"}`, L, T))
	if userOf(t, m)["phone"] != "139****0001" {
		t.Fatalf("phone/bind: %v", m)
	}

	wantOK(t, e.do(t, "POST", "/code", `{"purpose":"email","target":"zhangwei@example.com"}`, L, T))
	m = acct.check(t, "email/bind", e.do(t, "POST", "/email/bind", `{"email":"zhangwei@example.com","code":"`+e.lastCode(t)+`"}`, L, T))
	if userOf(t, m)["email"] != "zh***@example.com" {
		t.Fatalf("email/bind: %v", m)
	}
	wantErr(t, e.do(t, "POST", "/email/bind", `{"email":"ab.com","code":"123456"}`, L, T), 400, "bad_request", "")

	st := shape{top: wechatStartKeys}.check(t, "wechat/bind-start", e.do(t, "POST", "/wechat/bind-start", "", L, T))
	ticket := e.wechatTicket(t, st["state"].(string), "oxM:unM:惊呆了小白")
	m = acct.check(t, "wechat/bind-confirm", e.do(t, "POST", "/wechat/bind-confirm", `{"ticket":"`+ticket+`"}`, L, T))
	if userOf(t, m)["wechat"] != "惊呆了小白" {
		t.Fatalf("bind-confirm: %v", m)
	}
	wantErr(t, e.do(t, "POST", "/wechat/bind-confirm", `{"ticket":""}`, L, T), 400, "bad_request", "")
	wantErr(t, e.do(t, "POST", "/wechat/bind-start", "", L), 401, "unauthorized", "")
	m = acct.check(t, "wechat/unbind", e.do(t, "POST", "/wechat/unbind", "", L, T))
	if userOf(t, m)["wechat"] != "" {
		t.Fatalf("unbind: %v", m)
	}

	wantOK(t, e.do(t, "POST", "/code", `{"purpose":"password"}`, L, T))
	pc := e.lastCode(t)
	wantErr(t, e.do(t, "POST", "/password", `{"code":"`+pc+`","password":"abcdefgh"}`, L, T), 400, "password_invalid", "")
	wantErr(t, e.do(t, "POST", "/password", `{"code":"000000","password":"Abcdefg1"}`, L, T), 400, "code_invalid", "")
	wantEmptyObject(t, "password", e.do(t, "POST", "/password", `{"code":"`+pc+`","password":"Abcdefg1"}`, L, T))

	shape{top: []string{"content", "expires_in"}}.check(t, "notify/qr", e.do(t, "POST", "/notify/qr", "", L, T))
	if m := (shape{top: []string{"notify"}}).check(t, "notify/state", e.do(t, "GET", "/notify/state", "", L, T)); m["notify"] != false {
		t.Fatalf("notify/state: %v", m)
	}
	wantErr(t, e.do(t, "GET", "/notify/state", "", L), 401, "unauthorized", "")
	wantEmptyObject(t, "notify/off", e.do(t, "POST", "/notify/off", "", L, T))

	m = shape{top: []string{"purge_at"}}.check(t, "delete", e.do(t, "POST", "/delete", "", L, T))
	if m["purge_at"].(float64) < float64(time.Now().UnixMilli()) {
		t.Fatalf("purge_at: %v", m)
	}
	wantErr(t, e.do(t, "GET", "/me", "", L, T), 401, "unauthorized", "")
}

// token 轮换时,每个带 token 字段的老接口都要把新 token 带回去;老格式本来不带的(bind-start)不能多出来。
func TestLegacyRotatedToken(t *testing.T) {
	e := newEnv(t)
	L := legacy()
	_, _, tok := e.loggedIn(t)
	e.clock.Advance(25 * time.Hour)
	m := shape{top: []string{"token", "user", "user_id"}, user: legacyAcctKeys}.check(t, "me 轮换", e.do(t, "GET", "/me", "", L, withToken(tok)))
	newTok := m["token"].(string)
	if newTok == tok {
		t.Fatal("没轮换")
	}
	// 过渡期内旧 token 再来:照样告知新 token。
	shape{top: []string{"token", "user"}, user: legacyUserKeys}.check(t, "profile 轮换",
		e.do(t, "PUT", "/profile", `{"nickname":"阿福"}`, L, withToken(tok)))
	shape{top: wechatStartKeys}.check(t, "bind-start 轮换", e.do(t, "POST", "/wechat/bind-start", "", L, withToken(tok)))
	shape{top: []string{"cooldown_sec", "token"}}.check(t, "code 轮换",
		e.do(t, "POST", "/code", `{"purpose":"verify_phone"}`, L, withToken(tok)))
	shape{top: []string{"token"}}.check(t, "notify/off 轮换", e.do(t, "POST", "/notify/off", "", L, withToken(tok)))
}

// 老格式的错误体:code、msg、time 三个键(detail 没有时省略)。
func TestLegacyErrorBodyShape(t *testing.T) {
	e := newEnv(t)
	w := e.do(t, "GET", "/me", "", legacy())
	var m map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if got := keys(anyMap(m)); !reflect.DeepEqual(got, []string{"code", "msg", "time"}) {
		t.Fatalf("错误体: %s", w.Body.String())
	}
}

func anyMap(m map[string]json.RawMessage) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

// 老路径与业务方原实现同形:已登录接口缺设备头回 device_unverified(登录接口才是 version_too_low);
// 应用没开短信 / 密码登录时老 /login、/sms-code 回 unavailable 而不是 bad_request。新接口不变。
func TestLegacyMatchesOriginalErrorShapes(t *testing.T) {
	e := newEnv(t)
	L := legacy()
	_, _, tok := e.loggedIn(t)
	wantErr(t, e.do(t, "GET", "/me", "", L, withToken(tok), noDevice()), 403, "device_unverified", "")
	wantErr(t, e.do(t, "GET", "/me", "", withToken(tok), noDevice()), 400, "version_too_low", "")

	e.setAccount(t, func(c *domain.AccountConfig) { c.LoginTypes = []string{domain.AccountLoginWechat} })
	wantErr(t, e.do(t, "POST", "/login", `{"type":"password","account":"root","password":"x"}`, L), 503, "unavailable", "")
	wantErr(t, e.do(t, "POST", "/login", `{"type":"sms","phone":"13800138000","code":"123456"}`, L), 503, "unavailable", "")
	wantErr(t, e.do(t, "POST", "/sms-code", legacySMS("13800138000", "ok"), L), 503, "unavailable", "")
	wantErr(t, e.do(t, "POST", "/sms-code", `not json`, L), 400, "bad_request", "")
	wantErr(t, e.do(t, "POST", "/login", `{"type":"password","account":"root","password":"x"}`), 400, "bad_request", "")
	if len(e.sms.Sent()) != 0 {
		t.Fatal("没开短信登录却发了码")
	}
}
