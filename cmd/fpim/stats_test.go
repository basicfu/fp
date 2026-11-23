package main

import (
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/hub"
)

func TestStatsLine(t *testing.T) {
	got := statsLine(hub.StatsSnapshot{Online: 120, Opened: 8, Closed: 5, Rejected: 2, PushSent: 300, PushOffline: 4, PushFail: 1}, 5*time.Minute)
	want := "fp-im 统计(5m0s):在线 120,新连接 8,断开 5,握手被拒 2,推送成功 300,目标不在线 4,推送失败 1"
	if got != want {
		t.Fatalf("statsLine =\n%q\nwant\n%q", got, want)
	}
}
