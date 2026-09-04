package model

import "encoding/json"

// ws 关闭码。4001–4005 是 spec 定义的自定义码，1013 是标准 "try again later"。
const (
	CloseAuthFailed     = 4001
	ClosePolicyRejected = 4002
	CloseKicked         = 4003
	CloseUnavailable    = 4004
	// CloseIdleTimeout 是网关因为连接空闲太久主动关闭时用的码。不复用
	// CloseBackpressure(1013)：背压是"发送队列跟不上、client 应该拉历史
	// 补漏"，空闲超时是"这条连接太久没有任何帧、网关主动清理"，两者对
	// client SDK 该做什么（是否退避、是否需要补历史）含义不同，混用会让
	// SDK 没法区分。也不复用 CloseAuthFailed 等 4001-4004：那几个都已经
	// 有明确的、不是"空闲"的语义。
	CloseIdleTimeout  = 4005
	CloseBackpressure = 1013
)

// 断开原因，进 Disconnected 事件的 reason 字段。
const (
	ReasonClient       = "client"
	ReasonTimeout      = "timeout"
	ReasonKicked       = "kicked"
	ReasonReplaced     = "replaced"
	ReasonRevoked      = "revoked"
	ReasonBackpressure = "backpressure"
)

// 帧类型。
const (
	FrameAuth  = "auth"
	FrameHello = "hello"
	FrameMsg   = "msg"
	FramePing  = "ping"
	FramePong  = "pong"
)

// AuthFrame 是 client 连接后的第一帧。App 必须给：fp 的 token 是不透明的，
// im 要先知道用哪个 app 的凭据去 fp 验它。
type AuthFrame struct {
	T      string `json:"t"`
	App    string `json:"app"`
	Token  string `json:"token,omitempty"`
	Guest  string `json:"guest,omitempty"`
	UA     string `json:"ua,omitempty"`
	OS     string `json:"os,omitempty"`
	Mobile *bool  `json:"mobile,omitempty"`
}

// Frame 是握手之后的通用帧。P 对 im 是不透明的 JSON 原样值。
type Frame struct {
	T    string          `json:"t"`
	Conn string          `json:"conn,omitempty"`
	P    json.RawMessage `json:"p,omitempty"`
}
