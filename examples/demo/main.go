// Command demo 是一个接入 fp 的最小业务服务。
//
// 它演示接入 fp 需要写的全部代码：读环境变量、fpsdk.New、三个路由。
// 如果这个文件变长了，说明该把重复的部分挪进 SDK，而不是让每个业务方
// 重复写胶水代码。
//
// 用法与手工验收步骤见同目录下的 README.md。
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	fpsdk "github.com/basicfu/fp/sdk"
)

func main() {
	client, err := fpsdk.New(fpsdk.Options{
		Addr:      os.Getenv("FP_ADDR"),
		AppID:     os.Getenv("FP_APP_ID"),
		AppSecret: os.Getenv("FP_APP_SECRET"),
		// 本地开发没有 TLS。生产环境绝不要开——
		// appSecret 会随每个 RPC 以明文发送。
		Insecure: os.Getenv("FP_INSECURE") == "1",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	auth := client.Auth()
	mux := http.NewServeMux()

	// 公开路由：发验证码、登录。
	mux.HandleFunc("POST /api/login/code", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Phone string `json:"phone"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := auth.SendLoginCode(r.Context(), req.Phone); err != nil {
			fpsdk.WriteError(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})

	mux.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Phone string `json:"phone"`
			Code  string `json:"code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		res, err := auth.Login(r.Context(), fpsdk.LoginInput{
			ConnectorType: "sms_code",
			Credentials:   map[string]string{"phone": req.Phone, "code": req.Code},
			IP:            r.RemoteAddr,
			UserAgent:     r.UserAgent(),
		})
		if err != nil {
			fpsdk.WriteError(w, err)
			return
		}
		// 浏览器客户端走 cookie；curl / 移动端等不处理 Set-Cookie 的客户端
		// 从响应体里取 token，自己决定怎么存、怎么传。
		http.SetCookie(w, &http.Cookie{
			Name:     fpsdk.DefaultCookieName,
			Value:    res.Token,
			Path:     "/",
			HttpOnly: true,
		})
		writeJSON(w, map[string]any{
			"token":     res.Token,
			"sessionId": res.SessionID,
			"userId":    res.User.GetId(),
		})
	})

	// 受保护路由：一行中间件。
	mux.Handle("GET /api/me", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := fpsdk.IdentityFrom(r.Context())
		writeJSON(w, map[string]any{
			"userId":    id.UserID,
			"sessionId": id.SessionID,
			"stale":     id.Stale,
		})
	})))

	// 健康检查同时暴露推送流状态，方便手工验收时观察降级
	// （见 README 手工验收第 4 步）。
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"streamHealthy": client.StreamHealthy()})
	})

	const addr = ":8090"
	log.Printf("demo 监听 %s，fp 地址 %s", addr, os.Getenv("FP_ADDR"))
	log.Fatal(http.ListenAndServe(addr, mux))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
