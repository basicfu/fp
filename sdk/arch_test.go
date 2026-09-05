package fpsdk

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 本文件是必修 4 的产物：分支执行过程中定下的分层约束（"sdk/ 不得 import
// internal/"、"sdk/ 里不得出现 panic"、"examples/ 不得 import internal/"）
// 此前只写在计划文档与代码注释里，没有任何一条会在被违反时让
// go build / go vet / gofmt / 全量测试变红——这与本阶段判定为最要命的
// 那处缺陷（验收测试测不到它要验收的东西）是同一类失效模式，只是这里
// 守的是整个分层，不是单个字段。
//
// 用标准库 go/parser + go/ast 直接解析源码，不依赖任何被检查代码自身的
// import 关系——这样测试才能在"sdk/ 真的引用了 internal/"这种情况下
// 依然正常编译并跑出一个明确的失败，而不是被同一个问题拖累到编译失败、
// 连"哪条断言违反了"都说不清楚。

// archTestDir 返回调用方源文件所在目录的绝对路径。不依赖 go test 的工作
// 目录假设（虽然 go test 总是把工作目录设为被测包目录，这里仍然用
// runtime.Caller 显式定位，让这个文件本身的正确性不必依赖那条约定）。
func archTestDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(1)
	if !ok {
		t.Fatal("runtime.Caller 失败，无法定位调用方源文件所在目录")
	}
	return filepath.Dir(file)
}

// walkNonTestGoFiles 遍历 root 下所有非测试 .go 文件（跳过名字在 skipDirs
// 里的子目录，例如生成产物 gen/），对每个文件解析出的 AST 调用 fn。
//
// 跳过 _test.go：本条约束管的是"这段代码会被编译进真正的产物/会被第三方
// 引用"，测试文件本身不会被引用方 import，且测试代码里出于测试目的临时
// 出现 panic（比如故意测 recover）或引用 internal/testsupport 之类的
// 辅助包不违反任何一条约束的初衷。
func walkNonTestGoFiles(t *testing.T, root string, skipDirs map[string]bool, fn func(path string, file *ast.File, fset *token.FileSet)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
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

// assertNoImportOf 断言 file 没有 import 任何前缀匹配 forbiddenPrefix 的包，
// 违反时用 reason 说明这条约束存在的原因——将来触发时，读到失败信息的人
// 不需要再去翻计划文档。
func assertNoImportOf(t *testing.T, path string, file *ast.File, forbiddenPrefix, reason string) {
	t.Helper()
	for _, imp := range file.Imports {
		p := strings.Trim(imp.Path.Value, `"`)
		if p == forbiddenPrefix || strings.HasPrefix(p, forbiddenPrefix+"/") {
			t.Errorf("%s imports %q：%s", path, p, reason)
		}
	}
}

// TestSDKDoesNotImportInternal 守住"sdk/ 不得 import internal/"。
//
// Go 的 internal 包规则拦不住同 module 内的引用——sdk/ 与 internal/ 同属
// github.com/basicfu/fp 这一个 module，sdk/ 里的代码 import
// github.com/basicfu/fp/internal/... 完全能编译通过。这条隔离必须靠纪律，
// 而"纪律"没有测试守着就只是一句空话：sdk/ 一旦悄悄长出对 internal/ 的
// 依赖，第三方接入方的 go.mod 会连带拉进 fp 服务端自己的实现细节
// （数据库驱动、内部错误类型……），而 go build ./... 不会有任何异议。
//
// 跳过 sdk/gen/：那是 protoc 生成的产物，不受人工纪律约束，也不应该被
// 这条测试意外拖进来（生成器本身可能在注释里写运行它的命令，与本条约束
// 无关）。
func TestSDKDoesNotImportInternal(t *testing.T) {
	root := archTestDir(t)
	walkNonTestGoFiles(t, root, map[string]bool{"gen": true}, func(path string, file *ast.File, _ *token.FileSet) {
		assertNoImportOf(t, path, file, "github.com/basicfu/fp/internal",
			"sdk/ 是发给第三方接入方的公开库，不得依赖 fp 服务端自己的 internal/ 实现细节"+
				"（这条 Go 编译器不会拦，必须靠这条测试）")
	})
}

// TestSDKHasNoPanic 守住"sdk/ 里不得出现 panic"。
//
// sdk/ 是被嵌入第三方进程的库代码：它的 panic 不会被这个测试文件拦下来，
// 而是直接崩掉宿主应用的整个进程——一个鉴权 SDK 拖垮整个业务进程是不可
// 接受的，库代码必须把错误包成 error 返回，交给调用方决定如何处理。
//
// 用 go/ast 找 CallExpr 且 Fun 是 Ident{Name:"panic"}，而不是对源码文本
// 做字符串匹配："panic" 出现在注释或字符串字面量里（本文件的注释就是
// 例子）不该被误报，只有真正的 panic(...) 调用才算数。
//
// 跳过 sdk/gen/：生成产物不受这条纪律约束，改动它意味着重新生成而不是
// 手工修复。
func TestSDKHasNoPanic(t *testing.T) {
	root := archTestDir(t)
	walkNonTestGoFiles(t, root, map[string]bool{"gen": true}, func(path string, file *ast.File, fset *token.FileSet) {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || ident.Name != "panic" {
				return true
			}
			pos := fset.Position(call.Pos())
			t.Errorf("%s:%d 出现 panic(...) 调用：sdk/ 是嵌入第三方进程的库代码，"+
				"panic 会直接崩掉宿主应用的整个进程，必须返回 error 交给调用方决定",
				pos.Filename, pos.Line)
			return true
		})
	})
}

// TestExamplesDoNotImportInternal 守住"examples/ 不得 import internal/"。
//
// examples/ 是给第三方接入方看的可运行样例，唯一目的是演示"只用 sdk/ 这
// 个公开包能不能把事情做成"。一旦它偷偷引用了 internal/，样例本身依然能
// 在这个仓库里编译通过（同 module），但照抄样例走的第三方接入方拿到的是
// 一个引用了不存在的包路径、根本编译不过的项目——而且这种偷懒极难在
// 代码评审里被肉眼发现，因为"能跑"和"能作为样例发布"被悄悄混为一谈。
//
// examples/ 不属于 sdk/ 子树，这里单独定位它的目录，找不到就跳过而不是
// 失败——它的存在与否不是这条测试要断言的内容。
func TestExamplesDoNotImportInternal(t *testing.T) {
	root := filepath.Join(archTestDir(t), "..", "examples")
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Skip("examples/ 目录不存在，跳过")
	}
	walkNonTestGoFiles(t, root, nil, func(path string, file *ast.File, _ *token.FileSet) {
		assertNoImportOf(t, path, file, "github.com/basicfu/fp/internal",
			"examples/ 是给第三方接入方抄的可运行样例，只应演示 sdk/ 这个公开包，"+
				"引用 internal/ 会让照抄样例的接入方拿到一个编译不过的项目")
	})
}

// TestSDKDoesNotImportWebFrameworks 守住"fpsdk 本身不绑定任何 Web 框架"。
//
// 授权的框架适配器（chi、以后的 gin/echo）必须各自成包，例如 sdk/fpchi。
// 如果适配器直接写在 package fpsdk 里，**所有**接入方 import fpsdk 时都会
// 把那个路由库连进自己的二进制——用 gin 的人被迫拖上 chi，用裸 net/http
// 的人也一样。这不是理论风险：chi 适配器最初就是写在 package fpsdk 里的，
// go build 一声不吭，go list -deps ./sdk 才看得见。
//
// 只查非测试文件：适配器包自己的测试当然要 import 它适配的那个框架。
func TestSDKDoesNotImportWebFrameworks(t *testing.T) {
	// 适配器包自身是例外——它们存在的意义就是依赖某个框架。
	adapterDirs := map[string]bool{"fpchi": true}

	frameworks := []string{
		"github.com/go-chi/chi",
		"github.com/gin-gonic/gin",
		"github.com/labstack/echo",
		"github.com/gofiber/fiber",
		"github.com/gorilla/mux",
	}

	root := archTestDir(t)
	skip := map[string]bool{"gen": true}
	for d := range adapterDirs {
		skip[d] = true
	}
	walkNonTestGoFiles(t, root, skip, func(path string, file *ast.File, _ *token.FileSet) {
		for _, fw := range frameworks {
			assertNoImportOf(t, path, file, fw,
				"fpsdk 是所有接入方都要 import 的包，绑定某个 Web 框架会让用别的框架的人"+
					"被迫把它连进二进制。框架适配器请各自成包（见 sdk/fpchi）")
		}
	})
}
