package integration_test

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// allowedDirectDependencies 是当前允许出现在 go.mod 顶层 require 块里、
// 且不带 // indirect 后缀的模块集合（direct 依赖）。
//
// 第二阶段（gRPC 服务端 + Go SDK）的实施计划只批准新增四个依赖：
// google.golang.org/grpc、google.golang.org/protobuf、
// github.com/hashicorp/golang-lru/v2、golang.org/x/sync（下面各自标注了
// 用途）。其余都是第一阶段已经在用的。
//
// 这个列表本身就是登记表：新增依赖需要先在这里登记一行，并在旁边的
// 注释里说明为什么它是必要的，TestGoModDirectDependenciesAreWhitelisted
// 才会认。
var allowedDirectDependencies = []string{
	"github.com/alibabacloud-go/darabonba-openapi",
	"github.com/alibabacloud-go/dysmsapi-20170525/v2",
	"github.com/alibabacloud-go/tea",
	"github.com/coder/websocket", // fp-im 的 ws 传输；纯 Go、无间接依赖、ctx 驱动的 API
	"github.com/go-chi/chi/v5",
	"github.com/google/uuid",
	"github.com/hashicorp/golang-lru/v2", // 第二阶段新增：sdk 本地校验结果缓存（cache.go 的 LRU）
	"github.com/jackc/pgx/v5",
	"github.com/pressly/goose/v3",
	"github.com/redis/go-redis/v9",
	"golang.org/x/crypto",
	"golang.org/x/sync",          // 第二阶段新增：Auth.Validate 用 singleflight 合并并发回源
	"google.golang.org/grpc",     // 第二阶段新增：gRPC 服务端（internal/grpcapi）与 Go SDK 的传输层
	"google.golang.org/protobuf", // 第二阶段新增：Watch/ValidateToken 等 RPC 的消息类型（sdk/gen）
}

// TestGoModDirectDependenciesAreWhitelisted 守住依赖白名单。
//
// 分层约束（sdk/ 不得 import internal/、service 不得 import 传输层等）
// 已经由 sdk/arch_test.go 与 internal/service/arch_test.go 用 go/parser
// 守住，但"本阶段只允许新增这四个依赖"这条此前没有任何测试覆盖——
// go.mod 里悄悄多一行 require，go build / go vet / gofmt / 全量测试
// 照样全绿，不属于任何一条既有约束会拦下来的变化。
//
// 故意不用第三方库解析 go.mod：golang.org/x/mod/modfile 本身就不在允许
// 的依赖列表里，引入它来测"依赖列表有没有失控"是自我循环。go.mod 是
// go 工具链维护的规整格式，标准库按行扫描 require (...) 块、只需要区分
// 一行是不是带 // indirect 后缀就够了，不需要真正的 go.mod 语法解析器。
//
// 只比较模块路径（集合），不比较版本号：版本升级（安全补丁、功能更新）
// 是正常且频繁的活动，不该被这条测试拦下来；这条测试要挡的是"多了/少了
// 一个模块"，不是"某个模块的版本变了"。
func TestGoModDirectDependenciesAreWhitelisted(t *testing.T) {
	got := parseDirectRequires(t, repoGoModPath(t))

	want := make(map[string]bool, len(allowedDirectDependencies))
	for _, m := range allowedDirectDependencies {
		want[m] = true
	}

	var extra, missing []string
	for m := range got {
		if !want[m] {
			extra = append(extra, m)
		}
	}
	for m := range want {
		if !got[m] {
			missing = append(missing, m)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)

	if len(extra) > 0 {
		t.Errorf("go.mod 里出现了不在白名单的 direct 依赖：%v——"+
			"新增依赖需要先在 dependency_whitelist_test.go 的 "+
			"allowedDirectDependencies 里登记，并说明为什么它是必要的", extra)
	}
	if len(missing) > 0 {
		t.Errorf("allowedDirectDependencies 里的依赖在 go.mod 中已经找不到了：%v——"+
			"如果是有意移除，请同步从白名单里删掉那一行", missing)
	}
}

// repoGoModPath 返回仓库根目录下 go.mod 的绝对路径。
//
// 用 runtime.Caller 定位本文件自身所在目录（internal/integration），
// 向上两级即仓库根——不依赖 go test 把工作目录设为被测包目录这条约定
// （虽然这条约定总是成立），做法与 sdk/arch_test.go、
// internal/service/arch_test.go 的 archTestDir 一致。
func repoGoModPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败，无法定位仓库根目录")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "go.mod")
}

// parseDirectRequires 按行解析 go.mod，返回 require (...) 块里没有
// // indirect 后缀的模块路径集合。
//
// 支持多个 require 块（go 工具链维护的 go.mod 通常把 direct 与 indirect
// 分成两个独立的 require 块），也不依赖这种分块——分类完全按每一行是否
// 带 // indirect 后缀决定，即使将来 go mod tidy 把两者合并进同一个块，
// 这里的解析依然正确。
func parseDirectRequires(t *testing.T, path string) map[string]bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("打开 %s: %v", path, err)
	}
	defer f.Close()

	direct := make(map[string]bool)
	inBlock := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case !inBlock && line == "require (":
			inBlock = true
			continue
		case inBlock && line == ")":
			inBlock = false
			continue
		case !inBlock:
			continue
		case line == "" || strings.HasPrefix(line, "//"):
			continue
		}

		indirect := strings.Contains(line, "// indirect")
		if indirect {
			line = strings.TrimSpace(strings.SplitN(line, "// indirect", 2)[0])
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if !indirect {
			direct[fields[0]] = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("读取 %s: %v", path, err)
	}
	return direct
}
