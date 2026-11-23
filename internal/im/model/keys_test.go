package model

import "testing"

func TestKeys(t *testing.T) {
	if got := ConnKey("a1", "u:1001"); got != "fp:im:{a1:u:1001}:conn" {
		t.Fatalf("ConnKey=%q", got)
	}
	// 以下三个键是生产上 fp-im 已在用的，一个字都不能变。
	if got := ClusterKeys().Srv("a1"); got != "fp:im:srv:a1" {
		t.Fatalf("Srv=%q", got)
	}
	if got := ClusterKeys().NodeChannel("im-a"); got != "fp:im:node:im-a" {
		t.Fatalf("NodeChannel=%q", got)
	}
	if got := GroupIdxKey("a1", "r", "n1"); got != "fp:im:{a1:grp:r:n1}:idx" {
		t.Fatalf("GroupIdxKey=%q", got)
	}
	if got := ClusterKeys().Nodes(); got != "fp:im:node" {
		t.Fatalf("Nodes=%q", got)
	}
}
