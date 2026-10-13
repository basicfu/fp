package domain_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/basicfu/fp/internal/domain"
)

var (
	jpegHead = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}
	pngHead  = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	webpHead = []byte("RIFF\x24\x00\x00\x00WEBPVP8 ")
)

// padTo 把文件头补到 n 字节，用来踩大小边界。
func padTo(head []byte, n int) []byte {
	out := make([]byte, n)
	copy(out, head)
	return out
}

func TestCheckAvatar(t *testing.T) {
	cases := []struct {
		name  string
		image []byte
		want  string // 空串表示应被拒
	}{
		{"JPEG", jpegHead, "image/jpeg"},
		{"PNG", pngHead, "image/png"},
		{"WebP", webpHead, "image/webp"},
		{"恰好 1 MB", padTo(pngHead, domain.AvatarMaxBytes), "image/png"},
		{"1 MB 多 1 字节", padTo(pngHead, domain.AvatarMaxBytes+1), ""},
		{"GIF", []byte("GIF89a\x01\x00\x01\x00"), ""},
		{"SVG", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), ""},
		{"空文件", nil, ""},
		{"随机字节", []byte{0x13, 0x37, 0xC0, 0xDE, 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07}, ""},
		// 只认内容不认名字：一个改了扩展名的可执行文件、一段 HTML，都不是图片。
		{"伪装成图片的可执行文件", []byte("MZ\x90\x00\x03\x00\x00\x00\x04\x00\x00\x00"), ""},
		{"伪装成图片的 HTML", []byte("<html><script>alert(1)</script></html>"), ""},
		{"RIFF 但不是 WebP", []byte("RIFF\x24\x00\x00\x00WAVEfmt "), ""},
		{"文件头不完整的 WebP", []byte("RIFF\x24\x00\x00\x00WEB"), ""},
		{"文件头不在开头", append([]byte{0x00}, jpegHead...), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := domain.CheckAvatar(c.image)
			if c.want == "" {
				var de *domain.Error
				if !errors.As(err, &de) || de.Code != domain.CodeAvatarInvalid {
					t.Fatalf("err = %v, want AVATAR_INVALID", err)
				}
				if !errors.Is(err, domain.ErrInvalidArgument) {
					t.Fatalf("哨兵应为 ErrInvalidArgument: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("CheckAvatar: %v", err)
			}
			if got != c.want {
				t.Fatalf("contentType = %q, want %q", got, c.want)
			}
		})
	}
}

func TestAvatarHash(t *testing.T) {
	h := domain.AvatarHash(pngHead)
	// SHA-256 前 16 字节的十六进制。业务方拿它拼图片地址、当缓存文件名，形状是对外契约。
	if len(h) != 32 || !domain.IsAvatarHash(h) {
		t.Fatalf("hash = %q，应为 32 位小写十六进制", h)
	}
	if h != domain.AvatarHash(bytes.Clone(pngHead)) {
		t.Fatal("同样的字节应得到同样的哈希")
	}
	if h == domain.AvatarHash(jpegHead) {
		t.Fatal("不同的字节不该得到同样的哈希")
	}
	// 固定向量：防止有人把截断长度或算法悄悄换掉——那会让所有已缓存的地址一起失效。
	if got := domain.AvatarHash([]byte("fp")); got != "5aef96240bca5582e6fd0468879a2686" {
		t.Fatalf("AvatarHash(\"fp\") = %s", got)
	}
}

func TestIsAvatarHash(t *testing.T) {
	for s, want := range map[string]bool{
		"0123456789abcdef0123456789abcdef":  true,
		"0123456789ABCDEF0123456789ABCDEF":  false,
		"0123456789abcdef0123456789abcde":   false,
		"0123456789abcdef0123456789abcdef0": false,
		"../../etc/passwd0000000000000000":  false,
		"":                                  false,
	} {
		if got := domain.IsAvatarHash(s); got != want {
			t.Errorf("IsAvatarHash(%q) = %v, want %v", s, got, want)
		}
	}
}
