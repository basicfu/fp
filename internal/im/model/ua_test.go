package model

import "testing"

func TestParseUA(t *testing.T) {
	for _, tc := range []struct {
		ua     string
		os     string
		mobile bool
	}{
		{"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Mobile Safari/537.36", "android", true},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15", "ios", true},
		{"Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X)", "ios", true},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120", "windows", false},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 Safari/605.1.15", "mac", false},
		{"Mozilla/5.0 (X11; Linux x86_64) Gecko/20100101 Firefox/120", "linux", false},
		{"curl/8.4.0", "other", false},
		{"", "other", false},
	} {
		os, mobile := ParseUA(tc.ua)
		if os != tc.os || mobile != tc.mobile {
			t.Errorf("ParseUA(%q)=(%s,%v)，期望 (%s,%v)", tc.ua, os, mobile, tc.os, tc.mobile)
		}
	}
}
