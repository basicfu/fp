package connector_test

import (
	"testing"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/notify"
	"github.com/basicfu/fp/internal/service"
)

// TestUserServiceSatisfiesUserLookup 保证 service.UserService 的方法签名
// 与 connector.UserLookup 保持一致。签名漂移会在这里编译失败。
func TestUserServiceSatisfiesUserLookup(t *testing.T) {
	var _ connector.UserLookup = (*service.UserService)(nil)
}

// TestCodeServiceSatisfiesCodeVerifier 保证 notify.CodeService 的签名
// 与 connector.CodeVerifier 一致。
func TestCodeServiceSatisfiesCodeVerifier(t *testing.T) {
	var _ connector.CodeVerifier = (*notify.CodeService)(nil)
}
