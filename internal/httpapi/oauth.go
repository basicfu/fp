package httpapi

import (
	"html/template"
	"net/http"
	"regexp"
	"strings"

	"github.com/basicfu/fp/internal/service"
)

// oauthCallback 接三方授权回调。对面是终端用户的浏览器，所以只有两种响应：302 回业务页面，或一张结果页。
func oauthCallback(auth *service.AuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		res := auth.HandleOAuthCallback(r.Context(), q.Get("state"), q.Get("code"))

		// 回跳地址带着一次性票据，不能被缓存，也不能经 Referer 漏给业务页面引用的第三方资源。
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if res.Redirect != "" {
			http.Redirect(w, r, res.Redirect, http.StatusFound)
			return
		}

		page := oauthPage{Title: "登录已过期", Msg: "请回到应用重新发起登录。"}
		switch res.Status {
		case "ok":
			page = oauthPage{Title: "登录成功", Msg: "请回到应用继续。"}
		case "cancelled":
			page = oauthPage{Title: "已取消登录", Msg: "可以关闭此页面。"}
		case "failed":
			page = oauthPage{Title: "登录失败", Msg: "请回到应用重试。"}
		}
		if safeAppLink(res.AppLink) {
			// 自定义协议不在 html/template 认可的安全协议里，校验过形状后显式放行。
			page.AppLink = template.URL(res.AppLink)
			if res.Status == "ok" {
				page.Msg = "正在打开应用…"
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = oauthPageTmpl.Execute(w, page)
	}
}

// appLinkPattern 限定 scheme://oauth?… 的形状，查询串只许票据用到的字符。
var appLinkPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.\-]*://oauth\?[A-Za-z0-9_\-=&%.]*$`)

// safeAppLink 只放行指向桌面 App 自定义协议的链接：协议名来自控制台配置，配成 javascript 也不能执行。
func safeAppLink(link string) bool {
	if !appLinkPattern.MatchString(link) {
		return false
	}
	switch strings.ToLower(link[:strings.Index(link, ":")]) {
	case "javascript", "vbscript", "data", "file", "http", "https":
		return false
	}
	return true
}

type oauthPage struct {
	Title   string
	Msg     string
	AppLink template.URL
}

var oauthPageTmpl = template.Must(template.New("oauth").Parse(strings.TrimSpace(`
<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;
font-family:-apple-system,"PingFang SC","Microsoft YaHei",sans-serif;background:#f6f7f9;color:#1f2329}
main{text-align:center;padding:16px}
h1{font-size:22px;margin:0 0 12px}
p{color:#646a73;margin:0 0 24px}
a{display:inline-block;padding:10px 28px;border-radius:6px;background:#07c160;color:#fff;text-decoration:none}
@media (prefers-color-scheme:dark){body{background:#17181a;color:#e8e8e8}p{color:#a0a4aa}}
</style>
</head>
<body>
<main>
<h1>{{.Title}}</h1>
<p>{{.Msg}}</p>
{{if .AppLink}}<a href="{{.AppLink}}">打开应用</a>
<script>location.href = {{.AppLink}};</script>{{end}}
</main>
</body>
</html>`)))
