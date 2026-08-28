package service_test

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

// 本文件是必修 4 的产物：service 包"不得 import httpapi / net/http / grpc /
// 任何 *.pb.go"这条约束此前只写在计划文档与代码注释里——违反它，
// go build / go vet / gofmt / 全量测试照样全绿，没有任何信号能告诉任何人
// 这道分层被悄悄打破了。用标准库 go/parser + go/ast 直接解析源码，
// 不依赖被检查代码自身能否编译。
//
// 这条约束存在的理由：service 是编排业务规则的地方，唯一知道"登录该按
// 什么顺序发生"，故意写成对传输协议一无所知——同一套 service 既要能被
// internal/httpapi 的 REST 接口调用，也要能被 internal/grpcapi 的 gRPC
// 接口调用，谁引入了对某一种协议（net/http、grpc、生成的 pb 类型）的
// 依赖，都会让另一条传输路径被迫连带引入它根本用不上的东西，也让
// service 本该承担的"协议无关"这条设计前提名存实亡。

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
// AST 调用 fn。
//
// 跳过 _test.go：本条约束管的是"这段代码会被编译进真正的 service 包产物、
// 被 httpapi/grpcapi 两条传输路径共用"，测试文件不会被两边同时引用，
// 测试里引用 testsupport、甚至 httptest 之类的辅助包不违反约束的初衷
// （本包测试确实这样做，例如经由 internal/testsupport 连测试库）。
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

// TestServiceDoesNotImportTransportPackages 守住"service 包不得 import
// httpapi / net/http / grpc / 任何 *.pb.go"。
//
// 四条禁止项对应四类"协议漏进业务层"的具体路径：
//   - net/http：直接依赖 HTTP 的请求/响应类型，等于把 REST 语义焊死进业务
//     规则里，grpcapi 想复用这段逻辑就要先削这层依赖。
//   - google.golang.org/grpc：反过来，依赖 gRPC 的类型或错误码，
//     httpapi 就没法干净地复用。
//   - internal/httpapi：更直接——service 反过来依赖它本该被调用的上层，
//     会形成包级循环依赖的风险，且明确说明分层已经画反了。
//   - sdk/gen（生成的 *.pb.go 所在包）：service 若直接用 proto 生成的类型
//     构造返回值，等于把 gRPC 的传输编码焊进了业务对象，httpapi 那侧
//     的响应类型就不再能独立演化。
func TestServiceDoesNotImportTransportPackages(t *testing.T) {
	root := archTestDir(t)
	forbidden := []struct {
		prefix string
		reason string
	}{
		{"net/http", "service 是协议无关的业务编排层，依赖 net/http 会把 REST 语义焊死进业务规则，httpapi 与 grpcapi 就没法共用同一套 service"},
		{"google.golang.org/grpc", "service 是协议无关的业务编排层，依赖 grpc 会把 gRPC 语义焊死进业务规则，httpapi 与 grpcapi 就没法共用同一套 service"},
		{"github.com/basicfu/fp/internal/httpapi", "service 是被 httpapi 调用的下层，反过来依赖 httpapi 会形成分层倒置/循环依赖"},
		{"github.com/basicfu/fp/sdk/gen", "sdk/gen 是 gRPC 生成的传输类型，service 直接使用会把协议编码焊进业务对象，httpapi 的响应类型就没法独立演化"},
	}
	walkNonTestGoFiles(t, root, func(path string, file *ast.File, _ *token.FileSet) {
		for _, imp := range file.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			for _, f := range forbidden {
				if p == f.prefix || strings.HasPrefix(p, f.prefix+"/") {
					t.Errorf("%s imports %q：%s", path, p, f.reason)
				}
			}
		}
	})
}
