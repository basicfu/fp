package integration_test

import (
	"testing"

	"github.com/basicfu/fp/internal/grpcapi"
	fpsdk "github.com/basicfu/fp/sdk"
)

// TestKeepaliveTimingIsCompatible 守住必修 5：两个数值配对约束。
//
// 计划第七节自己把它们标成"错了不报错只出怪事"：两边的常量分处
// fpsdk（sdk/client.go）与 grpcapi（internal/grpcapi/server.go）两个包，
// sdk 不得 import internal/（见 sdk/arch_test.go），internal/grpcapi 也
// 不该反过来依赖 sdk 的实现细节，所以这条配对关系只能在能同时看到两边
// 的地方核对——internal/integration 是唯一合法的位置。
//
// 单独看任何一边，改错一个字面量 go build / go vet / gofmt / 全量测试
// 照样全绿：keepalive 的错配不会让任何 RPC 失败，只会让连接被服务端
// 周期性的 GOAWAY 掐断、SDK 再重连，在几秒到几十秒跑一次的测试里根本
// 来不及显形；MaxConnectionAge 归零也不影响任何功能测试，只有"fp 扩容
// 后存量连接会不会重新分摊"这条验收表第 16 项会受影响，而这条目前
// 也没有测试覆盖。
func TestKeepaliveTimingIsCompatible(t *testing.T) {
	if fpsdk.KeepaliveTime <= grpcapi.KeepaliveMinTime {
		t.Fatalf("客户端 keepalive Time (%s) 未严格大于服务端 KeepaliveMinTime (%s)——"+
			"配反了的话，服务端会认为客户端 ping 过频，回一个 ENHANCE_YOUR_CALM 的 "+
			"GOAWAY 把连接周期性掐断，双方都'配了 keepalive'，结果连接反而被掐断",
			fpsdk.KeepaliveTime, grpcapi.KeepaliveMinTime)
	}

	if grpcapi.MaxConnectionAge <= 0 {
		t.Fatalf("MaxConnectionAge 为 %s，必须 > 0——为 0 意味着 gRPC 长连接永远不会被"+
			"主动回收，fp 扩容后存量 SDK 连接会永远钉在老实例上，新实例长期空转，"+
			"且没有任何报错：扩容等于白扩", grpcapi.MaxConnectionAge)
	}
	if grpcapi.MaxConnectionAgeGrace <= 0 {
		t.Fatalf("MaxConnectionAgeGrace 为 %s，必须 > 0——为 0 意味着连接到龄后立刻被"+
			"强制切断，Watch 长流在途的撤销推送会被硬中断而不是优雅收尾",
			grpcapi.MaxConnectionAgeGrace)
	}
}
