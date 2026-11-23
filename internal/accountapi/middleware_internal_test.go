package accountapi

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIPTrustsXFFOnlyFromProxies(t *testing.T) {
	h := &handler{d: Deps{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}}
	for _, c := range []struct {
		remote string
		xff    []string
		want   string
	}{
		{"10.1.2.3:5000", []string{"6.6.6.6, 1.2.3.4"}, "1.2.3.4"},
		// 多个头取最后一个头的最后一跳:前面的都是客户端自己能写的。
		{"10.1.2.3:5000", []string{"6.6.6.6", "7.7.7.7"}, "7.7.7.7"},
		{"10.1.2.3:5000", nil, "10.1.2.3"},
		{"10.1.2.3:5000", []string{"garbage"}, "10.1.2.3"},
		// 不是从网关来的:头是伪造的。
		{"8.8.8.8:5000", []string{"1.2.3.4"}, "8.8.8.8"},
		{"[::ffff:10.0.0.1]:80", []string{"1.2.3.4"}, "1.2.3.4"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		for _, v := range c.xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		if got := h.clientIP(r); got != c.want {
			t.Errorf("%s %v: got %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
	if got := (&handler{}).clientIP(func() *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "10.0.0.1:1"
		r.Header.Set("X-Forwarded-For", "1.2.3.4")
		return r
	}()); got != "10.0.0.1" {
		t.Errorf("没配可信代理时不该认 XFF: %s", got)
	}
}

// 配了可信代理后，不是从代理来的请求（绕过网关直连 fp、伪造 Host）一律 404，连应用都不去认。
func TestUntrustedPeerIsNotFoundWhenProxiesConfigured(t *testing.T) {
	h := &handler{d: Deps{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}}
	reached := false
	next := h.withTrustedPeer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	for _, c := range []struct {
		remote string
		ok     bool
	}{
		{"10.1.2.3:5000", true},
		{"[::ffff:10.0.0.1]:80", true},
		{"8.8.8.8:5000", false},
		{"garbage", false},
	} {
		reached = false
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/me", nil)
		r.RemoteAddr = c.remote
		next.ServeHTTP(w, r)
		if reached != c.ok || (!c.ok && w.Code != http.StatusNotFound) {
			t.Errorf("%s: reached=%v code=%d", c.remote, reached, w.Code)
		}
	}
	// 没配可信代理：不设防（直连部署、测试）。
	open := (&handler{}).withTrustedPeer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	reached = false
	r := httptest.NewRequest("GET", "/me", nil)
	r.RemoteAddr = "8.8.8.8:1"
	open.ServeHTTP(httptest.NewRecorder(), r)
	if !reached {
		t.Error("没配可信代理时不该拦")
	}
}
