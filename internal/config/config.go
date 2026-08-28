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

	// 阿里云短信是生产环境唯一的短信供应商（见 cmd/fp/main.go）。
	// AliyunEndpoint 可以留空，notify.NewAliyunSMS 会套用它自己的默认接入点；
	// 其余四项没有安全的默认值，缺任何一项都会在下面的 Load 里让启动失败——
	// 不能悄悄退化成 notify.NewFakeProvider，那样验证码只会进内存，
	// 谁也收不到短信，而且不会有任何报错。
	AliyunAccessKeyID          string
	AliyunAccessKeySecret      string
	AliyunEndpoint             string
	AliyunSMSSignName          string
	AliyunSMSTemplateLoginCode string // 映射 service.LoginCodeTemplate 到阿里云侧模板 ID
}

// IsProd 报告当前是否为生产环境。
func (c *Config) IsProd() bool { return strings.EqualFold(c.Env, "PROD") }

// Load 读取环境变量并校验必填项。
func Load() (*Config, error) {
	c := &Config{
		Env:                        envOr("FP_ENV", "DEV"),
		HTTPAddr:                   envOr("FP_HTTP_ADDR", ":8080"),
		GRPCAddr:                   envOr("FP_GRPC_ADDR", ":9090"),
		PostgresURL:                os.Getenv("FP_POSTGRES_URL"),
		RedisURL:                   os.Getenv("FP_REDIS_URL"),
		LogLevel:                   envOr("FP_LOG_LEVEL", "info"),
		BootstrapAdminUser:         os.Getenv("FP_BOOTSTRAP_ADMIN_USER"),
		BootstrapAdminPassword:     os.Getenv("FP_BOOTSTRAP_ADMIN_PASSWORD"),
		AliyunAccessKeyID:          os.Getenv("FP_ALIYUN_ACCESS_KEY_ID"),
		AliyunAccessKeySecret:      os.Getenv("FP_ALIYUN_ACCESS_KEY_SECRET"),
		AliyunEndpoint:             os.Getenv("FP_ALIYUN_ENDPOINT"),
		AliyunSMSSignName:          os.Getenv("FP_ALIYUN_SMS_SIGN_NAME"),
		AliyunSMSTemplateLoginCode: os.Getenv("FP_ALIYUN_SMS_TEMPLATE_LOGIN_CODE"),
	}

	var missing []string
	if c.PostgresURL == "" {
		missing = append(missing, "FP_POSTGRES_URL")
	}
	if c.RedisURL == "" {
		missing = append(missing, "FP_REDIS_URL")
	}
	if c.AliyunAccessKeyID == "" {
		missing = append(missing, "FP_ALIYUN_ACCESS_KEY_ID")
	}
	if c.AliyunAccessKeySecret == "" {
		missing = append(missing, "FP_ALIYUN_ACCESS_KEY_SECRET")
	}
	if c.AliyunSMSSignName == "" {
		missing = append(missing, "FP_ALIYUN_SMS_SIGN_NAME")
	}
	if c.AliyunSMSTemplateLoginCode == "" {
		missing = append(missing, "FP_ALIYUN_SMS_TEMPLATE_LOGIN_CODE")
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
