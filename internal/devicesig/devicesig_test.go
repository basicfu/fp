package devicesig

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

// vectors.json 由 kkdesk server 的 crypto.DeviceAuthSigningInput / VerifyStr 生成（固定密钥、固定 ts），
// 签名内容与令牌编码必须与它逐字节一致，老客户端才不用改。
type vector struct {
	Name            string `json:"name"`
	Header          string `json:"header"`
	Now             int64  `json:"now"`
	Want            string `json:"want"`
	SignPubHex      string `json:"sign_pub_hex"`
	SigningInputHex string `json:"signing_input_hex"`
}

func loadVectors(t *testing.T) []vector {
	t.Helper()
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vs []vector
	if err := json.Unmarshal(raw, &vs); err != nil {
		t.Fatal(err)
	}
	if len(vs) != 3 {
		t.Fatalf("向量 %d 组，期望 3", len(vs))
	}
	return vs
}

func TestVectorsMatchKKdesk(t *testing.T) {
	for _, v := range loadVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			tok, err := Parse(v.Header)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if hex.EncodeToString(tok.SignPub) != v.SignPubHex {
				t.Fatalf("sign_pub = %x", tok.SignPub)
			}
			if got := hex.EncodeToString(SigningInput(tok)); got != v.SigningInputHex {
				t.Fatalf("签名内容与 kkdesk 不一致:\n got %s\nwant %s", got, v.SigningInputHex)
			}
			err = Verify(tok, time.Unix(v.Now, 0), 300*time.Second)
			switch v.Want {
			case "ok":
				if err != nil {
					t.Fatalf("Verify: %v", err)
				}
				if tok.OS != "windows" || tok.Ver != "1.2.3" || tok.PCName != "办公室电脑" {
					t.Fatalf("不签名的元信息没解析出来: %+v", tok)
				}
			case "bad_signature":
				if !errors.Is(err, ErrBadSignature) {
					t.Fatalf("Verify = %v，期望 ErrBadSignature", err)
				}
			}
		})
	}
}

func signed(t *testing.T, ts int64) Token {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	tok := Token{SignPub: pub, HS: []byte("hs"), TS: ts, Nonce: "n1"}
	tok.Sig = ed25519.Sign(priv, SigningInput(tok))
	return tok
}

func TestVerifyClockSkew(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for _, d := range []int64{301, -301, 3600} {
		tok := signed(t, now.Unix()+d)
		if err := Verify(tok, now, 300*time.Second); !errors.Is(err, ErrClockSkew) {
			t.Fatalf("偏 %d 秒: Verify = %v，期望 ErrClockSkew", d, err)
		}
	}
	for _, d := range []int64{300, -300, 0} {
		if err := Verify(signed(t, now.Unix()+d), now, 300*time.Second); err != nil {
			t.Fatalf("偏 %d 秒在窗内: %v", d, err)
		}
	}
}

func TestVerifyChecksClockBeforeSignature(t *testing.T) {
	// 钟偏的机器拿到 clock_skew 才知道去校时，而不是怀疑身份。
	tok := signed(t, 0)
	tok.Sig[0] ^= 0xFF
	if err := Verify(tok, time.Unix(1_800_000_000, 0), 300*time.Second); !errors.Is(err, ErrClockSkew) {
		t.Fatalf("Verify = %v，期望先报 ErrClockSkew", err)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	for name, h := range map[string]string{
		"空":            "",
		"不是 base64":    "%%%",
		"不是 JSON":      base64.RawURLEncoding.EncodeToString([]byte("not json")),
		"字节字段非 base64": base64.RawURLEncoding.EncodeToString([]byte(`{"sign_pub":"!!"}`)),
	} {
		if _, err := Parse(h); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%s: Parse = %v，期望 ErrMalformed", name, err)
		}
	}
}

func TestParseAcceptsPaddedBase64(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"sign_pub": []byte{1, 2}, "ts": 5, "nonce": "x"})
	tok, err := Parse(base64.URLEncoding.EncodeToString(raw))
	if err != nil || tok.TS != 5 || tok.Nonce != "x" {
		t.Fatalf("带填充的 base64url: tok=%+v err=%v", tok, err)
	}
}

func TestVerifyBadPublicKeyLength(t *testing.T) {
	tok := signed(t, 100)
	tok.SignPub = tok.SignPub[:10]
	if err := Verify(tok, time.Unix(100, 0), time.Minute); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Verify = %v", err)
	}
}
