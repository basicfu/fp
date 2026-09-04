package integration_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basicfu/fp/internal/im/imgrpc"
	"github.com/basicfu/fp/internal/im/model"
	fpsdk "github.com/basicfu/fp/sdk"
	fpim "github.com/basicfu/fp/sdk/im"
)

// TestImKeepalivePairing 守住 fp-im 版本的 keepalive 配对关系，与
// TestKeepaliveTimingIsCompatible（fp/fpsdk 那一对）是完全同构的另一份：
// 客户端 SDK 的 ping 间隔必须严格大于网关侧允许的最小间隔，否则网关会认为
// 客户端 ping 过频，回一个 ENHANCE_YOUR_CALM 的 GOAWAY 把连接周期性掐断。
// 两个常量分处 sdk/im（不得 import internal/）与 internal/im/imgrpc（不该
// 反过来依赖 sdk 的实现细节）两个包，只有 internal/integration 能同时看见
// 两边，单独看任何一边 go build/vet/gofmt/该包自己的测试都不会报错。
func TestImKeepalivePairing(t *testing.T) {
	if fpim.KeepaliveTime <= imgrpc.KeepaliveMinTime {
		t.Fatalf("fpim.KeepaliveTime(%v) 必须大于 imgrpc.KeepaliveMinTime(%v)，"+
			"否则网关会用 GOAWAY 周期性掐断客户端连接", fpim.KeepaliveTime, imgrpc.KeepaliveMinTime)
	}
}

// TestSubjectFormatParity 守住 subject 字符串格式的两份完整实现
// （internal/im/model.ParseSubject 与 sdk/im.Parse）必须逐字节一致。
//
// sdk/im 不得 import internal/im/model（sdk/ 不得 import internal/，见
// sdk/arch_test.go），所以 subject 解析逻辑在 sdk/im/subject.go 里整个重写
// 了一份；这份重复只能靠两边都跑同一批输入、比较结果来守住，编译器对此
// 无能为力——两份实现字面量上各自改错一个字符，各自包内的单元测试仍可能
// 全绿（各自的单元测试大概率只测了"自己那份逻辑内部自洽"，不知道另一份
// 的存在）。
//
// 输入覆盖：合法用户主体、合法访客主体、大写 uuid、无连字符 uuid、
// 版本位非 4 的 uuid、未知前缀、空串——这些正是两份实现里判断逻辑分叉的
// 关键点（Cut 是否成功、前缀是否认识、uuid 格式/大小写/版本位校验）。
func TestSubjectFormatParity(t *testing.T) {
	cases := []string{
		"u:1001",                                 // 合法用户主体
		"g:6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f", // 合法访客主体（标准写法的 uuid v4）
		"g:6F1C3C2E-4B1A-4D2E-9F0E-7A8B9C0D1E2F", // 大写 uuid：合法 uuid 但格式不被接受
		"g:6f1c3c2e4b1a4d2e9f0e7a8b9c0d1e2f",     // 无连字符 uuid
		"g:6f1c3c2e-4b1a-1d2e-9f0e-7a8b9c0d1e2f", // 版本位是 1 不是 4
		"x:1",                                    // 未知前缀
		"",                                       // 空串
	}
	for _, s := range cases {
		a, errA := model.ParseSubject(s)
		b, errB := fpim.Parse(s)
		if (errA == nil) != (errB == nil) {
			t.Fatalf("%q：model err=%v，fpim err=%v 两边是否报错不一致", s, errA, errB)
		}
		if errA == nil && a.String() != b.String() {
			t.Fatalf("%q：model=%s fpim=%s 解析结果不一致", s, a, b)
		}
	}
}

// TestGuestUUIDValidationParityViaMiddleware 覆盖第三份 subject/uuid 格式
// 校验实现：sdk/middleware.go 里未导出的 isUUIDv4。
//
// 为什么不能像上面那样直接调用比较：身份 SDK（sdk/ 顶层包 fpsdk）不得
// import sdk/im——两者是完全独立的 SDK（一个是登录鉴权，一个是 WebSocket
// 网关客户端），业务方可能只依赖其中一个，反向依赖会强迫只想要鉴权中间件
// 的接入方也拉进整个 WebSocket 依赖树；这一点决定了 isUUIDv4 必须在
// sdk/middleware.go 里再抄一份，而不是从 sdk/im 导出复用。而 isUUIDv4 本身
// 是未导出函数，internal/integration 所在的 integration_test 包既不在
// sdk 包内、也不在它的任何子包里，语言层面就够不到它。
//
// 于是这里退而求其次：通过 HTTP 中间件的可观察行为间接验证——构造
// MiddlewareOptions{AllowGuest: true} 的中间件，用同一批 uuid 输入（合法
// 访客 uuid 应该放行、大写/无连字符/版本位错误的应该以 401 拒绝），断言
// 中间件的通过/拒绝结果与 model/fpim 两份实现在同样输入上的判断一致。
//
// 覆盖边界的说明：这只验证了 isUUIDv4 在"通过 vs 拒绝"这个二元结果上与
// 另外两份一致，验证不到"拒绝的具体错误信息"或内部实现细节是否逐字节
// 相同——但 IsUUIDv4 的唯一对外契约就是这个布尔判断，间接验证已经覆盖了
// 全部可观察行为。若某天这三份出现字节级不一致但恰好不影响这个布尔结果
// （目前看不出这样的改法），这份测试不会报警，这是选择"能测到的最强断言"
// 而非因为覆盖不到就放弃这份测试的取舍。
func TestGuestUUIDValidationParityViaMiddleware(t *testing.T) {
	auth := &fpsdk.Auth{} // 零值即可：这里只走访客分支，不会碰 a.Validate，不需要真正连 fp
	mw := auth.MiddlewareWith(fpsdk.MiddlewareOptions{AllowGuest: true})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := fpsdk.IdentityFrom(r.Context())
		if !ok || !id.IsGuest() {
			t.Fatal("中间件放行了请求，但上下文里没有访客身份")
		}
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		guestID   string
		wantLegal bool // model.IsUUIDv4 / fpim.IsUUIDv4 对同一个输入的判断
	}{
		{"6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f", true},  // 合法访客 uuid
		{"6F1C3C2E-4B1A-4D2E-9F0E-7A8B9C0D1E2F", false}, // 大写
		{"6f1c3c2e4b1a4d2e9f0e7a8b9c0d1e2f", false},     // 无连字符
		{"6f1c3c2e-4b1a-1d2e-9f0e-7a8b9c0d1e2f", false}, // 版本位非 4
	}
	for _, c := range cases {
		// 先核对 model/fpim 两份实现对这个输入的判断确实是 c.wantLegal，
		// 避免这里的期望值本身就和另外两份对不上、白测了个错误的基准。
		if model.IsUUIDv4(c.guestID) != c.wantLegal || fpim.IsUUIDv4(c.guestID) != c.wantLegal {
			t.Fatalf("%q：测试用例的期望值本身与 model/fpim 不一致（model=%v fpim=%v want=%v）",
				c.guestID, model.IsUUIDv4(c.guestID), fpim.IsUUIDv4(c.guestID), c.wantLegal)
		}

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(fpsdk.GuestIDHeader, c.guestID)
		rw := httptest.NewRecorder()
		handler.ServeHTTP(rw, req)

		gotLegal := rw.Code == http.StatusOK
		if gotLegal != c.wantLegal {
			t.Fatalf("%q：中间件放行=%v，与 model/fpim 的判断(%v)不一致（HTTP 状态码 %d）",
				c.guestID, gotLegal, c.wantLegal, rw.Code)
		}
	}
}

// TestCloseCodesParity 守住 ws 关闭码的两份定义（服务端 internal/im/model
// 与客户端 sdk/im）逐个一致：SDK 靠这些码判断是否要自动重连、要不要退避
// （见 sdk/im/client.go 的 noAutoReconnect），服务端靠它们决定关闭时传什么
// 状态码；两边对同一个整数字面量的理解一旦分叉，SDK 会误判该不该重连，
// 而这个错判不会体现在任何一边自己的单元测试里。
func TestCloseCodesParity(t *testing.T) {
	pairs := []struct {
		name        string
		server, sdk int
	}{
		{"AuthFailed", model.CloseAuthFailed, fpim.CloseAuthFailed},
		{"PolicyRejected", model.ClosePolicyRejected, fpim.ClosePolicyRejected},
		{"Kicked", model.CloseKicked, fpim.CloseKicked},
		{"Unavailable", model.CloseUnavailable, fpim.CloseUnavailable},
		{"IdleTimeout", model.CloseIdleTimeout, fpim.CloseIdleTimeout},
		{"Backpressure", model.CloseBackpressure, fpim.CloseBackpressure},
	}
	for _, p := range pairs {
		if p.server != p.sdk {
			t.Fatalf("关闭码 %s 两边不一致：model=%d fpim=%d", p.name, p.server, p.sdk)
		}
	}
}

// TestCredentialMetadataKeysParity 守住业务 server 凭据的 metadata 键。
//
// imgrpc.MDAppID/MDAppSecret 与身份平台（internal/grpcapi）用的键
// （"fp-app-id"/"fp-app-secret"，见 internal/grpcapi/auth_interceptor.go 的
// mdAppID/mdAppSecret）必须完全相同，这样业务方配一份 app_id/app_secret
// 就能同时连身份平台与 fp-im 网关。grpcapi 那两个常量是未导出的，够不到、
// 也不必再导出——这里改为对字面量断言：这两个字符串本身就是契约，字面量
// 在这里写死是有意的，一旦哪边的键名改了，这条测试和 grpcapi 侧的字面量
// 都需要人工同步修改，不存在"改了一边另一边浑然不知"的风险，因为字面量
// 无法被自动重构工具静默改掉。sdk/im 侧同样把这两个键写成字面量
// （见 sdk/im/server.go 的 appCreds.GetRequestMetadata），三处字面量必须
// 手工保持一致。
func TestCredentialMetadataKeysParity(t *testing.T) {
	if imgrpc.MDAppID != "fp-app-id" || imgrpc.MDAppSecret != "fp-app-secret" {
		t.Fatalf("imgrpc 的 metadata 键(%q/%q)必须与身份平台 grpcapi 用的完全相同，"+
			"业务方才能用同一对凭据同时接入身份平台与 fp-im 网关",
			imgrpc.MDAppID, imgrpc.MDAppSecret)
	}
}
