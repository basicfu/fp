package notify

import (
	"testing"

	"github.com/alibabacloud-go/tea/tea"
)

// 阿里云客户端必须带着显式的连接/读取超时被创建出来。
//
// SDK 默认不设超时，而 Send 又拿不到 ctx（SendSms 没有 context 参数），
// 于是一个吊死的接入点会把 SendLoginCode 的 goroutine 永久挂住，Sender 的
// 降级逻辑也永远轮不到下一家。这条断言是"超时确实被设上了"的唯一防线——
// 网络调用本身测不了，但客户端的构造参数可以。
//
// 放在包内测试（package notify）而不是 notify_test：要读的是 AliyunSMS.client
// 这个私有字段，为了断言它而把 SDK 类型暴露到 fp 的公开 API 上并不划算。
func TestNewAliyunSMSSetsExplicitTimeouts(t *testing.T) {
	p, err := NewAliyunSMS(AliyunConfig{
		AccessKeyID:     "ak",
		AccessKeySecret: "sk",
		SignName:        "示例签名",
		Templates:       map[string]string{"login_code": "SMS_123456"},
	})
	if err != nil {
		t.Fatalf("NewAliyunSMS: %v", err)
	}
	if p.client.ConnectTimeout == nil || p.client.ReadTimeout == nil {
		t.Fatal("客户端没有设置超时——吊死的接入点会永久占住一个 goroutine")
	}
	if got := tea.IntValue(p.client.ConnectTimeout); got != aliyunConnectTimeoutMS {
		t.Fatalf("ConnectTimeout = %d, want %d", got, aliyunConnectTimeoutMS)
	}
	if got := tea.IntValue(p.client.ReadTimeout); got != aliyunReadTimeoutMS {
		t.Fatalf("ReadTimeout = %d, want %d", got, aliyunReadTimeoutMS)
	}
}
