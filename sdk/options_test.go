package fpsdk

import (
	"context"
	"crypto/tls"
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

// TestRequireTransportSecurityIsAlwaysFalse 钉住 appCredentials 不阻止在
// 明文 gRPC 连接上发送凭据——明文还是 TLS 由 Options.TLS 决定，这里不做
// 二次把关。
func TestRequireTransportSecurityIsAlwaysFalse(t *testing.T) {
	creds := newAppCredentials(Options{})
	if creds.RequireTransportSecurity() {
		t.Fatal("RequireTransportSecurity 应恒为 false")
	}
}

// TestTransportCredentialsNilMeansPlaintext 钉住 Options.TLS 零值（nil）
// 时走明文——这是兼容既有调用方（fp-im 连 fp、examples 下的示例都不设
// 这个字段）的关键：加了新字段不能让老代码的连接方式变了。
func TestTransportCredentialsNilMeansPlaintext(t *testing.T) {
	creds := transportCredentials(nil)
	if creds.Info().SecurityProtocol != "insecure" {
		t.Fatalf("SecurityProtocol = %q, want %q", creds.Info().SecurityProtocol, "insecure")
	}
}

// TestTransportCredentialsNonNilMeansTLS 钉住 Options.TLS 非 nil 时走
// TLS——不校验、不改写调用方给的 *tls.Config，原样交给
// credentials.NewTLS。
func TestTransportCredentialsNonNilMeansTLS(t *testing.T) {
	creds := transportCredentials(&tls.Config{ServerName: "fp.example.com"})
	if creds.Info().SecurityProtocol != "tls" {
		t.Fatalf("SecurityProtocol = %q, want %q", creds.Info().SecurityProtocol, "tls")
	}
}

// TestResolveAddrBareHostPortUnchanged 钉住兼容性的核心：没有 "://" 的
// 裸地址原样透传，tlsConfig 也原样透传——这是加了 scheme 解析之后，既有
// 调用方（不带 scheme）行为一个字节都不能变的保证。
func TestResolveAddrBareHostPortUnchanged(t *testing.T) {
	addr, tlsConfig, err := resolveAddr("fp.internal:9090", nil)
	if err != nil {
		t.Fatalf("resolveAddr() error = %v", err)
	}
	if addr != "fp.internal:9090" {
		t.Errorf("addr = %q，期望原样透传", addr)
	}
	if tlsConfig != nil {
		t.Errorf("tlsConfig = %v，期望仍是 nil", tlsConfig)
	}
}

// TestResolveAddrHTTPSDefaultsTLSConfig 钉住 https:// 且没显式给
// tlsConfig 时，自动补一个默认的 &tls.Config{}，而不是继续 nil（那样会
// 变回明文，跟 scheme 说的自相矛盾）。
func TestResolveAddrHTTPSDefaultsTLSConfig(t *testing.T) {
	addr, tlsConfig, err := resolveAddr("https://fp.xxzj.com:443", nil)
	if err != nil {
		t.Fatalf("resolveAddr() error = %v", err)
	}
	if addr != "fp.xxzj.com:443" {
		t.Errorf("addr = %q，期望 scheme 被剥掉只剩 host:port", addr)
	}
	if tlsConfig == nil {
		t.Fatal("tlsConfig 不该是 nil——https:// 必须走 TLS")
	}
}

// TestResolveAddrHTTPSKeepsExplicitTLSConfig 钉住 https:// 且调用方已经
// 显式给了 tlsConfig 时，用调用方给的那份，不被默认值覆盖掉。
func TestResolveAddrHTTPSKeepsExplicitTLSConfig(t *testing.T) {
	given := &tls.Config{ServerName: "custom.example.com"}
	_, tlsConfig, err := resolveAddr("https://fp.xxzj.com:443", given)
	if err != nil {
		t.Fatalf("resolveAddr() error = %v", err)
	}
	if tlsConfig != given {
		t.Fatal("应当原样使用调用方显式给的 tlsConfig，不该被替换成默认值")
	}
}

// TestResolveAddrHTTPStripsScheme 钉住 http:// 且没有 tlsConfig 时走明文，
// 且 scheme 被剥掉。
func TestResolveAddrHTTPStripsScheme(t *testing.T) {
	addr, tlsConfig, err := resolveAddr("http://fp.internal:9090", nil)
	if err != nil {
		t.Fatalf("resolveAddr() error = %v", err)
	}
	if addr != "fp.internal:9090" {
		t.Errorf("addr = %q", addr)
	}
	if tlsConfig != nil {
		t.Errorf("tlsConfig = %v，期望 nil（明文）", tlsConfig)
	}
}

// TestResolveAddrHTTPWithTLSConfigIsContradiction 钉住 http:// 却又显式
// 给了 tlsConfig 这种自相矛盾的输入必须报错，不能悄悄选其中一个。
func TestResolveAddrHTTPWithTLSConfigIsContradiction(t *testing.T) {
	_, _, err := resolveAddr("http://fp.internal:9090", &tls.Config{})
	if err == nil {
		t.Fatal("http:// 又带 tlsConfig 是自相矛盾的输入，必须报错")
	}
}

// TestResolveAddrRejectsUnknownScheme 钉住除了 http/https 之外的 scheme
// （比如 gRPC 自己的 dns://、unix:// 这些 target scheme，含义完全不同）
// 一律报错，不静默当成明文处理。
func TestResolveAddrRejectsUnknownScheme(t *testing.T) {
	_, _, err := resolveAddr("dns:///fp.internal:9090", nil)
	if err == nil {
		t.Fatal("不认识的 scheme 必须报错")
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
	c := newAppCredentials(Options{AppID: "ignored", AppSecret: "s", CallerType: CallerTypeIM})
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
	c := newAppCredentials(Options{AppID: "app1", AppSecret: "s"})
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
