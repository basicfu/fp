package im_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 本文件守住 fp-im（internal/im/**）对 fp 服务端其余部分的依赖方向：
// fp-im 对外只暴露两个接口（auth.Authenticator / auth.AppConfigSource），
// 除此之外不得触达 fp 服务端的存储、业务、领域类型、传输层，也不得绕开
// auth 直接 import fpsdk（只有 fpauth 允许，它就是接口的实现）。
//
// 与 internal/service/arch_test.go、sdk/arch_test.go 同款：用标准库
// go/parser + go/ast 直接解析源码，不 import 被测代码本身，这样即使这条
// 分层被违反，本测试依然能编译并给出明确的失败信息——如果反过来去 import
// 被测的包，违反规则的那次改动很可能已经让编译失败，测试的信号就被
// "编译不过"这件事本身淹没了，看不出到底是哪条约束被踩了。
//
// archTestDir / walkNonTestGoFiles 在每个包里各存一份是有意的，仓库没有
// 共享的测试辅助包（见 internal/service/arch_test.go 同名函数上的注释）。

// archTestDir 返回调用方源文件所在目录的绝对路径。
func archTestDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(1)
	if !ok {
		t.Fatal("runtime.Caller 失败，无法定位调用方源文件所在目录")
	}
	return filepath.Dir(file)
}

// walkNonTestGoFiles 遍历 root 下所有非测试 .go 文件，对每个文件解析出的
// AST 调用 fn。跳过 _test.go：本条约束管的是"会被编译进 fp-im 真正产物"
// 的代码，测试文件不会被其他服务引用。
func walkNonTestGoFiles(t *testing.T, root string, fn func(path string, file *ast.File, fset *token.FileSet)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("解析 %s: %v", path, err)
		}
		fn(path, file, fset)
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s: %v", root, err)
	}
}

// TestImImportRules 守住两类方向：
//   - internal/im/** 不得 import fp 服务端自己的 store/service/domain/
//     httpapi/grpcapi/connector/notify——fp-im 是独立服务，只认 auth 的
//     两个接口，这条 Go 编译器不会拦（同 module），必须靠这条测试。
//   - internal/im/** 不得 import fpsdk 的手写部分，唯一例外是 fpauth——
//     它就是 auth.Authenticator 的实现，理应是整个 im 子树里唯一知道
//     fpsdk 存在的地方；sdk/gen 是生成代码，谁都可以用（hub 包已经在用），
//     不在这条限制之列。
func TestImImportRules(t *testing.T) {
	root := archTestDir(t)
	forbidden := []struct{ prefix, reason string }{
		{"github.com/basicfu/fp/internal/store", "fp-im 没有数据库，不能借用 fp 的存储层"},
		{"github.com/basicfu/fp/internal/service", "fp-im 不理解 fp 的业务"},
		{"github.com/basicfu/fp/internal/domain", "fp-im 有自己的 model，不共享 fp 的领域类型"},
		{"github.com/basicfu/fp/internal/httpapi", "两个服务的传输层互不可见"},
		{"github.com/basicfu/fp/internal/grpcapi", "两个服务的传输层互不可见"},
		{"github.com/basicfu/fp/internal/connector", "登录方式是 fp 的事"},
		{"github.com/basicfu/fp/internal/notify", "fp-im 不发通知"},
	}
	walkNonTestGoFiles(t, root, func(path string, file *ast.File, _ *token.FileSet) {
		inFpauth := strings.Contains(filepath.ToSlash(path), "/internal/im/fpauth/")
		for _, imp := range file.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			for _, f := range forbidden {
				if p == f.prefix || strings.HasPrefix(p, f.prefix+"/") {
					t.Errorf("%s imports %q：%s", path, p, f.reason)
				}
			}
			// 只有 fpauth 可以碰 fpsdk 的手写部分；生成代码 sdk/gen 谁都可以用
			isSDK := p == "github.com/basicfu/fp/sdk" ||
				(strings.HasPrefix(p, "github.com/basicfu/fp/sdk/") && !strings.HasPrefix(p, "github.com/basicfu/fp/sdk/gen"))
			if isSDK && !inFpauth {
				t.Errorf("%s imports %q：fp-im 只能通过 auth.Authenticator / auth.AppConfigSource 接口触达 fpsdk，实现只允许在 internal/im/fpauth", path, p)
			}
		}
	})
}
