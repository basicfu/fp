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
