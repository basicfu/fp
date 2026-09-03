package fpsdk

import (
	"testing"

	fpimv1 "github.com/basicfu/fp/sdk/gen/fp/im/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// TestImServiceSurface 钉住 ImService 的形状：只有一条双向流。
// 有人加一元 RPC 时这里会红，逼他先想清楚为什么不走这条流。
func TestImServiceSurface(t *testing.T) {
	svc := fpimv1.File_fp_im_v1_im_proto.Services().ByName("ImService")
	if svc == nil {
		t.Fatal("proto 里找不到 ImService")
	}
	if got := svc.Methods().Len(); got != 1 {
		t.Fatalf("ImService 应只有 1 个方法，实际 %d", got)
	}
	m := svc.Methods().Get(0)
	if m.Name() != protoreflect.Name("Connect") || !m.IsStreamingClient() || !m.IsStreamingServer() {
		t.Fatalf("唯一方法应是双向流 Connect，实际 %s client=%v server=%v", m.Name(), m.IsStreamingClient(), m.IsStreamingServer())
	}
}

// TestImEnumZeroValues 钉住枚举零值语义：UNSPECIFIED 永远不能被当成有效状态。
//
// PushStatus 与 EventKind 都是版本兼容面：Go SDK 与 fp-im server 可能跑
// 不同版本的构建，双方都靠这些数值（而不是名字）在线上对话。谁把
// EventKind 重新编号或调整顺序，编译期不会有任何警示——混合版本部署时，
// 旧 SDK 会把一条 Disconnected（本该是 2）当成 Connected（1）甚至
// Unspecified（0）来处理，后果是断线事件被误判为上线事件，且没有任何
// 信号说明发生了错位。PushStatus 同理：Sent/NotOnline/Unavailable 的
// 数值一旦漂移，SDK 对推送结果的判断就会静默错位。
func TestImEnumZeroValues(t *testing.T) {
	if fpimv1.PushStatus_PUSH_STATUS_UNSPECIFIED != 0 || fpimv1.EventKind_EVENT_KIND_UNSPECIFIED != 0 {
		t.Fatal("枚举零值必须是 UNSPECIFIED")
	}
	if fpimv1.PushStatus_PUSH_STATUS_SENT != 1 || fpimv1.PushStatus_PUSH_STATUS_NOT_ONLINE != 2 || fpimv1.PushStatus_PUSH_STATUS_UNAVAILABLE != 3 {
		t.Fatal("PushStatus 的线上数值不能改，SDK 与 im 可能不同版本")
	}
	if fpimv1.EventKind_EVENT_KIND_CONNECTED != 1 || fpimv1.EventKind_EVENT_KIND_DISCONNECTED != 2 {
		t.Fatal("EventKind 的线上数值不能改：SDK 与 im 可能不同版本，" +
			"错位会让 Disconnected 被误判为 Connected 或 Unspecified")
	}
}
