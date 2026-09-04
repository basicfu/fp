package model

import (
	"testing"
	"time"
)

// TestNewNodeIDDifferentPIDSameMillisecondAreDistinct 守住修复批次 2
// 问题二：同一台主机、同一毫秒启动的两个进程必须拿到不同的节点标识，
// 不能像原来那样只靠"主机名+毫秒时间戳"，那样两个进程会算出完全相同的
// 标识，进而在服务表/存活表里互相覆盖对方的字段。
//
// 回退验证：把 NewNodeID 临时改回不带 pid 的 fmt.Sprintf("%s-%d", host,
// now.UnixMilli())，本测试按预期变红（a == b）；改回带 pid 的版本后
// 复测转绿。过程记在 fix-batch-2-report.md。
func TestNewNodeIDDifferentPIDSameMillisecondAreDistinct(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	a := NewNodeID("host1", 100, now)
	b := NewNodeID("host1", 200, now)
	if a == b {
		t.Fatalf("同一主机、同一毫秒但进程号不同（100 vs 200），节点标识不应相同，实际都是 %q", a)
	}
}

// TestNewNodeIDDifferentMillisecondSamePIDAreDistinct 守住 pid 复用场景：
// 同一个进程（同一个 pid）在不同毫秒先后启动（例如同一个 pid 被操作系统
// 回收后又分配给一个后来重启的新进程），节点标识必须不同——这是时间戳
// 那一段仍然要保留的理由：只拼 pid 不够，pid 会被复用。
//
// 回退验证：把 NewNodeID 临时改成只拼主机名和 pid、不带时间戳
// （fmt.Sprintf("%s-%d", host, pid)），本测试按预期变红（a == b，因为
// 两次调用的 pid 相同）；改回带时间戳的版本后复测转绿。过程记在
// fix-batch-2-report.md。
func TestNewNodeIDDifferentMillisecondSamePIDAreDistinct(t *testing.T) {
	a := NewNodeID("host1", 100, time.UnixMilli(1_700_000_000_000))
	b := NewNodeID("host1", 100, time.UnixMilli(1_700_000_000_001))
	if a == b {
		t.Fatalf("同一进程（pid 100）但启动毫秒不同，节点标识不应相同，实际都是 %q", a)
	}
}
