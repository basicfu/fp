package model

import "encoding/json"

// ws 关闭码。4001–4005 是 spec 定义的自定义码，1013 是标准 "try again later"。
// 关闭码是对外契约的一部分：client SDK 是后续任务才写，这里把每个码
// 对应的客户端动作定下来，避免实现时各自理解、行为不一致。
const (
	// CloseAuthFailed：token 无效或已被撤销。客户端动作：不要自动重连，
	// 交给调用方去重新登录换一个新 token，拿着旧 token 立刻重连只会
	// 再次被拒，白白重试。
	CloseAuthFailed = 4001
	// ClosePolicyRejected：被这个 app 的连接策略拒绝（reject 策略下已有
	// 连接、或 limit 策略下连接数已满）。客户端动作：不要重试——策略
	// 状态在服务端，不重新登录、不换 token 也不会变，立即重连大概率
	// 拿到同样的拒绝，只会造成无意义的重连风暴。
	ClosePolicyRejected = 4002
	// CloseKicked：被顶替或被业务方主动踢下线。客户端动作：由业务决定
	// ——这条连接本身没有错，是否重连、要不要提示用户，属于业务语义，
	// SDK 不替业务做这个决定。
	CloseKicked = 4003
	// CloseUnavailable：握手依赖的后端（身份服务或 Redis 注册表）暂时
	// 不可用。客户端动作：和 client 自身无关，退避后重连即可，不需要
	// 重新登录也不需要业务介入。
	CloseUnavailable = 4004
	// CloseIdleTimeout 是网关因为连接空闲太久主动关闭时用的码。不复用
	// CloseBackpressure(1013)：背压是"发送队列跟不上、client 应该拉历史
	// 补漏"，空闲超时是"这条连接太久没有任何帧、网关主动清理"，两者对
	// client SDK 该做什么（是否退避、是否需要补历史）含义不同，混用会让
	// SDK 没法区分。也不复用 CloseAuthFailed 等 4001-4004：那几个都已经
	// 有明确的、不是"空闲"的语义。
	//
	// 客户端动作：立即重连，不要退避。空闲超时只说明这条连接长时间没有
	// 流量，是网关的常规清理，服务端一切正常；client 想继续用就该马上
	// 连回来，没有理由像遇到真正故障（1013）那样退避。
	CloseIdleTimeout = 4005
	// CloseBackpressure：发送队列跟不上，网关主动断开避免无限堆积。
	// 客户端动作：退避后重连，重连后向业务方拉一次历史补上断连期间可能
	// 错过的消息——这条关闭码本身就意味着有数据没送达。
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
	// ReasonShutdown：本节点正在优雅关闭（发版、缩容），主动断开这条连接。
	// 与 ReasonClient/ReasonTimeout 区分开是有意的：业务方看到它就知道
	// 这次下线与用户行为和这条连接本身都无关，client 会退避后重连回来
	// （关闭码是 CloseUnavailable），不该据此清理用户的业务状态。
	ReasonShutdown = "shutdown"
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
