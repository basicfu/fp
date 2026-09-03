package model

import (
	"fmt"
	"time"
)

// NewNodeID 生成 "主机标识-启动毫秒时间戳"。每次启动都是新节点，
// 这样重启后旧条目会被当成死节点过滤，而不是被误认为仍然有效。
func NewNodeID(host string, now time.Time) string {
	return fmt.Sprintf("%s-%d", host, now.UnixMilli())
}
