package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/basicfu/fp/internal/im/hub"
)

// statsInterval 是统计日志的周期。fp-im 正常运行时只打生命周期日志,太安静就
// 看不出它在干活;每节点半小时一行(增量是这半小时的量),量小又够判断负载是否异常。
const statsInterval = 30 * time.Minute

// statsLoop 每个周期打一行本周期的增量。
func statsLoop(ctx context.Context, h *hub.Hub, log *slog.Logger, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	prev := h.StatsSnapshot()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cur := h.StatsSnapshot()
			log.Info(statsLine(cur.Sub(prev), every))
			prev = cur
		}
	}
}

// statsLine 把标签写进消息正文:日志格式只输出字段值、不输出字段名(见 logging.Setup),
// 用附加字段的话一行数字看不出哪个是哪个。
func statsLine(d hub.StatsSnapshot, every time.Duration) string {
	return fmt.Sprintf("fp-im 统计(%s):在线 %d,新连接 %d,断开 %d,握手被拒 %d,推送成功 %d,目标不在线 %d,推送失败 %d",
		every, d.Online, d.Opened, d.Closed, d.Rejected, d.PushSent, d.PushOffline, d.PushFail)
}
