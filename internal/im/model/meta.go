package model

import "encoding/json"

// ConnMeta 是 conn hash 里每个 field 的 value。不存 UA 原文：
// 十万条连接每条几百字节的 UA 会把 Redis 内存花在没人读的东西上。
type ConnMeta struct {
	Node   string `json:"n"`
	OS     string `json:"os"`
	Mobile bool   `json:"m"`
	At     int64  `json:"ts"`
}

func (m ConnMeta) Encode() string {
	b, _ := json.Marshal(m) // 全是标量字段，Marshal 不会失败
	return string(b)
}

func DecodeMeta(s string) (ConnMeta, error) {
	var m ConnMeta
	err := json.Unmarshal([]byte(s), &m)
	return m, err
}

// EventBody 是 Connected/Disconnected 事件经节点频道转发时的 payload。
type EventBody struct {
	Kind   string `json:"k"`
	OS     string `json:"os"`
	Mobile bool   `json:"m"`
	UA     string `json:"ua,omitempty"`
	Reason string `json:"r,omitempty"`
	At     int64  `json:"ts"`
}

const (
	EventConnected    = "connected"
	EventDisconnected = "disconnected"
)
