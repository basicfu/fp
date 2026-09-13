package fpsdk

import (
	"encoding/json"
	"errors"
	"net/http"
)

// 访问密钥请求被拒的错误码。ACCESS_KEY_* 与 fp 服务端的码逐字相同。
const (
	CodeSignatureInvalid  = "SIGNATURE_INVALID"
	CodeTimestampExpired  = "TIMESTAMP_EXPIRED"
	CodeAccessKeyInvalid  = "ACCESS_KEY_INVALID"
	CodeAccessKeyDisabled = "ACCESS_KEY_DISABLED"
	CodeAccessKeyExpired  = "ACCESS_KEY_EXPIRED"
	CodeSignatureMismatch = "SIGNATURE_MISMATCH"
	CodeNonceUsed         = "NONCE_USED"
	CodeIPDenied          = "IP_DENIED"
	CodeBodyTooLarge      = "BODY_TOO_LARGE"
)

// AccessKeyError 是访问密钥请求被拒的原因。OnError 里可以用 errors.As 取出它自定义响应。
type AccessKeyError struct {
	Code string
	Msg  string
	// StringToSign 只在 SIGNATURE_MISMATCH 时非空：SDK 算出的待签名串，全部来自请求本身，不含 SK。
	StringToSign string

	sentinel error
}

func (e *AccessKeyError) Error() string { return e.Msg }
func (e *AccessKeyError) Unwrap() error { return e.sentinel }

func akErr(sentinel error, code, msg string) *AccessKeyError {
	return &AccessKeyError{Code: code, Msg: msg, sentinel: sentinel}
}

// writeAccessKeyError 写出错误码与固定文案。唯一回显的请求内容是待签名串，里面没有秘密。
func writeAccessKeyError(w http.ResponseWriter, e *AccessKeyError) {
	status := http.StatusUnauthorized
	switch {
	case errors.Is(e, ErrForbidden):
		status = http.StatusForbidden
	case errors.Is(e, ErrBodyTooLarge):
		status = http.StatusRequestEntityTooLarge
	}
	body := map[string]string{"code": e.Code, "msg": e.Msg}
	if e.StringToSign != "" {
		body["stringToSign"] = e.StringToSign
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
