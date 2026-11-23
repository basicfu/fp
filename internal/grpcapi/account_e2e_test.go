package grpcapi

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	"github.com/basicfu/fp/internal/accountapi"
	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/devicesig"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// 账号接口(HTTP)与业务方 gRPC 之间的端到端:同一套服务上,登录校验经真实的 Watch 流问应用,撤销经真实的 gRPC。
// 装配同 cmd/fp/main.go:accountapi 的 Check 是 service.NewLoginCheck(本实例的 LoginCheckRelay)。

const (
	e2eHost         = "acct.e2e.local"
	e2eDeviceHeader = "X-E2E-Device"
	e2ePassword     = "hunter2hunter2"
)

type e2eAccount struct {
	c    *lcCluster
	h    http.Handler
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

// accountAPI 给集群的应用打开账号接口(密码登录、设备绑定),在 node 上起 HTTP 接口。
func (c *lcCluster) accountAPI(t *testing.T, node int, opts service.LoginCheckOptions) *e2eAccount {
	t.Helper()
	ctx := context.Background()
	if err := c.apps.SetConnector(ctx, c.app.ID, connector.TypePassword, true, nil); err != nil {
		t.Fatal(err)
	}
	app, err := c.apps.SetAccount(ctx, c.app.ID, []string{e2eHost}, domain.AccountConfig{
		Enabled: true, LoginTypes: []string{domain.AccountLoginPassword},
		DeviceBinding: true, DeviceHeader: e2eDeviceHeader,
	})
	if err != nil {
		t.Fatal(err)
	}
	c.app = app
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	h := accountapi.NewRouter(accountapi.Deps{
		Apps: c.apps, Auth: c.auth, Users: c.users, Sessions: c.sessions,
		Guard: service.NewDeviceGuard(store.NewNonceStore(c.rdb)),
		Check: c.check(node, opts),
		Redis: c.rdb,
	})
	return &e2eAccount{c: c, h: h, priv: priv, pub: pub}
}

func (a *e2eAccount) deviceHeader(t *testing.T) string {
	t.Helper()
	var n [12]byte
	_, _ = rand.Read(n[:])
	tok := devicesig.Token{SignPub: a.pub, HS: []byte("hs"), TS: time.Now().Unix(), Nonce: hex.EncodeToString(n[:])}
	tok.Sig = ed25519.Sign(a.priv, devicesig.SigningInput(tok))
	raw, err := json.Marshal(tok)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func (a *e2eAccount) do(t *testing.T, method, path, body, token string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = e2eHost
	r.Header.Set(accountapi.APIHeader, accountapi.APIVersion)
	r.Header.Set(e2eDeviceHeader, a.deviceHeader(t))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	a.h.ServeHTTP(w, r)
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("%s %s 响应不是 JSON: %d %q", method, path, w.Code, w.Body.String())
	}
	return w.Code, m
}

func (a *e2eAccount) newUser(t *testing.T) string {
	t.Helper()
	phone := randomPhone()
	u, _, _, err := a.c.users.EnsureUserWithIdentity(context.Background(), service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: phone,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.c.users.SetPassword(context.Background(), u.ID, e2ePassword); err != nil {
		t.Fatal(err)
	}
	return phone
}

func loginBody(phone string) string {
	return `{"type":"password","account":"` + phone + `","password":"` + e2ePassword + `"}`
}

func (a *e2eAccount) login(t *testing.T, phone string) string {
	t.Helper()
	code, m := a.do(t, "POST", "/login", loginBody(phone), "")
	tok, _ := m["token"].(string)
	if code != http.StatusOK || tok == "" {
		t.Fatalf("登录: %d %v", code, m)
	}
	return tok
}

func wantHTTPErr(t *testing.T, what string, code int, m map[string]any, status int, errCode string) {
	t.Helper()
	if code != status || m["code"] != errCode {
		t.Fatalf("%s: got %d %v, want %d %s", what, code, m, status, errCode)
	}
}

// 业务方经 gRPC RevokeSession(kick) 踢掉的会话,客户端调 /me 得到 kicked(客户端据此提示「已被移出账号」);
// 普通登出的得到 unauthorized。
func TestAccountMeKickedAfterRevokeSessionRPC(t *testing.T) {
	c := newLCCluster(t, false)
	c.answer(t, 0, []string{CapLoginCheck}, allowAs("ref-1"))
	a := c.accountAPI(t, 0, service.LoginCheckOptions{})
	phone := a.newUser(t)
	kicked, loggedOut := a.login(t, phone), a.login(t, phone)

	if code, m := a.do(t, "GET", "/me", "", kicked); code != http.StatusOK {
		t.Fatalf("踢之前 /me: %d %v", code, m)
	}
	ctx := metadata.AppendToOutgoingContext(context.Background(), mdAppID, c.app.AppID, mdAppSecret, c.secret)
	rpc := c.nodes[0].client
	v, err := rpc.ValidateToken(ctx, &fpv1.ValidateTokenRequest{Token: kicked})
	if err != nil {
		t.Fatal(err)
	}
	if v.GetClientRef() != "ref-1" {
		t.Fatalf("会话没带上登录校验给的 client_ref: %q", v.GetClientRef())
	}
	res, err := rpc.RevokeSession(ctx, &fpv1.RevokeSessionRequest{UserId: v.GetUserId(), SessionId: v.GetSessionId(), Reason: "kick"})
	if err != nil || res.GetRevoked() != 1 {
		t.Fatalf("RevokeSession: %v %v", res, err)
	}
	code, m := a.do(t, "GET", "/me", "", kicked)
	wantHTTPErr(t, "被踢的 /me", code, m, http.StatusUnauthorized, "kicked")

	if code, m := a.do(t, "POST", "/logout", "{}", loggedOut); code != http.StatusOK {
		t.Fatalf("登出: %d %v", code, m)
	}
	code, m = a.do(t, "GET", "/me", "", loggedOut)
	wantHTTPErr(t, "登出后的 /me", code, m, http.StatusUnauthorized, "unauthorized")
}

// 登录校验的每种结局经 /login 落到客户端认得的码上。
func TestAccountLoginCheckErrorsOverHTTP(t *testing.T) {
	c := newLCCluster(t, false)
	a := c.accountAPI(t, 0, service.LoginCheckOptions{Timeout: 300 * time.Millisecond})
	phone := a.newUser(t)

	// 应用没有在线的流。
	code, m := a.do(t, "POST", "/login", loginBody(phone), "")
	wantHTTPErr(t, "无流", code, m, http.StatusServiceUnavailable, "unavailable")

	var reply atomic.Pointer[func(*fpv1.LoginCheck) *fpv1.LoginCheckReply]
	c.answer(t, 0, []string{CapLoginCheck}, func(lc *fpv1.LoginCheck) *fpv1.LoginCheckReply { return (*reply.Load())(lc) })
	for _, tc := range []struct {
		name   string
		reply  *fpv1.LoginCheckReply
		status int
		code   string
	}{
		{"超时", nil, http.StatusServiceUnavailable, "unavailable"},
		{"放行却没给 client_ref", &fpv1.LoginCheckReply{Allow: true}, http.StatusServiceUnavailable, "unavailable"},
		{"应用自己判定不了", &fpv1.LoginCheckReply{Code: "unavailable"}, http.StatusServiceUnavailable, "unavailable"},
		{"拒绝没给码", &fpv1.LoginCheckReply{}, http.StatusUnauthorized, "login_failed"},
		{"拒绝给了不认识的码", &fpv1.LoginCheckReply{Code: "banned_device"}, http.StatusUnauthorized, "login_failed"},
		{"拒绝给了客户端认得的码", &fpv1.LoginCheckReply{Code: "version_too_low", Msg: "请升级"}, http.StatusBadRequest, "version_too_low"},
		{"放行", &fpv1.LoginCheckReply{Allow: true, ClientRef: "ref-ok"}, http.StatusOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fn := func(*fpv1.LoginCheck) *fpv1.LoginCheckReply {
				if tc.reply == nil {
					return nil
				}
				return proto.Clone(tc.reply).(*fpv1.LoginCheckReply)
			}
			reply.Store(&fn)
			code, m := a.do(t, "POST", "/login", loginBody(phone), "")
			if tc.code == "" {
				if code != tc.status || m["token"] == nil {
					t.Fatalf("got %d %v", code, m)
				}
				return
			}
			wantHTTPErr(t, tc.name, code, m, tc.status, tc.code)
		})
	}
}
