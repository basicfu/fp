package wechatmp

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Server 是服务号「服务器配置」里的那组参数，用来校验与解开微信推来的事件。
type Server struct {
	AppID string
	Token string
	// EncodingAESKey 为空表示明文模式；填了就按安全模式解密（兼容模式也走密文那一份）。
	EncodingAESKey string
}

// Message 是推送里 fp 关心的字段。
type Message struct {
	// ToUserName 是服务号的原始 ID（gh_ 开头），回复时作为发送方。
	ToUserName string
	// FromUserName 是用户在本服务号下的 openid。
	FromUserName string
	CreateTime   int64
	MsgType      string
	Event        string
	EventKey     string

	encrypted bool
}

// 推送里的事件类型。
const (
	EventSubscribe   = "subscribe"
	EventUnsubscribe = "unsubscribe"
	EventScan        = "SCAN"
)

// ErrBadSignature 是签名对不上：不是微信推来的，或 Token 配错了。
var ErrBadSignature = errors.New("wechat_mp: 签名校验失败")

// VerifyURL 处理配置服务器地址时微信发来的 GET 校验，签名正确时返回要原样回写的 echostr。
func (s Server) VerifyURL(q url.Values) (string, error) {
	if !s.checkSignature(q.Get("signature"), q.Get("timestamp"), q.Get("nonce")) {
		return "", ErrBadSignature
	}
	return q.Get("echostr"), nil
}

type envelope struct {
	ToUserName   string `xml:"ToUserName"`
	FromUserName string `xml:"FromUserName"`
	CreateTime   int64  `xml:"CreateTime"`
	MsgType      string `xml:"MsgType"`
	Event        string `xml:"Event"`
	EventKey     string `xml:"EventKey"`
	Encrypt      string `xml:"Encrypt"`
}

// Open 校验并解开一次 POST 推送。q 是请求的 query，body 是原始 XML。
//
// 安全模式下只认密文的 msg_signature：明文的 signature 不覆盖消息体，攻击者拿一个旧推送的签名就能配任意内容。
func (s Server) Open(q url.Values, body []byte) (*Message, error) {
	var env envelope
	if err := xml.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("wechat_mp: 推送不是合法 XML: %w", err)
	}
	if s.EncodingAESKey == "" {
		if !s.checkSignature(q.Get("signature"), q.Get("timestamp"), q.Get("nonce")) {
			return nil, ErrBadSignature
		}
		return env.message(false), nil
	}
	if env.Encrypt == "" {
		return nil, errors.New("wechat_mp: 已配置 EncodingAESKey，推送却没有密文（公众平台的消息加解密方式要选安全模式）")
	}
	if !s.checkSignature(q.Get("msg_signature"), q.Get("timestamp"), q.Get("nonce"), env.Encrypt) {
		return nil, ErrBadSignature
	}
	plain, err := s.decrypt(env.Encrypt)
	if err != nil {
		return nil, err
	}
	var inner envelope
	if err := xml.Unmarshal(plain, &inner); err != nil {
		return nil, fmt.Errorf("wechat_mp: 解密后的推送不是合法 XML: %w", err)
	}
	return inner.message(true), nil
}

func (e envelope) message(encrypted bool) *Message {
	return &Message{
		ToUserName: e.ToUserName, FromUserName: e.FromUserName, CreateTime: e.CreateTime,
		MsgType: e.MsgType, Event: e.Event, EventKey: e.EventKey, encrypted: encrypted,
	}
}

// ReplyText 生成给 m 发送者的文字被动回复；m 是密文推来的就加密回复。
func (s Server) ReplyText(m *Message, content string) ([]byte, error) {
	now := time.Now().Unix()
	plain := fmt.Sprintf("<xml><ToUserName>%s</ToUserName><FromUserName>%s</FromUserName><CreateTime>%d</CreateTime>"+
		"<MsgType><![CDATA[text]]></MsgType><Content>%s</Content></xml>",
		cdata(m.FromUserName), cdata(m.ToUserName), now, cdata(content))
	if !m.encrypted {
		return []byte(plain), nil
	}
	enc, err := s.encrypt([]byte(plain))
	if err != nil {
		return nil, err
	}
	ts := strconv.FormatInt(now, 10)
	nonce, err := randomHex(8)
	if err != nil {
		return nil, err
	}
	sig := signature(s.Token, ts, nonce, enc)
	return []byte(fmt.Sprintf("<xml><Encrypt>%s</Encrypt><MsgSignature>%s</MsgSignature><TimeStamp>%s</TimeStamp><Nonce>%s</Nonce></xml>",
		cdata(enc), cdata(sig), ts, cdata(nonce))), nil
}

// cdata 包一段 CDATA。内容里的 "]]>" 要拆成两段，否则会提前结束。
func cdata(s string) string {
	return "<![CDATA[" + strings.ReplaceAll(s, "]]>", "]]]]><![CDATA[>") + "]]>"
}

func (s Server) checkSignature(got string, parts ...string) bool {
	if got == "" || s.Token == "" {
		return false
	}
	want := signature(append([]string{s.Token}, parts...)...)
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// signature 是微信的签名算法：参数按字典序排序后拼接，取 SHA1 的十六进制。
func signature(parts ...string) string {
	p := append([]string(nil), parts...)
	sort.Strings(p)
	sum := sha1.Sum([]byte(strings.Join(p, "")))
	return hex.EncodeToString(sum[:])
}

func (s Server) aesKey() ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(s.EncodingAESKey + "=")
	if err != nil || len(key) != 32 {
		return nil, errors.New("wechat_mp: EncodingAESKey 不是 43 位的合法取值")
	}
	return key, nil
}

// 微信的 PKCS#7 按 32 字节分块补位，不是 AES 的 16。
const padBlock = 32

// decrypt 解开 Encrypt：AES-256-CBC，IV 取密钥前 16 字节；明文是 16 字节随机串 + 4 字节大端长度 + 消息 + AppID。
func (s Server) decrypt(b64 string) ([]byte, error) {
	key, err := s.aesKey()
	if err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("wechat_mp: 密文格式不对")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, key[:aes.BlockSize]).CryptBlocks(plain, data)
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > padBlock || pad > len(plain) {
		return nil, errors.New("wechat_mp: 解密后补位不对，EncodingAESKey 可能配错了")
	}
	plain = plain[:len(plain)-pad]
	if len(plain) < 20 {
		return nil, errors.New("wechat_mp: 解密后长度不对")
	}
	n := int(binary.BigEndian.Uint32(plain[16:20]))
	if n < 0 || 20+n > len(plain) {
		return nil, errors.New("wechat_mp: 解密后长度不对")
	}
	msg, appID := plain[20:20+n], string(plain[20+n:])
	// AppID 对不上说明是别的服务号的推送，或者配错了服务号。
	if s.AppID != "" && appID != s.AppID {
		return nil, errors.New("wechat_mp: 推送不属于本服务号")
	}
	return msg, nil
}

func (s Server) encrypt(msg []byte) (string, error) {
	key, err := s.aesKey()
	if err != nil {
		return "", err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	buf.Write(random)
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(msg)))
	buf.Write(msg)
	buf.WriteString(s.AppID)
	pad := padBlock - buf.Len()%padBlock
	buf.Write(bytes.Repeat([]byte{byte(pad)}, pad))
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	out := make([]byte, buf.Len())
	cipher.NewCBCEncrypter(block, key[:aes.BlockSize]).CryptBlocks(out, buf.Bytes())
	return base64.StdEncoding.EncodeToString(out), nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
