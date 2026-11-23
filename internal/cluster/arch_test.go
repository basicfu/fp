package cluster

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cluster 是 fp-im 与 fp 核心共用的中立基础件：任何一边的业务包都不能被它引用，
// 否则两边会经由它互相耦合。与 internal/im/arch_test.go 同款：直接解析源码的 import，
// 不依赖被测代码能否编译。
func TestClusterImportsNoBusinessPackages(t *testing.T) {
	const mod = "github.com/basicfu/fp/"
	forbidden := []string{
		mod + "internal/im",
		mod + "internal/store",
		mod + "internal/service",
		mod + "internal/domain",
		mod + "internal/httpapi",
		mod + "internal/grpcapi",
		mod + "sdk",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		if _, err := os.Stat(f); err != nil {
			t.Fatal(err)
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("解析 %s: %v", f, err)
		}
		for _, imp := range af.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				if p == bad || strings.HasPrefix(p, bad+"/") {
					t.Errorf("%s 不得引用 %s：internal/cluster 必须保持中立", f, p)
				}
			}
		}
	}
}
