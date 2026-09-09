package fpsdk

import (
	"context"
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
	if o.CacheSize != defaultCacheSize {
		t.Errorf("CacheSize 默认值为 %d，期望 %d", o.CacheSize, defaultCacheSize)
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

// TestValidateRejectsNegativeCacheSize 确认 CacheSize < 0 这一分支真的被
// validate 拒绝，而不只是声明了却没人调用到。
func TestValidateRejectsNegativeCacheSize(t *testing.T) {
	o := Options{Addr: "x:9090", AppID: "a", AppSecret: "s", CacheSize: -1}
	err := o.validate()
	if err == nil || !strings.Contains(err.Error(), "CacheSize") {
		t.Fatalf("负的 CacheSize 未被拒绝: %v", err)
	}
}

// TestValidateRejectsNegativeDegradedCacheTTL 确认 DegradedCacheTTL < 0 这一
// 分支真的被 validate 拒绝，而不只是声明了却没人调用到。
func TestValidateRejectsNegativeDegradedCacheTTL(t *testing.T) {
	o := Options{Addr: "x:9090", AppID: "a", AppSecret: "s", DegradedCacheTTL: -time.Second}
	err := o.validate()
	if err == nil || !strings.Contains(err.Error(), "DegradedCacheTTL") {
		t.Fatalf("负的 DegradedCacheTTL 未被拒绝: %v", err)
	}
}

// TestValidateRejectsNegativeMaxStaleness 确认 MaxStaleness < 0 这一分支
// 真的被 validate 拒绝，而不只是声明了却没人调用到。
func TestValidateRejectsNegativeMaxStaleness(t *testing.T) {
	o := Options{Addr: "x:9090", AppID: "a", AppSecret: "s", MaxStaleness: -time.Second}
	err := o.validate()
	if err == nil || !strings.Contains(err.Error(), "MaxStaleness") {
		t.Fatalf("负的 MaxStaleness 未被拒绝: %v", err)
	}
}

// TestStaleFallbackIsOffByDefault 钉住降级方向的默认值。
//
// 字段命名为"允许用陈旧数据"而非它的反面，是为了让放宽的那个方向必须被
// 显式写出来——没人会主动去关掉一项他不知道存在的开关，零值必须落在
// 安全的一侧。
func TestStaleFallbackIsOffByDefault(t *testing.T) {
	var o Options
	o.applyDefaults()
	if o.AllowStaleOnOutage {
		t.Fatal("默认允许使用陈旧缓存——fp 一挂，已被撤销的会话会继续通行")
	}
	if o.MaxStaleness <= 0 {
		t.Fatalf("MaxStaleness 默认值为 %v——陈旧兜底必须有上限，"+
			"否则 fp 长时间不可用时会无限延用", o.MaxStaleness)
	}
	if o.DegradedCacheTTL <= 0 {
		t.Fatalf("DegradedCacheTTL 默认值为 %v", o.DegradedCacheTTL)
	}
}

// TestCallerTypeIMAllowsEmptyAppID：fp-im 的连接没有固定 app——它的 appId
// 来自每个 client 的 ws 握手帧，逐调用附上（见 WithAppID）。
func TestCallerTypeIMAllowsEmptyAppID(t *testing.T) {
	o := Options{Addr: "x:9090", AppSecret: "s", CallerType: CallerTypeIM}
	if err := o.validate(); err != nil {
		t.Fatalf("CallerType=im 时应允许空 AppID：%v", err)
	}
}

// TestDefaultCallerTypeStillRequiresAppID：这条不能因为新增字段而放松。
func TestDefaultCallerTypeStillRequiresAppID(t *testing.T) {
	o := Options{Addr: "x:9090", AppSecret: "s"}
	if err := o.validate(); err == nil {
		t.Fatal("默认调用方仍然必须填 AppID")
	}
}

func TestUnknownCallerTypeRejected(t *testing.T) {
	o := Options{Addr: "x:9090", AppID: "a", AppSecret: "s", CallerType: "gateway"}
	if err := o.validate(); err == nil {
		t.Fatal("未知的 CallerType 必须报错")
	}
}

// TestIMCredentialsOmitAppID：im 凭据不输出 fp-app-id，为逐调用附上的那个
// 让路。两个都出的话 metadata 里会有两个值，服务端 first() 取哪个是未定义
// 行为。
func TestIMCredentialsOmitAppID(t *testing.T) {
	c := newAppCredentials(Options{AppID: "ignored", AppSecret: "s", CallerType: CallerTypeIM, Insecure: true})
	md, err := c.GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := md["fp-app-id"]; ok {
		t.Fatal("im 凭据不能输出 fp-app-id")
	}
	if md["fp-caller-type"] != CallerTypeIM {
		t.Fatalf("fp-caller-type = %q, want %q", md["fp-caller-type"], CallerTypeIM)
	}
	if md["fp-app-secret"] != "s" {
		t.Fatalf("fp-app-secret = %q", md["fp-app-secret"])
	}
}

// TestDefaultCredentialsUnchanged 钉住"已接入的 SDK 一个字节都不用改"。
func TestDefaultCredentialsUnchanged(t *testing.T) {
	c := newAppCredentials(Options{AppID: "app1", AppSecret: "s", Insecure: true})
	md, err := c.GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if md["fp-app-id"] != "app1" || md["fp-app-secret"] != "s" {
		t.Fatalf("既有凭据形状变了：%v", md)
	}
	if _, ok := md["fp-caller-type"]; ok {
		t.Fatal("默认调用方不该出现 fp-caller-type")
	}
}
