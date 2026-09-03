// Package web 把管理控制台的前端构建产物嵌进二进制。
package web

import (
	"embed"
	"io/fs"
)

// 必须写 all: 前缀。
//
// 不带 all: 的话 embed 会跳过以 . 开头的文件，而仓库里 web/dist/ 只提交了
// 一个 .gitkeep（构建产物不入库）——于是"目录里没有可嵌入的文件"，编译
// 直接失败：cannot embed directory web/dist: contains no embeddable files。
// 带上 all: 之后，没跑过 npm 的人也能 go build ./...，只是打开控制台会看到
// 一句"前端尚未构建"的提示（见 httpapi/static.go）。
//
//go:embed all:dist
var assets embed.FS

// Dist 返回以 dist/ 为根的前端产物文件系统。
func Dist() fs.FS {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		// embed 的目录结构在编译期就固定了，这里失败意味着上面的 embed
		// 指令被改坏了，属于编译期就该发现的错误。
		panic("web: 无法定位 dist 目录: " + err.Error())
	}
	return sub
}
