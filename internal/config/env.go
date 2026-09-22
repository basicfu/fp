package config

import (
	"fmt"
	"os"
)

// RequireEnv 读取 name 指向的环境变量，为空时返回一个点明是哪个变量缺失
// 的错误。postgres/redis 连接串必须来自环境变量而不是这个包解析的
// YAML——读系统配置表本身就要先连上数据库，见
// docs/superpowers/specs/2026-09-21-fp-system-config-design.md 第三节。
func RequireEnv(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("config: 环境变量 %s 未设置", name)
	}
	return v, nil
}

// EnvOr 读取 name 指向的环境变量，为空时返回 def。用于像 FP_ENV 这种
// 有合理默认值、缺失不该拦住启动的环境变量——与必须显式设置的
// RequireEnv 是两种不同的严格程度。
func EnvOr(name, def string) string {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	return v
}
