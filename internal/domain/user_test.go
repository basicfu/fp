package domain_test

import (
	"testing"

	"github.com/basicfu/fp/internal/domain"
)

func TestCanTransitionUserStatus(t *testing.T) {
	tests := []struct {
		from, to string
		want     bool
	}{
		{domain.UserStatusActive, domain.UserStatusFrozen, true},
		{domain.UserStatusFrozen, domain.UserStatusActive, true},
		{domain.UserStatusActive, domain.UserStatusPendingDelete, true},
		// 保护期内可撤销注销
		{domain.UserStatusPendingDelete, domain.UserStatusActive, true},
		{domain.UserStatusPendingDelete, domain.UserStatusDeleted, true},
		// 已注销是终态
		{domain.UserStatusDeleted, domain.UserStatusActive, false},
		{domain.UserStatusDeleted, domain.UserStatusFrozen, false},
		// 冻结状态不能直接跳到注销中
		{domain.UserStatusFrozen, domain.UserStatusPendingDelete, false},
		// 未知状态
		{"WHATEVER", domain.UserStatusActive, false},
		{domain.UserStatusActive, "WHATEVER", false},
	}
	for _, tt := range tests {
		if got := domain.CanTransitionUserStatus(tt.from, tt.to); got != tt.want {
			t.Errorf("CanTransitionUserStatus(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestUserCanLogin(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{domain.UserStatusActive, true},
		// 注销保护期内允许登录——登录行为本身会撤销注销申请
		{domain.UserStatusPendingDelete, true},
		{domain.UserStatusFrozen, false},
		{domain.UserStatusDeleted, false},
	}
	for _, tt := range tests {
		u := domain.User{Status: tt.status}
		if got := u.CanLogin(); got != tt.want {
			t.Errorf("status %q CanLogin() = %v, want %v", tt.status, got, tt.want)
		}
	}
}
