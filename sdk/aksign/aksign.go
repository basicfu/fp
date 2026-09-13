// Package aksign 是 fp 访问密钥签名规则的唯一实现：第三方用它签名，fpsdk 用它验签。
//
// 只依赖标准库：用 Go 的第三方 import 它，不会被带进 gRPC 等依赖。
package aksign

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	HeaderAccessKey = "X-Fp-Access-Key"
	HeaderTimestamp = "X-Fp-Timestamp"
	HeaderNonce     = "X-Fp-Nonce"
	HeaderSignature = "X-Fp-Signature"
)

// StringToSign 拼出 6 行待签名串。rawPath、rawQuery 必须是请求行里的原样字节。
func StringToSign(method, rawPath, rawQuery string, timestamp int64, nonce string, body []byte) string {
	sum := sha256.Sum256(body)
	return method + "\n" + rawPath + "\n" + rawQuery + "\n" +
		strconv.FormatInt(timestamp, 10) + "\n" + nonce + "\n" + hex.EncodeToString(sum[:])
}

// Signature 返回小写十六进制的 HMAC-SHA256。密钥是 secret 字符串本身的字节，不做 base64 解码。
func Signature(secret, stringToSign string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(stringToSign))
	return hex.EncodeToString(m.Sum(nil))
}

// SplitRequestURI 把 request-target 拆成原样 path 与 query。绝对形式只取 path 部分。
func SplitRequestURI(requestURI string) (rawPath, rawQuery string) {
	// origin-form 总是以 "/" 开头；只有不是 origin-form 时才处理绝对形式。
	// 这样可以避免被 query 参数里的 "://" (比如跳转 URL) 误判。
	if !strings.HasPrefix(requestURI, "/") {
		if i := strings.Index(requestURI, "://"); i >= 0 {
			rest := requestURI[i+3:]
			j := strings.IndexByte(rest, '/')
			if j < 0 {
				return "/", ""
			}
			requestURI = rest[j:]
		}
	}
	rawPath, rawQuery, _ = strings.Cut(requestURI, "?")
	return rawPath, rawQuery
}

// ValidNonce 报告 nonce 是否符合 [A-Za-z0-9_-]{1,64}。
func ValidNonce(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// Sign 用当前时间和随机 nonce 给即将发出的请求加上 4 个签名头。
func Sign(req *http.Request, accessKeyID, secret string) error {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	return SignAt(req, accessKeyID, secret, time.Now().Unix(), hex.EncodeToString(b[:]))
}

// SignAt 与 Sign 相同，但时间戳和 nonce 由调用方给出。会读完 req.Body 并放回。
func SignAt(req *http.Request, accessKeyID, secret string, timestamp int64, nonce string) error {
	var body []byte
	if req.Body != nil && req.Body != http.NoBody {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return err
		}
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(b))
		req.ContentLength = int64(len(b))
		body = b
	}
	// 客户端发出的请求行就是 URL.RequestURI()，签的必须是这串字节。
	path, query := SplitRequestURI(req.URL.RequestURI())
	req.Header.Set(HeaderAccessKey, accessKeyID)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(timestamp, 10))
	req.Header.Set(HeaderNonce, nonce)
	req.Header.Set(HeaderSignature, Signature(secret, StringToSign(req.Method, path, query, timestamp, nonce, body)))
	return nil
}
