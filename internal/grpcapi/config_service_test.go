package grpcapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/domain"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// GetConfig 只返回已配置的项：value 为 JSON null 的"未配置"项不出现，
// SDK 因此可以把 missing 算成"struct 里有、values 里没有"。
//
// 【辨别力】必须同时造出"已配置"和"未配置"两种项：只造一种的话，
// 一个不做过滤、把 null 也吐出去的实现照样会绿。
func TestGetConfigOmitsUnsetFields(t *testing.T) {
	e := newGRPCEnv(t)
	ctx := context.Background()

	if _, err := e.configs.Save(ctx, e.app.ID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"fee_rate": {Type: domain.ConfigValueFloat, Value: json.RawMessage(`0.02`)},
		"api_key":  {Type: domain.ConfigValueString, Value: json.RawMessage(`null`)},
	}, false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	resp, err := e.configClient.GetConfig(e.authed(ctx), &fpv1.GetConfigRequest{
		Type: domain.ConfigTypeDefault,
	})
	if err != nil {
		t.Fatalf("GetConfig 失败: %v", err)
	}
	if resp.GetVersion() != 1 {
		t.Fatalf("version = %d，期望 1", resp.GetVersion())
	}

	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(resp.GetValues()), &values); err != nil {
		t.Fatalf("values 不是合法 JSON 对象: %v", err)
	}
	if got := string(values["fee_rate"]); got != "0.02" {
		t.Fatalf("fee_rate = %s，期望 0.02", got)
	}
	if _, ok := values["api_key"]; ok {
		t.Fatal("未配置的 api_key 不该出现在 values 里")
	}
}

// 分区隔离穿到 gRPC 出口。
// 【辨别力】两个分区的值必须不同，否则"分区对了"和"压根没分区"同结果。
func TestGetConfigIsolatesPartitions(t *testing.T) {
	e := newGRPCEnv(t)
	ctx := context.Background()

	mustSaveTyped(t, e, domain.ConfigTypeDefault, `"后端"`)
	mustSaveTyped(t, e, domain.ConfigTypeWeb, `"前端"`)

	for _, c := range []struct{ typ, want string }{
		{domain.ConfigTypeDefault, `"后端"`},
		{domain.ConfigTypeWeb, `"前端"`},
	} {
		resp, err := e.configClient.GetConfig(e.authed(ctx), &fpv1.GetConfigRequest{Type: c.typ})
		if err != nil {
			t.Fatalf("%s: GetConfig 失败: %v", c.typ, err)
		}
		var values map[string]json.RawMessage
		if err := json.Unmarshal([]byte(resp.GetValues()), &values); err != nil {
			t.Fatalf("%s: values 不是合法 JSON: %v", c.typ, err)
		}
		if got := string(values["site.title"]); got != c.want {
			t.Fatalf("%s: site.title = %s，期望 %s", c.typ, got, c.want)
		}
	}
}

func TestGetConfigRejectsUnknownPartition(t *testing.T) {
	e := newGRPCEnv(t)
	_, err := e.configClient.GetConfig(e.authed(context.Background()),
		&fpv1.GetConfigRequest{Type: "MOBILE"})
	if err == nil {
		t.Fatal("未知分区应当被拒绝")
	}
	// 状态码走既有的 StatusFrom 映射，与 HTTP 层的 400 一一对应。
	if code := status.Code(err); code != codes.InvalidArgument {
		t.Fatalf("未知分区返回 %v，期望 InvalidArgument", code)
	}
}

// 该分区一个版本都没有时返回 version=0 与空对象，不是错误——
// "还没配过任何东西"是正常状态，SDK 会据此把全部字段算进 missing。
func TestGetConfigEmptyPartition(t *testing.T) {
	e := newGRPCEnv(t)
	resp, err := e.configClient.GetConfig(e.authed(context.Background()),
		&fpv1.GetConfigRequest{Type: domain.ConfigTypeDefault})
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if resp.GetVersion() != 0 {
		t.Fatalf("version = %d，期望 0", resp.GetVersion())
	}
	if resp.GetValues() != "{}" {
		t.Fatalf("values = %q，期望 {}", resp.GetValues())
	}
}

// mustSaveTyped 在给定分区保存一个 site.title 字符串配置项（push=false，
// 这里只测 gRPC 出口读到的落库结果，用不到 Redis 广播）。
func mustSaveTyped(t *testing.T, e *grpcEnv, typ, jsonValue string) {
	t.Helper()
	if _, err := e.configs.Save(context.Background(), e.app.ID, typ, map[string]domain.ConfigField{
		"site.title": {Type: domain.ConfigValueString, Value: json.RawMessage(jsonValue)},
	}, false); err != nil {
		t.Fatalf("保存分区 %s 失败: %v", typ, err)
	}
}

// 停用的应用不该还能拉到配置。
//
// 这条守护的是 GetConfig 走 GetActiveByAppID 而不是 GetByAppID——
// Watch 那条路径历史上正是因为用错而让 status 变成"没人读的死开关"
// （见 TestWatchRejectsDisabledApplication 附近的注释）。
func TestGetConfigRejectsDisabledApplication(t *testing.T) {
	e := newGRPCEnv(t)
	ctx := context.Background()

	// 先存一份配置，确保失败原因是"应用被停用"而不是"没有配置"。
	if _, err := e.configs.Save(ctx, e.app.ID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"fee_rate": {Type: domain.ConfigValueFloat, Value: json.RawMessage(`0.02`)},
	}, false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	e.disableApplication(t)

	_, err := e.configClient.GetConfig(e.authed(ctx), &fpv1.GetConfigRequest{
		Type: domain.ConfigTypeDefault,
	})
	if err == nil {
		t.Fatal("停用的应用不该能拉到配置")
	}
	if got := status.Code(err); got != codes.PermissionDenied {
		t.Fatalf("状态码 = %v，期望 PermissionDenied", got)
	}
}

// 保存并选择推送 → Watch 流上收到 ConfigChanged。
func TestWatchDeliversConfigChanged(t *testing.T) {
	e := newGRPCEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := e.client.Watch(e.authed(ctx))
	if err != nil {
		t.Fatalf("建流失败: %v", err)
	}
	// 先吃掉 ready，确保服务端已经订上，之后的变更不会漏推。
	if msg, err := stream.Recv(); err != nil || msg.GetReady() == nil {
		t.Fatalf("首条消息应当是 ready，得到 %v / %v", msg, err)
	}

	if _, err := e.configs.Save(ctx, e.app.ID, domain.ConfigTypeWeb, map[string]domain.ConfigField{
		"site.title": {Type: domain.ConfigValueString, Value: json.RawMessage(`"商城"`)},
	}, true); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	for {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatalf("收流失败: %v", err)
		}
		cc := msg.GetConfigChanged()
		if cc == nil {
			continue // 可能先来别的事件类型
		}
		if cc.GetType() != domain.ConfigTypeWeb {
			t.Fatalf("Type = %q，期望 WEB", cc.GetType())
		}
		return
	}
}

// 「仅落库」不推送。
// 【辨别力】两条腿都要断言——这里断言"没推"，Task 11 的 SDK 测试断言
// "新起一次 Bind 能拿到新值"。只断言前者的话，一个根本没存的实现也会绿。
func TestWatchSilentWhenPushDisabled(t *testing.T) {
	e := newGRPCEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream, err := e.client.Watch(e.authed(ctx))
	if err != nil {
		t.Fatalf("建流失败: %v", err)
	}
	if msg, err := stream.Recv(); err != nil || msg.GetReady() == nil {
		t.Fatalf("首条消息应当是 ready，得到 %v / %v", msg, err)
	}

	if _, err := e.configs.Save(ctx, e.app.ID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"n": {Type: domain.ConfigValueInt, Value: json.RawMessage(`1`)},
	}, false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	// 保存后不该有任何 ConfigChanged。用一个短超时来观测"什么都没发生"。
	done := make(chan *fpv1.WatchResponse, 1)
	go func() {
		msg, err := stream.Recv()
		if err == nil {
			done <- msg
		}
		close(done)
	}()
	select {
	case msg := <-done:
		if msg != nil && msg.GetConfigChanged() != nil {
			t.Fatal("选了「仅落库」却推送了 ConfigChanged")
		}
	case <-time.After(800 * time.Millisecond):
		// 什么都没来，正是期望
	}
}
