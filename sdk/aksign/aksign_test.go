package aksign

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

const vecSecret = "ZmFrZS1zZWNyZXQtZm9yLWRvY3MtZXhhbXBsZS0wMDE"

// vectors 同时写进 docs/access-key.md，Task 14 的测试核对两边一致。
var vectors = []struct {
	name, method, path, query, nonce, body, sts, sig string
}{
	{"POST 带 query 与 body", "POST", "/api/v1/orders", "source=partner",
		"3f2b8c1e-7d4a-4e1b-9c55-2a6f0d8e9b17", `{"sku":"A100","qty":2}`,
		"POST\n/api/v1/orders\nsource=partner\n1789200000\n3f2b8c1e-7d4a-4e1b-9c55-2a6f0d8e9b17\neac3608933ecc3a8767f2d0966e808202214fa80644296ed3b7a0286dae3c383",
		"a3d533065c49e9a9888c0f6e8c1b41c9d70316891411e001ec73ef12f464f3c4"},
	{"GET 编码斜杠、无 query、无 body", "GET", "/api/v1/orders/a%2Fb", "", "n-0001", "",
		"GET\n/api/v1/orders/a%2Fb\n\n1789200000\nn-0001\ne3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"46b32c81d96b8e44e56e152af7bb2e6a6485968fc9bac34df180b2fa299ba8f8"},
}

func TestVectors(t *testing.T) {
	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			sts := StringToSign(v.method, v.path, v.query, 1789200000, v.nonce, []byte(v.body))
			if sts != v.sts {
				t.Fatalf("StringToSign = %q, want %q", sts, v.sts)
			}
			if got := Signature(vecSecret, sts); got != v.sig {
				t.Fatalf("Signature = %s, want %s", got, v.sig)
			}
		})
	}
}

// 【辨别力】六个字段逐个改一处，签名都必须变。
func TestEveryFieldParticipates(t *testing.T) {
	base := func() (string, string, string, int64, string, []byte) {
		return "POST", "/a", "x=1", 100, "n1", []byte("b")
	}
	m, p, q, ts, n, b := base()
	want := Signature("s", StringToSign(m, p, q, ts, n, b))
	mutations := map[string]func(){
		"method": func() { m = "PUT" }, "path": func() { p = "/b" }, "query": func() { q = "x=2" },
		"timestamp": func() { ts = 101 }, "nonce": func() { n = "n2" }, "body": func() { b = []byte("c") },
	}
	for name, mutate := range mutations {
		m, p, q, ts, n, b = base()
		mutate()
		if Signature("s", StringToSign(m, p, q, ts, n, b)) == want {
			t.Errorf("改了 %s，签名却没变", name)
		}
	}
}

func TestSignAtMatchesVectorAndKeepsBody(t *testing.T) {
	v := vectors[0]
	req, _ := http.NewRequest(v.method, "http://biz.example"+v.path+"?"+v.query, strings.NewReader(v.body))
	if err := SignAt(req, "FPAK7Q2M9X4K1D8R3T6W", vecSecret, 1789200000, v.nonce); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get(HeaderSignature) != v.sig || req.Header.Get(HeaderTimestamp) != "1789200000" {
		t.Fatalf("签名头不对: %v", req.Header)
	}
	if b, _ := io.ReadAll(req.Body); string(b) != v.body {
		t.Fatalf("签名后 body 被读空了: %q", b)
	}
}

func TestSplitRequestURI(t *testing.T) {
	cases := []struct{ in, path, query string }{
		{"/a?b=1&c=2", "/a", "b=1&c=2"},
		{"/a", "/a", ""},
		{"/a%2Fb?x", "/a%2Fb", "x"},
		{"http://h:8080/a?x=1", "/a", "x=1"},
		{"http://h", "/", ""},
		{"/redirect?url=http://evil.com/steal", "/redirect", "url=http://evil.com/steal"},
	}
	for _, c := range cases {
		if p, q := SplitRequestURI(c.in); p != c.path || q != c.query {
			t.Errorf("SplitRequestURI(%q) = (%q, %q), want (%q, %q)", c.in, p, q, c.path, c.query)
		}
	}
}

func TestValidNonce(t *testing.T) {
	for _, ok := range []string{"a", "A-z_0", strings.Repeat("x", 64)} {
		if !ValidNonce(ok) {
			t.Errorf("%q 应当合法", ok)
		}
	}
	for _, bad := range []string{"", strings.Repeat("x", 65), "a b", "a\nb", "中"} {
		if ValidNonce(bad) {
			t.Errorf("%q 应当不合法", bad)
		}
	}
}
