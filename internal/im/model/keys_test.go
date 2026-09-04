package model

import "testing"

func TestKeys(t *testing.T) {
	if got := ConnKey("a1", "u:1001"); got != "fp:im:{a1:u:1001}:conn" {
		t.Fatalf("ConnKey=%q", got)
	}
	if got := SrvKey("a1"); got != "fp:im:srv:a1" {
		t.Fatalf("SrvKey=%q", got)
	}
	if got := NodeChannel("im-a"); got != "fp:im:node:im-a" {
		t.Fatalf("NodeChannel=%q", got)
	}
	if KeyNodes != "fp:im:node" {
		t.Fatalf("KeyNodes=%q", KeyNodes)
	}
}
