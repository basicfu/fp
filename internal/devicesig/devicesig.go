// Package devicesig 解析并校验终端设备随请求带的设备头。
//
// 格式与签名内容照搬 kkdesk server（api/imverify.go 的 decodeDeviceAuthToken、crypto.DeviceAuthSigningInput），
// 逐字节一致：已发出去的客户端不用改。testdata/vectors.json 是用 kkdesk 代码生成的对照向量。
package devicesig

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrMalformed 设备头不是合法的 base64url(JSON)。
	ErrMalformed = errors.New("devicesig: 设备头格式不正确")
	// ErrClockSkew 设备时间超出允许的偏差。客户端据此按服务端时间校正后重签。
	ErrClockSkew = errors.New("devicesig: 设备时间偏差过大")
	// ErrBadSignature 签名不对。
	ErrBadSignature = errors.New("devicesig: 签名校验失败")
)

// Token 是解析后的设备头。JSON 字段名与 kkdesk 的 deviceAuthToken 相同；字节字段在 JSON 里是标准 base64。
type Token struct {
	SignPub []byte `json:"sign_pub"`
	HS      []byte `json:"hs"`
	// Sig 对应 JSON 的 str。
	Sig []byte `json:"str"`
	// TS 是 Unix 秒。
	TS    int64  `json:"ts"`
	Nonce string `json:"nonce"`
	// OS、Ver、PCName 是设备自报的元信息，不在签名范围内，只能用于展示。
	OS     string `json:"os"`
	Ver    string `json:"ver"`
	PCName string `json:"pc_name"`
}

// Parse 解析设备头：base64url 编码的 JSON，先按无填充解，失败再按带填充解（与 kkdesk 相同）。
func Parse(header string) (Token, error) {
	if header == "" {
		return Token{}, ErrMalformed
	}
	raw, err := base64.RawURLEncoding.DecodeString(header)
	if err != nil {
		if raw, err = base64.URLEncoding.DecodeString(header); err != nil {
			return Token{}, fmt.Errorf("%w: %v", ErrMalformed, err)
		}
	}
	var t Token
	if err := json.Unmarshal(raw, &t); err != nil {
		return Token{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return t, nil
}

// SigningInput 拼待签字节：sign_pub、hs、ts（8 字节大端）、nonce 依次各带 4 字节大端长度前缀。
func SigningInput(t Token) []byte {
	var b bytes.Buffer
	putField(&b, t.SignPub)
	putField(&b, t.HS)
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(t.TS))
	putField(&b, ts[:])
	putField(&b, []byte(t.Nonce))
	return b.Bytes()
}

func putField(b *bytes.Buffer, p []byte) {
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(p)))
	b.Write(l[:])
	b.Write(p)
}

// Verify 先查时间窗再验签：时间窗更便宜，且钟偏的机器拿到 ErrClockSkew 才知道去校时而不是怀疑身份。
func Verify(t Token, now time.Time, skew time.Duration) error {
	limit := int64(skew / time.Second)
	if d := now.Unix() - t.TS; d > limit || d < -limit {
		return ErrClockSkew
	}
	// ed25519.Verify 遇到长度不对的公钥会 panic，必须先挡掉。
	if len(t.SignPub) != ed25519.PublicKeySize {
		return ErrBadSignature
	}
	if !ed25519.Verify(ed25519.PublicKey(t.SignPub), SigningInput(t), t.Sig) {
		return ErrBadSignature
	}
	return nil
}
