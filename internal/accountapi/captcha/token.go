// Package captcha 是短信验证码前的滑块验证:题库(Pool)、无状态票据(Sealer)、轨迹规则与出题/校验服务。
package captcha

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"
)

var (
	ErrFailed  = errors.New("captcha: 验证失败")
	ErrExpired = errors.New("captcha: 验证已过期")
)

// Sealer 把答案加密进 captcha_id:出题不写库,任何实例都能校验(各实例从 Redis 取同一份密钥,见 SharedSecret)。
type Sealer struct{ aead cipher.AEAD }

func NewSealer(secret string) (*Sealer, error) {
	key := sha256.Sum256([]byte("account-captcha-v1" + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// 明文: x uint16 | y uint16 | exp unix 秒 int64
func (s *Sealer) Seal(x, y int, exp time.Time) string {
	var plain [12]byte
	binary.BigEndian.PutUint16(plain[0:], uint16(x))
	binary.BigEndian.PutUint16(plain[2:], uint16(y))
	binary.BigEndian.PutUint64(plain[4:], uint64(exp.Unix()))
	nonce := make([]byte, s.aead.NonceSize(), s.aead.NonceSize()+len(plain)+s.aead.Overhead())
	_, _ = rand.Read(nonce)
	return base64.RawURLEncoding.EncodeToString(s.aead.Seal(nonce, nonce, plain[:], nil))
}

func (s *Sealer) Open(id string, now time.Time) (x, y int, exp time.Time, err error) {
	// Strict 拒绝末尾填充位非零的变体:否则同一票据有多个 id 字符串,按 id 做的防重放会被绕过。
	raw, err := base64.RawURLEncoding.Strict().DecodeString(id)
	ns := s.aead.NonceSize()
	if err != nil || len(raw) < ns {
		return 0, 0, time.Time{}, ErrFailed
	}
	plain, err := s.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil || len(plain) != 12 {
		return 0, 0, time.Time{}, ErrFailed
	}
	exp = time.Unix(int64(binary.BigEndian.Uint64(plain[4:])), 0)
	if now.After(exp) {
		return 0, 0, exp, ErrExpired
	}
	return int(binary.BigEndian.Uint16(plain[0:])), int(binary.BigEndian.Uint16(plain[2:])), exp, nil
}
