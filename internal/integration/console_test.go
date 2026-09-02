package integration_test

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestConsoleDistGitkeepTrackedByGit 守住"web/dist/.gitkeep 必须留在版本库
// 里"这条性质。
//
// web/embed.go 用 //go:embed all:dist 把管理控制台前端产物嵌进二进制，这条
// 指令能在前端还没构建时也编译通过，唯一依托就是 fresh clone 出来的
// web/dist/ 目录不是空的——而这靠的正是这一个被 git 跟踪的占位文件（仓库根
// .gitignore 用 web/dist/* + !web/dist/.gitkeep 精确放行了它，见
// internal/httpapi 那一批测试和 task-3 的验收记录）。它一旦从版本库里消失，
// 任何 fresh clone 在没跑前端构建的情况下 go build ./... 会直接报
// "cannot embed directory web/dist: contains no embeddable files"。
//
// 这条测试特意去问 git（git ls-files --error-unmatch），而不是用 os.Stat
// 查磁盘上文件在不在：真正危险的场景恰恰是"本地磁盘上文件还在，但 git
// 已经不再跟踪它了"。例如 vite build 默认会在写入产物前清空 dist/
// （emptyOutDir），这一步会把 .gitkeep 从磁盘删掉；即便 web/package.json 的
// build 脚本会在 vite build 之后把它重新写回磁盘（本仓库确实这么做了），
// 只要有人在这条防线补上之前于本地跑过一次旧版构建、又顺手 git add -A，
// 这次删除就会被提交——本地磁盘上因为构建产物本身还在、看起来一切正常，
// 只有下一个 clone 这个仓库、还没跑过前端构建的人会撞上编译错误。这种
// "破坏者看不见、受害者说不清"的缺陷，只查磁盘的测试完全无感，必须真的去问
// git 版本库本身。
func TestConsoleDistGitkeepTrackedByGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("本机没有 git 可执行文件，跳过")
	}

	// repoGoModPath 定义在同目录 dependency_whitelist_test.go：用
	// runtime.Caller 定位本文件所在目录、向上两级得到仓库根，不依赖工作
	// 目录约定。直接复用它取仓库根，避免重复一份等价的 runtime.Caller 逻辑。
	repoRoot := filepath.Dir(repoGoModPath(t))

	cmd := exec.Command("git", "ls-files", "--error-unmatch", "web/dist/.gitkeep")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf(
			"web/dist/.gitkeep 未被 git 跟踪（git ls-files --error-unmatch 失败：%v；输出：%s）——"+
				"这个占位文件是 web/embed.go 的 //go:embed all:dist 能在前端未构建时通过编译的"+
				"唯一依托，从版本库里消失会让任何 fresh clone 无法 go build ./...",
			err, out,
		)
	}
}
