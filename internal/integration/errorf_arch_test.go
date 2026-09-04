package integration_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoDomainErrorf 禁止 domain.Errorf 复活。
//
// 它已经被全量迁移到 domain.Fail/Failf 并删除——"每个对外错误都有码"
// 从此由编译器保证，而不是靠约定。但编译器只能保证"现在不存在"：将来
// 有人为了图省事再加回一个 Errorf（或者别的绕过码的构造方式），代码照样
// 能编译、所有既有测试照样全绿，而那个错误在传输层会被降级成 INTERNAL、
// 500，接入方看到的又是一个没有原因的失败——正是这次要修的缺陷。
//
// 用 go/parser 而不是 grep：注释和字符串里出现这个词是允许的（本测试
// 自己的注释就有），只有**函数声明与调用**才算违规。仓库里已有同类做法，
// 见 sdk/arch_test.go 与 internal/service/arch_test.go。
func TestNoDomainErrorf(t *testing.T) {
	root := filepath.Dir(repoGoModPath(t))

	var violations []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// 跳过非源码目录。.claude/worktrees 下是**其他功能分支的独立
			// 工作区**，不属于本仓库当前分支的代码，扫它只会误报。
			switch d.Name() {
			case ".git", "node_modules", "web", ".claude", ".superpowers":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}

		rel, _ := filepath.Rel(root, path)
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.FuncDecl:
				if node.Name.Name == "Errorf" && node.Recv == nil {
					violations = append(violations,
						rel+": 声明了 Errorf——请用 domain.Fail/Failf 并显式带码")
				}
			case *ast.SelectorExpr:
				// domain.Errorf(...) 这种带包名的调用。
				if ident, ok := node.X.(*ast.Ident); ok &&
					ident.Name == "domain" && node.Sel.Name == "Errorf" {
					violations = append(violations,
						rel+": 调用了 domain.Errorf——请用 domain.Fail/Failf 并显式带码")
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("遍历源码: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("发现 %d 处对已删除的 domain.Errorf 的引用：\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}
