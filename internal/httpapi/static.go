package httpapi

import (
	"crypto/sha256"
	"fmt"
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
//  1. 路径以 /static/ 开头     → 去掉前缀去 dist 里找同名文件，找不到
//     404。这个前缀是给 CDN 回源用的：Vite 构建时把 base 配成 /static/
//     （或者一个指向 CDN 域名下 /static/ 的绝对 URL），index.html 里
//     引用的资源地址因此天然带这个前缀，CDN 只要把自己的路径映射到
//     源站的 /static/ 就行，不用碰 index.html 本身。
//  2. 其余路径                 → 回 index.html，交给前端路由；带 ETag，
//     内容没变时回 304，不用每次都整份重传
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
	// index.html 的内容只在进程启动时读这一次（下次改动要等重新构建、
	// 重启进程），ETag 提前算好，不用每个请求都重新哈希一遍。
	indexETag := fmt.Sprintf(`"%x"`, sha256.Sum256(index))

	fileServer := http.StripPrefix("/static", http.FileServer(http.FS(dist)))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := path.Clean("/" + r.URL.Path)

		if name, ok := strings.CutPrefix(upath, "/static/"); ok && name != "" {
			if info, err := fs.Stat(dist, name); err == nil && !info.IsDir() {
				// assets/ 下的文件名带 Vite 加的内容哈希，内容一变文件名
				// 就变，可以放心长缓存；public/ 目录直接拷过来的文件
				// （比如 favicon.svg）文件名不带哈希，不能这样缓存，
				// 不然改了图标线上还是老的。
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
			// 找不到就是真的没有，回退成 index.html 只会让浏览器把一份
			// HTML 当 JS 执行，报出与真实原因无关的语法错误。
			http.NotFound(w, r)
			return
		}

		// SPA 回退：index.html。Cache-Control: no-cache 只是告诉客户端
		// "每次都要来验证一下"，真正省流量靠下面这段 ETag 比对——没变就
		// 回 304，不用整份 HTML 都传一遍。
		if r.Header.Get("If-None-Match") == indexETag {
			w.Header().Set("ETag", indexETag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", indexETag)
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
