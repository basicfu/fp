// Package config 从 YAML 文件加载 fp 的启动配置。
//
// 字段树与 config.yaml 的字段树 1:1 同构，中间没有映射层——加了字段却忘了
// 映射是这类加载器最常见的漏，同构就没有这个漏可犯。代价是每个字段都必须
// 显式写 yaml tag：yaml.v3 的默认规则是把字段名整个小写，BootstrapAdmin 会
// 变成 bootstrapadmin 而不是 bootstrap_admin。
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultPath 是 -c 未指定时读的配置文件。
const DefaultPath = "config.yaml"

// Config 是 fp 进程的全部启动配置。
type Config struct {
	Env            string         `yaml:"env"` // dev / prod，大小写不敏感
	Log            Log            `yaml:"log"`
	HTTP           Listen         `yaml:"http"` // 管理控制台
	GRPC           Listen         `yaml:"grpc"` // SDK 接入
	Postgres       Endpoint       `yaml:"postgres"`
	Redis          Endpoint       `yaml:"redis"`
	BootstrapAdmin BootstrapAdmin `yaml:"bootstrap_admin"`
	SMS            SMS            `yaml:"sms"`
}

type Log struct {
	Level string `yaml:"level"` // debug / info / warn / error
}

type Listen struct {
	Addr string `yaml:"addr"`
}

type Endpoint struct {
	URL string `yaml:"url"`
}

// BootstrapAdmin 是首次启动时创建的平台管理员。两项都填才生效——
// AdminService.EnsureBootstrap 在任一为空时直接跳过。
type BootstrapAdmin struct {
	User     string `yaml:"user"`
	Password string `yaml:"password"`
}

type SMS struct {
	Aliyun Aliyun `yaml:"aliyun"`
}

// Aliyun 是阿里云短信供应商的配置（见 cmd/fp/main.go）。
//
// 只在生产环境（IsProd）强制要求前四项非空——notify.FakeProvider 的文档
// 注释本身就写着"用于测试与本地开发"，逼所有人在本机跑 ./scripts/run.sh
// 或 CI 跑 cmd/fp 二进制都先备齐（哪怕是假的）阿里云凭据，是把一条只该管
// 生产的约束错误地套到了所有环境头上。非生产环境缺任何一项时，
// cmd/fp/main.go 会退化成 notify.NewLoggingFakeProvider 并打一条醒目的
// WARN——不静默，只是不强制。四项在非生产环境下也齐全时仍然装配真实供应商，
// 方便有人就是想在本机联调真实短信通道。
//
// Endpoint 任何环境下都可以留空，notify.NewAliyunSMS 会套用它自己的默认
// 接入点，不参与必填校验。
type Aliyun struct {
	AccessKeyID       string `yaml:"access_key_id"`
	AccessKeySecret   string `yaml:"access_key_secret"`
	SignName          string `yaml:"sign_name"`
	TemplateLoginCode string `yaml:"template_login_code"` // 映射 service.LoginCodeTemplate 到阿里云侧模板 ID
	Endpoint          string `yaml:"endpoint"`
}

// IsProd 报告当前是否为生产环境。
func (c *Config) IsProd() bool { return strings.EqualFold(c.Env, "prod") }

// Load 读 path 指向的 YAML 文件，填默认值并校验必填项。
func Load(path string) (*Config, error) {
	// 先填默认值再解码：yaml.v3 只写文档里出现过的字段，没出现的原样保留，
	// 于是"默认值"这件事不需要任何额外的 applyDefaults 逻辑。
	c := &Config{
		Env:  "dev",
		Log:  Log{Level: "info"},
		HTTP: Listen{Addr: ":8080"},
		GRPC: Listen{Addr: ":9090"},
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: 打开配置文件 %s: %w", path, err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	// 拼错的键当场报错，而不是静默回落到默认值。这是从环境变量迁到文件
	// 净新增的能力：env 那个介质压根没有"这个键我不认识"的概念。
	// 与 internal/httpapi 的 decodeJSON 开 DisallowUnknownFields 同一条纪律。
	dec.KnownFields(true)
	// 空文件（或整份只有注释）解码返回 io.EOF，不是错误——它只是"什么都
	// 没覆盖"，默认值原样留着，随后必填校验会告诉调用方到底缺什么。
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config: 解析 %s: %w", path, err)
	}

	var missing []string
	if c.Postgres.URL == "" {
		missing = append(missing, "postgres.url")
	}
	if c.Redis.URL == "" {
		missing = append(missing, "redis.url")
	}
	// 阿里云短信凭据只在生产环境强制必填，理由见 Aliyun 上方的注释。
	if c.IsProd() {
		for _, kv := range []struct{ path, v string }{
			{"sms.aliyun.access_key_id", c.SMS.Aliyun.AccessKeyID},
			{"sms.aliyun.access_key_secret", c.SMS.Aliyun.AccessKeySecret},
			{"sms.aliyun.sign_name", c.SMS.Aliyun.SignName},
			{"sms.aliyun.template_login_code", c.SMS.Aliyun.TemplateLoginCode},
		} {
			if kv.v == "" {
				missing = append(missing, kv.path)
			}
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("config: %s 缺少必填项 %s", path, strings.Join(missing, ", "))
	}
	return c, nil
}
