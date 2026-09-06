package integration_test

import (
	"testing"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/sdk"
)

// fp 类型的取值是线上契约，服务端（internal/domain）与 SDK（sdk/）各存了
// 一份字符串常量。sdk 不得 import internal，两边单独看都只是孤立常量，
// 改错一个 go build / vet / 全量测试照样全绿——这里是唯一能同时看到两个
// 包的地方。与 TestKeepaliveTimingIsCompatible 是同一类守护。
func TestConfigValueTypesMatch(t *testing.T) {
	for _, s := range fpsdk.ExportedConfigTypes() {
		if !domain.IsConfigValueType(s) {
			t.Errorf("SDK 的类型 %q 服务端不认", s)
		}
	}
	// 反向也要查：服务端多出一个类型而 SDK 不认，控制台上能建、
	// SDK 拉下来解析不了。
	for _, s := range []string{
		domain.ConfigValueBool, domain.ConfigValueInt, domain.ConfigValueFloat,
		domain.ConfigValueString, domain.ConfigValueArray, domain.ConfigValueObject,
	} {
		if !containsString(fpsdk.ExportedConfigTypes(), s) {
			t.Errorf("服务端的类型 %q SDK 不认", s)
		}
	}
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
