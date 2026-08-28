package fpsdk

import (
	"strings"
	"testing"
	"time"
)

func TestOptionsRequireEssentials(t *testing.T) {
	cases := map[string]Options{
		"缺地址":      {AppID: "a", AppSecret: "s"},
		"缺 appId":  {Addr: "x:9090", AppSecret: "s"},
		"缺 secret": {Addr: "x:9090", AppID: "a"},
	}
	for name, o := range cases {
		if err := o.validate(); err == nil {
			t.Errorf("%s：validate 没有报错", name)
		}
	}
}

func TestOptionsFillDefaults(t *testing.T) {
	o := Options{Addr: "x:9090", AppID: "a", AppSecret: "s"}
	o.applyDefaults()

	if o.ValidateTimeout <= 0 {
		t.Errorf("ValidateTimeout 默认值为 %v", o.ValidateTimeout)
	}
	if o.Logger == nil {
		t.Error("Logger 默认值为 nil，SDK 内部日志会 panic")
	}
}

// TestInsecureRequiresExplicitOptIn 钉住传输安全的默认值。
//
// appSecret 随每个 RPC 的 metadata 发送。明文传输等于把它印在网线上，
// 任何能抓包的人都能拿到一个可以签发任意用户会话的凭据。
// 所以默认必须要求 TLS，明文只能显式 opt-in。
func TestInsecureRequiresExplicitOptIn(t *testing.T) {
	var o Options
	o.applyDefaults()
	if o.Insecure {
		t.Fatal("默认允许明文——appSecret 会在网络上裸奔")
	}

	creds := newAppCredentials(o)
	if !creds.RequireTransportSecurity() {
		t.Fatal("默认凭据不要求传输层安全")
	}
	insecureCreds := newAppCredentials(Options{Insecure: true})
	if insecureCreds.RequireTransportSecurity() {
		t.Fatal("Insecure=true 时仍要求传输层安全，本地开发无法连接")
	}
}

func TestValidateRejectsNegativeDurations(t *testing.T) {
	o := Options{Addr: "x:9090", AppID: "a", AppSecret: "s", ValidateTimeout: -time.Second}
	err := o.validate()
	if err == nil || !strings.Contains(err.Error(), "ValidateTimeout") {
		t.Fatalf("负的 ValidateTimeout 未被拒绝: %v", err)
	}
}
