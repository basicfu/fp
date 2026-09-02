package httpapi

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// notBuiltMessage 在前端尚未构建时返回。
//
// 给一句能照着做的话，而不是空白页：仓库里 web/dist/ 只有一个 .gitkeep，
// 任何 clone 下来直接 go run 的人都会撞上这个页面。
const notBuiltMessage = "管理控制台前端尚未构建。请先运行 ./scripts/build-web.sh 再启动 fp。"

// newStaticHandler 返回托管管理控制台前端的 handler。
//
// 路径解析规则，按顺序：
//  1. dist 里存在同名文件      → 直接返回该文件
//  2. 路径以 /assets/ 开头     → 404。assets 下的文件名都带内容哈希，
//     找不到就是真的没有，回退成 index.html 只会让浏览器把一份 HTML
//     当 JS 执行，报出与真实原因无关的语法错误
//  3. 其余路径                 → 回 index.html，交给前端路由
//
// 注意这个 handler 不认识 /admin/api——那些路由在 chi 里注册得更具体，
// 根本走不到这里；API 的 404 由 /admin/api 子路由自己的 NotFound 处理。
func newStaticHandler(dist fs.FS) http.Handler {
	if dist == nil {
		return notBuiltHandler()
	}
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		return notBuiltHandler()
	}

	fileServer := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := path.Clean("/" + r.URL.Path)
		name := strings.TrimPrefix(upath, "/")

		if name != "" {
			if info, err := fs.Stat(dist, name); err == nil && !info.IsDir() {
				// Vite 给 assets/ 下的文件名都加了内容哈希，内容一变文件名
				// 就变，可以放心长缓存。其余文件（favicon 之类）不加。
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
			if strings.HasPrefix(name, "assets/") {
				http.NotFound(w, r)
				return
			}
		}

		// SPA 回退：index.html 必须每次revalidate，否则发布新版本后用户
		// 拿着缓存里的旧 HTML 去请求已经不存在的旧 JS 文件名。
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(index)
	})
}

// notBuiltHandler 是前端未构建时的降级 handler。
func notBuiltHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(notBuiltMessage))
	})
}
