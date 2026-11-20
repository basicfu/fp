package httpapi

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/basicfu/fp/internal/notify"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

func mpSign(parts ...string) string {
	sort.Strings(parts)
	sum := sha1.Sum([]byte(strings.Join(parts, "")))
	return hex.EncodeToString(sum[:])
}

func TestWechatMPCallbackRoute(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)
	ns := service.NewNotifyService(pool, rdb, notify.DefaultRegistry())
	if _, err := ns.CreateProvider(context.Background(), service.CreateNotifyProviderInput{
		Type: notify.TypeWechatMP, Enabled: true, Config: map[string]any{"appId": "wxmp", "appSecret": "s", "token": "tk"},
	}); err != nil {
		t.Fatal(err)
	}
	h := NewRouter(Deps{WechatMP: service.NewWechatMPService(pool, rdb)})
	sig := mpSign("tk", "1", "n")

	// 公众平台填服务器地址时的校验：原样回写 echostr。
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, notify.WechatMPCallbackPath+"?"+url.Values{
		"signature": {sig}, "timestamp": {"1"}, "nonce": {"n"}, "echostr": {"e123"},
	}.Encode(), nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "e123" {
		t.Fatalf("GET: code = %d body = %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, notify.WechatMPCallbackPath+"?signature=bad&timestamp=1&nonce=n&echostr=e", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("签名不对: code = %d", rec.Code)
	}

	// 取关推送：记下状态、回 success。
	body := `<xml><ToUserName>gh_1</ToUserName><FromUserName>o1</FromUserName><CreateTime>100</CreateTime>` +
		`<MsgType>event</MsgType><Event>unsubscribe</Event></xml>`
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, notify.WechatMPCallbackPath+"?signature="+sig+"&timestamp=1&nonce=n", strings.NewReader(body)))
	if rec.Code != http.StatusOK || rec.Body.String() != "success" {
		t.Fatalf("POST: code = %d body = %q", rec.Code, rec.Body.String())
	}
	var subscribed bool
	if err := pool.QueryRow(context.Background(), `SELECT subscribed FROM wechat_mp_follower WHERE openid = 'o1'`).Scan(&subscribed); err != nil || subscribed {
		t.Fatalf("subscribed = %v, err = %v", subscribed, err)
	}
}
