package captcha

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func TestSealRoundTrip(t *testing.T) {
	s, _ := NewSealer("sec")
	now := time.Unix(1_700_000_000, 0)
	id := s.Seal(312, 45, now.Add(2*time.Minute))
	x, y, exp, err := s.Open(id, now)
	if err != nil || x != 312 || y != 45 || !exp.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("got %d %d %v %v", x, y, exp, err)
	}
	// 同一答案每次封出来都不同(随机 nonce),否则同题 5 次会得到同一张票。
	if s.Seal(312, 45, now) == id {
		t.Fatal("两次 Seal 结果相同")
	}
}

func TestOpenRejects(t *testing.T) {
	s, _ := NewSealer("sec")
	other, _ := NewSealer("other")
	now := time.Unix(1_700_000_000, 0)
	id := s.Seal(1, 2, now.Add(time.Minute))
	if _, _, _, err := s.Open(id, now.Add(2*time.Minute)); !errors.Is(err, ErrExpired) {
		t.Fatalf("过期: %v", err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(id)
	// 末字符只有低 4 位是填充位:换掉它们,非严格解码得到的字节不变,严格解码必须拒掉这种变体。
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	var variant string
	for _, c := range alphabet {
		v := id[:len(id)-1] + string(c)
		if got, err := base64.RawURLEncoding.DecodeString(v); v != id && err == nil && bytes.Equal(got, raw) {
			variant = v
			break
		}
	}
	if variant == "" {
		t.Fatal("找不到填充位变体")
	}
	// 翻转密文最后一字节(属于 GCM tag),结果确定地失败,不依赖随机 nonce。
	raw[len(raw)-1] ^= 1
	tampered := base64.RawURLEncoding.EncodeToString(raw)
	for _, bad := range []string{"", "!!", tampered, variant} {
		if _, _, _, err := s.Open(bad, now); !errors.Is(err, ErrFailed) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	if _, _, _, err := other.Open(id, now); !errors.Is(err, ErrFailed) {
		t.Fatalf("换密钥: %v", err)
	}
}
