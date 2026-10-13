package domain_test

import (
	"errors"
	"strings"
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

func TestNormalizeNickname(t *testing.T) {
	valid := map[string]string{
		"小明":                          "小明",
		"  小明\t":                      "小明",
		"a":                           "a",
		"Tom Hanks":                   "Tom Hanks",
		"😀":                           "😀",
		strings.Repeat("字", 32):       strings.Repeat("字", 32),
		" " + strings.Repeat("x", 32): strings.Repeat("x", 32),
	}
	for in, want := range valid {
		got, err := domain.NormalizeNickname(in)
		if err != nil || got != want {
			t.Errorf("NormalizeNickname(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	invalid := []string{
		"",
		"   ",
		"\n",
		strings.Repeat("字", 33),
		"张三\n李四",
		"张三\r李四",
		"tab\tin",
		"zero\x00byte",
		"esc\x1b[31m",
		"del\x7f",
		"行分隔\u2028符",
		"段分隔\u2029符",
		"坏编码\xff",
	}
	for _, in := range invalid {
		_, err := domain.NormalizeNickname(in)
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodeNicknameInvalid {
			t.Errorf("NormalizeNickname(%q) err = %v, want NICKNAME_INVALID", in, err)
		}
	}
}

func TestValidGender(t *testing.T) {
	for g, want := range map[string]bool{
		"": true, domain.GenderMale: true, domain.GenderFemale: true,
		"male": false, "M": false, "UNKNOWN": false, " MALE": false,
	} {
		if got := domain.ValidGender(g); got != want {
			t.Errorf("ValidGender(%q) = %v, want %v", g, got, want)
		}
	}
}
