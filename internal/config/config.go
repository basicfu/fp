// Package config 解析 fp 的系统配置——存在数据库 system_config 表里的
// YAML 原文，字段树与 YAML 的字段树 1:1 同构，中间没有映射层——加了字段
// 却忘了映射是这类加载器最常见的漏，同构就没有这个漏可犯。代价是每个
// 字段都必须显式写 yaml tag：yaml.v3 的默认规则是把字段名整个小写，
// BootstrapAdmin 会变成 bootstrapadmin 而不是 bootstrap_admin。
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config 是 fp 进程的系统配置（不含 postgres/redis/env——这三项必须来自
// 环境变量 FP_POSTGRES_URL/FP_REDIS_URL/FP_ENV，读系统配置本身就要先连上
// 数据库，见 docs/superpowers/specs/2026-09-21-fp-system-config-design.md
// 第三节）。
type Config struct {
	// Env 不从 YAML 读（yaml:"-"）：由 Parse 的 env 参数直接赋值，来源是
	// FP_ENV 环境变量。这里如果还留一个 yaml:"env" 的口子，会出现两个
	// 都能改 env 的入口、谁说了算说不清楚；系统配置 YAML 里如果还写着
	// env:，KnownFields(true) 会把它当未知键报错，逼着改成 FP_ENV。
	Env            string         `yaml:"-"`
	Log            Log            `yaml:"log"`
	HTTP           Listen         `yaml:"http"` // 管理控制台
	GRPC           Listen         `yaml:"grpc"` // SDK 接入
	BootstrapAdmin BootstrapAdmin `yaml:"bootstrap_admin"`
	SMS            SMS            `yaml:"sms"`
}

type Log struct {
	Level string `yaml:"level"` // debug / info / warn / error
}

type Listen struct {
	Addr string `yaml:"addr"`
}

// BootstrapAdmin 是首次启动时创建的平台管理员。两项都填才生效——
// AdminService.EnsureBootstrap 在任一为空时直接跳过；cmd/fp/main.go 在
// 两项都为空时会落到内置默认值 admin/admin，见该文件里的
// resolveBootstrapAdmin。
type BootstrapAdmin struct {
	User     string `yaml:"user"`
	Password string `yaml:"password"`
}

type SMS struct {
	Aliyun Aliyun `yaml:"aliyun"`
}

// Aliyun 是阿里云短信供应商的配置（见 cmd/fp/main.go）。不强制必填——
// 任何环境缺任一项时，cmd/fp/main.go 都会退化成
// notify.NewLoggingFakeProvider 并打一条醒目的 WARN，不静默、但也不拦住
// 启动。阿里云短信只是短信这一种通知渠道的其中一个供应商，后续会挪进
// 统一的通知中心配置，不该由 fp 自己的系统配置在启动时强制卡它。
//
// Endpoint 任何环境下都可以留空，notify.NewAliyunSMS 会套用它自己的默认
// 接入点。
type Aliyun struct {
	AccessKeyID       string `yaml:"access_key_id"`
	AccessKeySecret   string `yaml:"access_key_secret"`
	SignName          string `yaml:"sign_name"`
	TemplateLoginCode string `yaml:"template_login_code"` // 映射 service.LoginCodeTemplate 到阿里云侧模板 ID
	Endpoint          string `yaml:"endpoint"`
}

// IsProd 报告当前是否为生产环境。
func (c *Config) IsProd() bool { return strings.EqualFold(c.Env, "prod") }

// Parse 解析系统配置表里存的 YAML 原文，填默认值并校验必填项。yamlText
// 为空（系统配置表还没有任何版本、或者存过一份空文本）按"什么都没覆盖"
// 处理，返回全部默认值——不是错误：首次启动系统配置表必然是空的，这是
// 正常状态。
//
// env 来自 FP_ENV 环境变量（调用方用 config.EnvOr("FP_ENV", "DEV") 取，
// 空串时那个函数已经落到 "DEV"），这里再兜一层空串保护纯粹是防御性的，
// 不指望真的用到。
func Parse(yamlText string, env string) (*Config, error) {
	if env == "" {
		env = "DEV"
	}
	// 先填默认值再解码：yaml.v3 只写文档里出现过的字段，没出现的原样
	// 保留，于是"默认值"这件事不需要任何额外的 applyDefaults 逻辑。
	c := &Config{
		Env:  env,
		Log:  Log{Level: "info"},
		HTTP: Listen{Addr: ":8080"},
		GRPC: Listen{Addr: ":9090"},
	}

	dec := yaml.NewDecoder(strings.NewReader(yamlText))
	// 拼错的键当场报错，不静默回落到默认值——与 internal/httpapi 的
	// decodeJSON 开 DisallowUnknownFields 同一条纪律。
	dec.KnownFields(true)
	// 空文本解码返回 io.EOF，不是错误——它只是"什么都没覆盖"。
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config: 解析系统配置: %w", err)
	}
	return c, nil
}

// ToYAML 把 c 序列化成系统配置表能存的 YAML 原文。用于系统配置表还没有
// 任何版本时，把首次启动解析出来的默认值（含 cmd/fp/main.go 里
// resolveBootstrapAdmin 兜底过的 admin/admin）整份写回数据库，让「系统
// 配置」页面第一次打开就是一份能直接改的真实内容，而不是一个空编辑框
// 加一段前端 placeholder 提示。
//
// Env 不会出现在输出里（yaml:"-"）——它只能来自 FP_ENV，这里如果把它也
// 写回 YAML，会让人误以为改这个 YAML 里的 env 字段有用。
//
// 缩进用 2 格：yaml.v3 的 Marshal 默认是 4 格，跟这个仓库里其余手写
// YAML（config.example.yaml 时代、前端占位符）的习惯不一致。
func ToYAML(c *Config) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return "", fmt.Errorf("config: 序列化默认配置: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("config: 序列化默认配置: %w", err)
	}
	return buf.String(), nil
}
