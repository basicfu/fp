package fpsdk

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// TestCacheTTLSurvives90Days 守住 cache_ttl_ms 的位宽。
//
// 90 天 = 7,776,000,000 毫秒，远超 int32 的上限 2,147,483,647。
// 谁把这个字段改成 int32，这里立刻失败——而线上的表现会隐蔽得多：
// 长会话的 cache_ttl 溢出成负数，SDK 要么永不缓存（回源量暴涨），
// 要么按负 TTL 算出一个已过期的条目，看起来"缓存没生效"却查不出原因。
func TestCacheTTLSurvives90Days(t *testing.T) {
	const ninetyDaysMs int64 = 90 * 24 * 60 * 60 * 1000

	raw, err := proto.Marshal(&fpv1.ValidateTokenResponse{CacheTtlMs: ninetyDaysMs})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out fpv1.ValidateTokenResponse
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := out.GetCacheTtlMs(); got != ninetyDaysMs {
		t.Fatalf("cache_ttl_ms 往返后为 %d，期望 %d", got, ninetyDaysMs)
	}
}

// TestZeroCacheTTLIsIndistinguishableFromAbsent 钉住交接契约 1。
//
// cache_ttl_ms == 0 的语义是"不要缓存"。proto3 的隐式存在性让显式的 0
// 根本不上线，接收端拿到的就是零值——也就是说"0"与"没给"在协议层面
// 无法区分。这不是缺陷，是保护：它让"没给就套本地默认值"这种实现
// 从一开始就无法写对，只能收敛到唯一安全的解释——不缓存。
//
// 本测试固定这个性质。谁把字段改成 optional（显式存在性）或包一层
// wrapper message，这里就会失败，届时必须同步审查 SDK 缓存层的 0 值处理。
func TestZeroCacheTTLIsIndistinguishableFromAbsent(t *testing.T) {
	explicitZero, err := proto.Marshal(&fpv1.ValidateTokenResponse{CacheTtlMs: 0})
	if err != nil {
		t.Fatalf("marshal explicit: %v", err)
	}
	absent, err := proto.Marshal(&fpv1.ValidateTokenResponse{})
	if err != nil {
		t.Fatalf("marshal absent: %v", err)
	}
	if len(explicitZero) != len(absent) {
		t.Fatalf("显式 0 编码 %d 字节，缺省编码 %d 字节——两者已可区分，"+
			"请同步审查 SDK 缓存层对 0 的处理", len(explicitZero), len(absent))
	}
	var out fpv1.ValidateTokenResponse
	if err := proto.Unmarshal(explicitZero, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetCacheTtlMs() != 0 {
		t.Fatalf("显式 0 解码后为 %d", out.GetCacheTtlMs())
	}
}

// TestAuthServiceSurface 钉住 AuthService 的 RPC 清单与流式属性。
//
// 少一个 RPC 会让 SDK 编译失败，那种错误不需要测试来发现。这里真正守的是
// **流式属性**：把 Watch 从双向流改成一元 RPC（或反过来）在 proto 里只是
// 两个关键字的差别，生成的 Go 代码却完全不同，而"连接永不空闲"这个
// 整套延迟论证的前提正建立在 Watch 是长流之上。
func TestAuthServiceSurface(t *testing.T) {
	svc := fpv1.File_fp_v1_auth_proto.Services().ByName("AuthService")
	if svc == nil {
		t.Fatal("auth.proto 里找不到 AuthService")
	}

	type want struct {
		clientStream bool
		serverStream bool
	}
	expected := map[protoreflect.Name]want{
		"SendLoginCode": {false, false},
		"Login":         {false, false},
		"Logout":        {false, false},
		"ValidateToken": {false, false},
		"Watch":         {true, true},
	}

	methods := svc.Methods()
	if methods.Len() != len(expected) {
		t.Fatalf("AuthService 有 %d 个 RPC，期望 %d 个——"+
			"增删 RPC 请同步更新本测试与计划文档", methods.Len(), len(expected))
	}
	for name, w := range expected {
		m := methods.ByName(name)
		if m == nil {
			t.Errorf("缺少 RPC %s", name)
			continue
		}
		if m.IsStreamingClient() != w.clientStream || m.IsStreamingServer() != w.serverStream {
			t.Errorf("RPC %s 的流式属性为 (client=%v, server=%v)，期望 (client=%v, server=%v)",
				name, m.IsStreamingClient(), m.IsStreamingServer(), w.clientStream, w.serverStream)
		}
	}
}

// TestRevokeEventCarriesTokens 钉住撤销事件必须携带 token 列表。
//
// SDK 的缓存是按 token 键控的。谁把 tokens 删掉只留 user_id，
// SDK 就无从知道该清哪些条目——退化的结果不是报错，而是撤销静默失效，
// 被踢的用户在整个 cache_ttl 内继续畅通。
func TestRevokeEventCarriesTokens(t *testing.T) {
	f := fpv1.File_fp_v1_common_proto.Messages().ByName("RevokeEvent").Fields().ByName("tokens")
	if f == nil {
		t.Fatal("RevokeEvent 缺少 tokens 字段")
	}
	if !f.IsList() || f.Kind() != protoreflect.StringKind {
		t.Fatalf("RevokeEvent.tokens 是 %v（list=%v），期望 repeated string", f.Kind(), f.IsList())
	}
}
