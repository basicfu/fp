package model

import "strings"

// ParseUA 只提取两个值：操作系统族与是否移动端。
// 不引入 UA 解析库：我们只需要六个取值，子串匹配足够，而且库的更新节奏不由我们控制。
// 顺序有讲究：iPhone 的 UA 里含 "like Mac OS X"，Android 的 UA 里含 "Linux"，
// 所以先判移动端再判桌面端。
func ParseUA(ua string) (os string, mobile bool) {
	switch {
	case strings.Contains(ua, "Android"):
		return "android", true
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"), strings.Contains(ua, "iPod"):
		return "ios", true
	case strings.Contains(ua, "Windows"):
		return "windows", strings.Contains(ua, "Mobile")
	case strings.Contains(ua, "Mac OS X"), strings.Contains(ua, "Macintosh"):
		return "mac", false
	case strings.Contains(ua, "Linux"), strings.Contains(ua, "X11"):
		return "linux", strings.Contains(ua, "Mobile")
	}
	return "other", strings.Contains(ua, "Mobile")
}
