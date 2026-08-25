// Package config 从环境变量加载 fp 的启动配置。
package config

import (
	"fmt"
	"os"
	"strings"
)

// Config 是 fp 进程的全部启动配置。所有字段来自环境变量，前缀 FP_。
type Config struct {
	Env         string // DEV / PROD
	HTTPAddr    string // 管理 UI 的 HTTP 监听地址
	GRPCAddr    string // SDK 的 gRPC 监听地址（计划二使用）
	PostgresURL string
	RedisURL    string
	LogLevel    string // debug / info / warn / error

	// 首次启动时创建的平台管理员。已存在同名管理员时跳过。
	BootstrapAdminUser     string
	BootstrapAdminPassword string
}

// IsProd 报告当前是否为生产环境。
func (c *Config) IsProd() bool { return strings.EqualFold(c.Env, "PROD") }

// Load 读取环境变量并校验必填项。
func Load() (*Config, error) {
	c := &Config{
		Env:                    envOr("FP_ENV", "DEV"),
		HTTPAddr:               envOr("FP_HTTP_ADDR", ":8080"),
		GRPCAddr:               envOr("FP_GRPC_ADDR", ":9090"),
		PostgresURL:            os.Getenv("FP_POSTGRES_URL"),
		RedisURL:               os.Getenv("FP_REDIS_URL"),
		LogLevel:               envOr("FP_LOG_LEVEL", "info"),
		BootstrapAdminUser:     os.Getenv("FP_BOOTSTRAP_ADMIN_USER"),
		BootstrapAdminPassword: os.Getenv("FP_BOOTSTRAP_ADMIN_PASSWORD"),
	}

	var missing []string
	if c.PostgresURL == "" {
		missing = append(missing, "FP_POSTGRES_URL")
	}
	if c.RedisURL == "" {
		missing = append(missing, "FP_REDIS_URL")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("config: 缺少必填环境变量 %s", strings.Join(missing, ", "))
	}
	return c, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
