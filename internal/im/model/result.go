package model

import "time"

// Result 是鉴权结果帧（t=2）的 p，也是业务方鉴权回调的响应体。fp-im 与
// 业务方共用这一个结构，client 只需按 code 判断成败，不必区分是谁拒的。
type Result struct {
	Code   string `json:"code"`
	Msg    string `json:"msg"`
	Detail string `json:"detail,omitempty"`
	// Time 是服务端当前 Unix 毫秒；成功也带，client 每次连上都能顺手校时。
	Time int64 `json:"time"`
}

// 鉴权结果码。CodeSuccess 以外都是失败；业务方可以返回这里没有的码，
// fp-im 原样转发不解读。
const (
	CodeSuccess         = "success"
	CodeBadRequest      = "bad_request"
	CodeAuthFailed      = "auth_failed"
	CodeAuthUnavailable = "auth_unavailable"
	CodeAppUnavailable  = "app_unavailable"
	CodeConnLimited     = "conn_limited"
	CodeRateLimited     = "rate_limited"
)

// NewResult 生成 fp-im 自己给出的结果，time 取当前时间。
func NewResult(code, msg string) Result {
	return Result{Code: code, Msg: msg, Time: time.Now().UnixMilli()}
}
