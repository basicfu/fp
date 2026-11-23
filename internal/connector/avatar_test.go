package connector_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
)

var avatarPNG = []byte("\x89PNG\r\n\x1a\navatar")

func TestAvatarFetcherRejects(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/jump":
			http.Redirect(w, r, "http://localhost:1/x.png", http.StatusFound)
		case "/text":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write(avatarPNG)
		case "/notimage":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("<html>"))
		case "/big":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(append(append([]byte{}, avatarPNG...), make([]byte, domain.AvatarMaxBytes)...))
		default:
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(avatarPNG)
		}
	}))
	t.Cleanup(srv.Close)

	if _, err := connector.NewAvatarFetcher(connector.WechatAvatarHosts...).Fetch(ctx, srv.URL+"/ok.png"); err == nil {
		t.Fatal("白名单外的域名应被拒")
	}
	f := connector.NewAvatarFetcher("127.0.0.1")
	if img, err := f.Fetch(ctx, srv.URL+"/ok.png"); err != nil || !bytes.Equal(img, avatarPNG) {
		t.Fatalf("白名单内应下载成功：%v", err)
	}
	for _, path := range []string{"/jump", "/text", "/notimage", "/big"} {
		if _, err := f.Fetch(ctx, srv.URL+path); err == nil {
			t.Fatalf("%s 应被拒", path)
		}
	}
	if _, err := f.Fetch(ctx, "file:///etc/passwd"); err == nil {
		t.Fatal("非 http(s) 应被拒")
	}
}

func TestIsWechatDefaultAvatar(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://thirdwx.qlogo.cn/mmopen/vi_32/" + connector.WechatDefaultAvatarID + "/132": true,
		"https://thirdwx.qlogo.cn/mmopen/vi_32/" + connector.WechatDefaultAvatarID + "/0":   true,
		"https://thirdwx.qlogo.cn/mmopen/vi_32/abcdef/132":                                  false,
		"":       false,
		"://bad": false,
	} {
		if got := connector.IsWechatDefaultAvatar(raw); got != want {
			t.Errorf("IsWechatDefaultAvatar(%q) = %v, want %v", raw, got, want)
		}
	}
}
