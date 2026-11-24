// Package resp 是 fp 与 kkdesk 共用的 HTTP 响应格式(设计: kkdesk 仓库 docs/specs/2026-11-24-unified-http-response-design.md)。
//
// **本包是两个项目的统一约束: 改字段、状态码、错误码类别、请求头或 relogin 规则,必须 fp 与 kkdesk 同步改、同步发版。**
//
// 请求带 `X-Resp-Format: 2` 才返回新格式, 不带则各服务保持原样(已发布客户端不受影响)。新格式只有四个状态码:
//
//	200  {data, rid, time}                       成功: 没有 code / msg
//	400  {code, msg, data?, rid, time}           业务错误: data 只在有业务详情时带
//	401  {code, msg, data?, rid, time}           未登录 / 登录过期 / 没有权限; 登录态失效时 data.relogin = true
//	500  {code, msg, rid, time}                  系统错误: msg 固定一句话, 不带 data, 内部原因只进日志
package resp

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"time"
)

const (
	// HeaderFormat 请求头: 值为 "2" 选用新响应格式。名字刻意不叫 X-Api-Version: 账号接口已有 X-Account-Api 表示接口版本,
	// 本头只表示"响应格式版本", 两者正交。
	HeaderFormat    = "X-Resp-Format"
	HeaderRequestID = "X-Request-Id"
	// SystemMsg 是所有系统错误(500)统一的用户文案。
	SystemMsg = "服务器发生错误，请稍后重试"
)

// Class 是错误类别, 决定新格式的 HTTP 状态码。
type Class int

const (
	Business Class = iota // 400
	Auth                  // 401
	System                // 500
)

func (c Class) status() int {
	switch c {
	case Auth:
		return http.StatusUnauthorized
	case System:
		return http.StatusInternalServerError
	}
	return http.StatusBadRequest
}

// 类别规则集中在这里(两个项目共用): 默认由旧状态码推导, 再用 overrides 处理特例。
var overrides = map[string]Class{
	// 账号或密码输错是业务结果: 用户本来就没登录, 不该走"登录态失效"流程。
	"login_failed": Business,
	// 设备签名校验失败: 授权类, 但不是用户登录态的问题, 重新登录解决不了(见 reloginCodes)。
	"auth_failed": Auth,
}

// reloginCodes 是"登录态失效、客户端要清登录态并弹登录"的码(kicked 是会话被别的设备从账号移除, 客户端据此清登录态并给专门提示)。
// 已发布客户端的约定: 只有这几个才清登录态,
// forbidden(没有权限)绝不能触发登出, 否则用户点一个没权限的功能会被踢回登录页、重新登录后又被踢。
var reloginCodes = map[string]bool{"unauthorized": true, "login_required": true, "kicked": true}

// ClassFor 返回错误码在新格式里的类别。legacyStatus 是该码在旧格式下的 HTTP 状态码。
func ClassFor(code string, legacyStatus int) Class {
	if c, ok := overrides[code]; ok {
		return c
	}
	switch {
	case legacyStatus == http.StatusUnauthorized || legacyStatus == http.StatusForbidden:
		return Auth
	case legacyStatus >= 500:
		return System
	}
	return Business
}

type state struct {
	v2  bool
	rid string
}

type ridKey struct{}

// RIDFromContext 取请求编号(Middleware 注入), 日志里带上它就能把一个请求跨系统串起来。
func RIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(ridKey{}).(string)
	return s
}

// Options 配置 Middleware。
type Options struct {
	// OnPanic 在捕获到 panic 时调用(记日志 / 计数); 为空则只打一条带堆栈的 Error。
	OnPanic func(r *http.Request, rec any)
}

var ridRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

func newRID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// writer 包住 ResponseWriter: 携带本次请求的格式与 rid, 并把标准库自己写的 404/405/413(纯文本)在新格式下改成统一的 400。
// 必须保留 Flush / Hijack / Unwrap, 流式响应和连接升级都依赖它们。
type writer struct {
	http.ResponseWriter
	st      *state
	swallow bool // 标准库的响应已被改写, 吞掉它后续写的 body
}

func (w *writer) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *writer) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *writer) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("resp: 底层 ResponseWriter 不支持 Hijack")
}

func (w *writer) WriteHeader(code int) {
	if w.st.v2 && !w.swallow {
		var c string
		switch code {
		case http.StatusNotFound:
			c = "not_found"
		case http.StatusMethodNotAllowed:
			c = "method_not_allowed"
		case http.StatusRequestEntityTooLarge:
			c = "payload_too_large"
		}
		// 业务代码在新格式下走 Fail(只会写 400/401/500), 走到这里的 404/405/413 只会是标准库自己写的。
		if c != "" {
			w.swallow = true
			writeFail(w.ResponseWriter, w.st, c, msgForStd(c), nil, Business)
			return
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *writer) Write(b []byte) (int, error) {
	if w.swallow {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

func msgForStd(code string) string {
	switch code {
	case "not_found":
		return "接口不存在"
	case "method_not_allowed":
		return "请求方法不正确"
	}
	return "请求内容过大"
}

func stateOf(w http.ResponseWriter) *state {
	for w != nil {
		if rw, ok := w.(*writer); ok {
			return rw.st
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil
		}
		w = u.Unwrap()
	}
	return nil
}

// IsV2 本次请求是否要求新格式(经过 Middleware 且带了 X-Resp-Format: 2)。
func IsV2(w http.ResponseWriter) bool {
	s := stateOf(w)
	return s != nil && s.v2
}

// RID 本次请求的编号; 没经过 Middleware 返回空串。
func RID(w http.ResponseWriter) string {
	if s := stateOf(w); s != nil {
		return s.rid
	}
	return ""
}

// Middleware 放在最外层: 生成 / 沿用 rid、识别响应格式、规整标准库的 404/405/413、兜底 panic。
func Middleware(next http.Handler, opt Options) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get(HeaderRequestID)
		if !ridRe.MatchString(rid) {
			rid = newRID()
		}
		st := &state{v2: r.Header.Get(HeaderFormat) == "2", rid: rid}
		w.Header().Set(HeaderRequestID, rid)
		if st.v2 {
			w.Header().Set(HeaderFormat, "2")
		}
		ww := &writer{ResponseWriter: w, st: st}
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// ErrAbortHandler 是 net/http 约定的"静默中止这次响应"(它收到后直接关连接、不记日志), 不是故障。
			// sdk/ 里不允许 panic(见 arch_test), 所以不重新抛出, 而是自己接管连接并关掉, 效果一致。
			if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				if h, ok := w.(http.Hijacker); ok {
					if conn, _, err := h.Hijack(); err == nil {
						_ = conn.Close()
					}
				}
				return
			}
			if opt.OnPanic != nil {
				opt.OnPanic(r, rec)
			} else {
				slog.Error("panic 已捕获", "where", "http "+r.Method+" "+r.URL.Path, "panic", rec, "rid", rid, "stack", string(debug.Stack()))
			}
			if st.v2 {
				writeFail(ww.ResponseWriter, st, "internal", SystemMsg, nil, System)
				return
			}
			// 旧格式: 与各服务现有的错误体同形({code,msg,time}, 是它们字段的子集)。
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "internal", "msg": SystemMsg, "time": time.Now().UnixMilli()})
		}()
		next.ServeHTTP(ww, r.WithContext(context.WithValue(r.Context(), ridKey{}, rid)))
	})
}

type okBody struct {
	Data any    `json:"data"`
	Rid  string `json:"rid"`
	Time int64  `json:"time"`
}

type failBody struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data,omitempty"`
	Rid  string `json:"rid"`
	Time int64  `json:"time"`
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// OK 写新格式的成功响应(200)。调用方先用 IsV2 判断; 旧格式照旧写自己的。无返回内容时 data 为 {}。
func OK(w http.ResponseWriter, data any) {
	if data == nil {
		data = struct{}{}
	}
	s := stateOf(w)
	rid := ""
	if s != nil {
		rid = s.rid
	}
	write(w, http.StatusOK, okBody{Data: data, Rid: rid, Time: time.Now().UnixMilli()})
}

// Fail 写新格式的错误响应。legacyStatus 是该码在旧格式下的状态码, 用来推导类别(见 ClassFor)。
// 业务错误(400)的 data 是可选的业务详情(如缺哪些必填项); 系统错误(500)一律使用 SystemMsg 且不带 data。
func Fail(w http.ResponseWriter, code, msg string, data any, legacyStatus int) {
	s := stateOf(w)
	if s == nil {
		s = &state{v2: true, rid: newRID()}
	}
	writeFail(w, s, code, msg, data, ClassFor(code, legacyStatus))
}

func writeFail(w http.ResponseWriter, s *state, code, msg string, data any, class Class) {
	switch class {
	case System:
		if code == "" {
			code = "internal"
		}
		msg, data = SystemMsg, nil
	case Auth:
		if reloginCodes[code] {
			switch d := data.(type) {
			case nil:
				data = map[string]any{"relogin": true}
			case map[string]any:
				cp := make(map[string]any, len(d)+1)
				for k, v := range d {
					cp[k] = v
				}
				cp["relogin"] = true
				data = cp
			}
		}
	}
	write(w, class.status(), failBody{Code: code, Msg: msg, Data: data, Rid: s.rid, Time: time.Now().UnixMilli()})
}
