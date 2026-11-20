package wechatmp

import (
	"encoding/xml"
	"errors"
	"net/url"
	"strings"
	"testing"
)

const (
	testToken = "tok123"
	// 43 位，官方文档示例里的取值。
	testAESKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
	testAppID  = "wx5823bf96d3bd56c7"
)

const eventXML = `<xml><ToUserName><![CDATA[gh_1]]></ToUserName><FromUserName><![CDATA[openid-1]]></FromUserName>` +
	`<CreateTime>1700000000</CreateTime><MsgType><![CDATA[event]]></MsgType><Event><![CDATA[subscribe]]></Event>` +
	`<EventKey><![CDATA[qrscene_fpsub:abc]]></EventKey></xml>`

func TestVerifyURL(t *testing.T) {
	s := Server{Token: testToken}
	q := url.Values{"timestamp": {"1"}, "nonce": {"n"}, "echostr": {"hello"}, "signature": {signature(testToken, "1", "n")}}
	echo, err := s.VerifyURL(q)
	if err != nil || echo != "hello" {
		t.Fatalf("echo = %q, err = %v", echo, err)
	}
	q.Set("signature", "bad")
	if _, err := s.VerifyURL(q); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenPlain(t *testing.T) {
	s := Server{Token: testToken}
	q := url.Values{"timestamp": {"1"}, "nonce": {"n"}, "signature": {signature(testToken, "1", "n")}}
	m, err := s.Open(q, []byte(eventXML))
	if err != nil {
		t.Fatal(err)
	}
	if m.FromUserName != "openid-1" || m.Event != EventSubscribe || m.EventKey != "qrscene_fpsub:abc" || m.CreateTime != 1700000000 {
		t.Fatalf("m = %+v", m)
	}
	q.Set("signature", "bad")
	if _, err := s.Open(q, []byte(eventXML)); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("err = %v", err)
	}
}

func encryptedPush(t *testing.T, s Server, inner string) (url.Values, []byte) {
	t.Helper()
	enc, err := s.encrypt([]byte(inner))
	if err != nil {
		t.Fatal(err)
	}
	q := url.Values{
		"timestamp": {"1"}, "nonce": {"n"}, "encrypt_type": {"aes"},
		"signature":     {signature(testToken, "1", "n")},
		"msg_signature": {signature(testToken, "1", "n", enc)},
	}
	return q, []byte("<xml><ToUserName><![CDATA[gh_1]]></ToUserName><Encrypt><![CDATA[" + enc + "]]></Encrypt></xml>")
}

func TestOpenEncryptedAndReply(t *testing.T) {
	s := Server{Token: testToken, EncodingAESKey: testAESKey, AppID: testAppID}
	q, body := encryptedPush(t, s, eventXML)
	m, err := s.Open(q, body)
	if err != nil {
		t.Fatal(err)
	}
	if m.FromUserName != "openid-1" || !m.encrypted {
		t.Fatalf("m = %+v", m)
	}

	reply, err := s.ReplyText(m, "已开启通知 ]]> 测试")
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Encrypt      string `xml:"Encrypt"`
		MsgSignature string `xml:"MsgSignature"`
		TimeStamp    string `xml:"TimeStamp"`
		Nonce        string `xml:"Nonce"`
	}
	if err := xml.Unmarshal(reply, &out); err != nil {
		t.Fatal(err)
	}
	if out.MsgSignature != signature(testToken, out.TimeStamp, out.Nonce, out.Encrypt) {
		t.Fatal("回复的签名对不上")
	}
	plain, err := s.decrypt(out.Encrypt)
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		ToUserName   string `xml:"ToUserName"`
		FromUserName string `xml:"FromUserName"`
		Content      string `xml:"Content"`
	}
	if err := xml.Unmarshal(plain, &r); err != nil {
		t.Fatal(err)
	}
	if r.ToUserName != "openid-1" || r.FromUserName != "gh_1" || r.Content != "已开启通知 ]]> 测试" {
		t.Fatalf("reply = %+v", r)
	}
}

// 安全模式下明文 signature 不覆盖消息体，只认 msg_signature。
func TestOpenEncryptedRejectsTamperedCiphertext(t *testing.T) {
	s := Server{Token: testToken, EncodingAESKey: testAESKey, AppID: testAppID}
	q, _ := encryptedPush(t, s, eventXML)
	_, other := encryptedPush(t, s, strings.Replace(eventXML, "openid-1", "attacker", 1))
	if _, err := s.Open(q, other); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenEncryptedRejectsOtherAppID(t *testing.T) {
	other := Server{Token: testToken, EncodingAESKey: testAESKey, AppID: "wx-other"}
	q, body := encryptedPush(t, other, eventXML)
	s := Server{Token: testToken, EncodingAESKey: testAESKey, AppID: testAppID}
	if _, err := s.Open(q, body); err == nil || !strings.Contains(err.Error(), "不属于本服务号") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenEncryptedRequiresCiphertext(t *testing.T) {
	s := Server{Token: testToken, EncodingAESKey: testAESKey, AppID: testAppID}
	q := url.Values{"timestamp": {"1"}, "nonce": {"n"}, "signature": {signature(testToken, "1", "n")}}
	if _, err := s.Open(q, []byte(eventXML)); err == nil {
		t.Fatal("安全模式下明文推送应当被拒")
	}
}

func TestPlainReply(t *testing.T) {
	s := Server{Token: testToken}
	r, err := s.ReplyText(&Message{ToUserName: "gh_1", FromUserName: "o1"}, "hi")
	if err != nil || !strings.Contains(string(r), "<ToUserName><![CDATA[o1]]></ToUserName>") {
		t.Fatalf("reply = %s, err = %v", r, err)
	}
}
