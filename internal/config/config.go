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
	"net/netip"
	"net/url"
	"strings"
	"time"

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
	// PublicURL 是 fp 对公网的地址（如 https://fp.example.com），三方扫码登录用它拼回调地址。
	// 微信开放平台的「授权回调域」要填它的域名。不用扫码登录可以不填。
	PublicURL string `yaml:"public_url"`
	// AccountDeletion 是用户注销的参数。
	AccountDeletion AccountDeletion `yaml:"account_deletion"`
	// VerifyCode 是业务方给已登录用户发验证码（换绑、设密码前的身份校验）的参数。
	VerifyCode VerifyCode `yaml:"verify_code"`
	// OSS 是对象存储（头像等用户上传的文件）。Bucket 留空表示没配，依赖它的接口返回不可用。
	OSS OSS `yaml:"oss"`
	// AccountAPI 是终端用户账号接口（/api/account）的参数。
	AccountAPI AccountAPI `yaml:"account_api"`
	// LegacySMS 只为兼容：Parse 开着 KnownFields(true)，而存量系统配置的第一版是
	// seedDefaultsYAML 把默认值整份写进去的，里面带着 sms: 段。直接删字段会让这些
	// 库启动时解析失败；版本快照又不允许改写。读进来、丢掉；omitempty 保证 ToYAML
	// 不再输出它。
	LegacySMS any `yaml:"sms,omitempty"`
}

// OSS 是阿里云对象存储的接入参数。
type OSS struct {
	AccessKeyID     string `yaml:"access_key_id"`
	AccessKeySecret string `yaml:"access_key_secret"`
	Bucket          string `yaml:"bucket"`
	Endpoint        string `yaml:"endpoint"`
	// FileURL 是文件对外地址前缀（通常是 CDN 域名）；留空用 https://{bucket}.{endpoint}。
	FileURL string `yaml:"file_url"`
}

// AccountAPI 是终端用户账号接口的参数。
type AccountAPI struct {
	// TrustedProxies 是网关的地址（单个 IP 或 CIDR）。只有从这些地址直连来的请求才认 X-Forwarded-For，
	// 否则客户端自己写一个头就能冒充任意 IP。填了之后不是从这些地址来的账号接口请求一律 404，
	// 免得绕过网关直连 fp、随意填 Host 冒充应用的域名。留空表示一律用直连地址、不拦来源——fp 在网关后面时要填。
	TrustedProxies []string `yaml:"trusted_proxies"`

	proxies []netip.Prefix
}

// Proxies 返回解析好的 TrustedProxies（Parse 已校验过）。
func (a AccountAPI) Proxies() []netip.Prefix { return a.proxies }

func parseProxies(in []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(in))
	for _, raw := range in {
		raw = strings.TrimSpace(raw)
		if p, err := netip.ParsePrefix(raw); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(raw)
		if err != nil {
			return nil, fmt.Errorf("config: account_api.trusted_proxies 里的 %q 不是 IP 或 CIDR", raw)
		}
		out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
	}
	return out, nil
}

type Log struct {
	Level string `yaml:"level"` // debug / info / warn / error
}

// AccountDeletion 是用户注销的参数。
type AccountDeletion struct {
	// CoolingOffDays 是冷静期天数：提交注销后这么多天内用该账号登录即撤销注销，期满永久删除。
	// 隐私政策承诺"15 个工作日内处理"，默认 7 天落在承诺之内；调长之前先核对对外承诺。
	CoolingOffDays int `yaml:"cooling_off_days"`
}

// VerifyCode 是发验证码的参数。短信模板固定用 verify_code（与登录共用），邮件模板可配。
type VerifyCode struct {
	// EmailTemplate 是邮件验证码用的通知模板 code，控制台「通知中心」里要有一条同名的 email 模板，变量只有 code。
	EmailTemplate string `yaml:"email_template"`
	// RateLimit 是发码频率限制，按收件人（手机号 / 邮箱）计数。登录、验身份、换绑、设密码共用同一份计数，
	// 发送失败也计数：供应商超时但其实已送达的情况不少见，退还额度会让重试绕过上限。
	RateLimit VerifyRateLimit `yaml:"rate_limit"`
}

// VerifyRateLimit 按渠道分开配：短信按条计费、邮件便宜，松紧可以不同。每个渠道至少一条规则，
// 全部规则都满足才发；不想限制就把 limit 调大，没有"关掉"这一档——验证码接口不限流就能被拿来刷账单、骚扰别人。
type VerifyRateLimit struct {
	SMS   []RateRule `yaml:"sms"`
	Email []RateRule `yaml:"email"`
}

// RateRule 是一条"window 内最多 limit 条"。window 写 Go 时长格式，如 30s、1h、24h。
type RateRule struct {
	Window time.Duration `yaml:"window"`
	Limit  int           `yaml:"limit"`
}

// DefaultVerifyRateRules 是 verify_code.rate_limit 两个渠道的默认值：30 秒 1 条、1 小时 5 条、1 天 10 条。
func DefaultVerifyRateRules() []RateRule {
	return []RateRule{{Window: 30 * time.Second, Limit: 1}, {Window: time.Hour, Limit: 5}, {Window: 24 * time.Hour, Limit: 10}}
}

// DefaultVerifyEmailTemplate 是 verify_code.email_template 的默认值。
//
// 生产用阿里云邮件推送（DirectMail）的 SMTP 发信，fp 无需专门的阿里云邮件类型：DirectMail 的 SMTP
// 不要求预审模板，正文由 fp 渲染，改文案不用找阿里云审核（短信则必须用审核过的模板）。已实测通过的配置：
//   - 阿里云：发信域名配好 SPF / DKIM / MX（不过验证会退信或进垃圾箱），发信地址类型选「触发邮件」并设 SMTP 密码；
//   - 供应商 smtp：host=smtpdm.aliyun.com（杭州，新加坡为 smtpdm-ap-southeast-1.aliyun.com）、port=465、tls=ssl，
//     username 与 from 都是发信地址全称——MAIL FROM 与发信地址不符会被拒；ECS 封了 25 端口，所以用 465；
//   - 模板：email 渠道、自定义内容模式（smtp 发不了供应商模板），变量只有 code，多了少了都发码失败。
const DefaultVerifyEmailTemplate = "verify_code_email"

// DefaultCoolingOffDays 是 account_deletion.cooling_off_days 的默认值。
const DefaultCoolingOffDays = 7

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

		AccountDeletion: AccountDeletion{CoolingOffDays: DefaultCoolingOffDays},
		VerifyCode: VerifyCode{EmailTemplate: DefaultVerifyEmailTemplate,
			RateLimit: VerifyRateLimit{SMS: DefaultVerifyRateRules(), Email: DefaultVerifyRateRules()}},
	}

	dec := yaml.NewDecoder(strings.NewReader(yamlText))
	// 拼错的键当场报错，不静默回落到默认值——与 internal/httpapi 的
	// decodeJSON 开 DisallowUnknownFields 同一条纪律。
	dec.KnownFields(true)
	// 空文本解码返回 io.EOF，不是错误——它只是"什么都没覆盖"。
	var err error
	if err = dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config: 解析系统配置: %w", err)
	}
	// 旧 sms 段读进来只是为了过 KnownFields；不丢掉的话，ToYAML 会把其中
	// 的 access_key_secret 原样写回库里。
	c.LegacySMS = nil
	if c.PublicURL, err = normalizePublicURL(c.PublicURL); err != nil {
		return nil, err
	}
	// 0 天意味着提交即删、登录撤销的口子形同虚设，按写错处理而不是照办。
	if c.AccountDeletion.CoolingOffDays < 1 {
		return nil, fmt.Errorf("config: account_deletion.cooling_off_days 至少为 1，收到 %d", c.AccountDeletion.CoolingOffDays)
	}
	if c.AccountAPI.proxies, err = parseProxies(c.AccountAPI.TrustedProxies); err != nil {
		return nil, err
	}
	// 显式写成空串等同没配：模板 code 为空时 Send 只会回"模板不存在"，不如落回默认值。
	if c.VerifyCode.EmailTemplate = strings.TrimSpace(c.VerifyCode.EmailTemplate); c.VerifyCode.EmailTemplate == "" {
		c.VerifyCode.EmailTemplate = DefaultVerifyEmailTemplate
	}
	if err := checkRateRules("sms", c.VerifyCode.RateLimit.SMS); err != nil {
		return nil, err
	}
	if err := checkRateRules("email", c.VerifyCode.RateLimit.Email); err != nil {
		return nil, err
	}
	return c, nil
}

// checkRateRules:空列表等于不限流，按写错处理;同一个时长写两遍会共用一个计数键，也按写错处理。
func checkRateRules(channel string, rules []RateRule) error {
	if len(rules) == 0 {
		return fmt.Errorf("config: verify_code.rate_limit.%s 至少要有一条规则", channel)
	}
	seen := make(map[time.Duration]bool, len(rules))
	for _, r := range rules {
		if r.Window < time.Second || r.Limit < 1 {
			return fmt.Errorf("config: verify_code.rate_limit.%s 的规则要 window ≥ 1s、limit ≥ 1，收到 window=%s limit=%d", channel, r.Window, r.Limit)
		}
		if seen[r.Window] {
			return fmt.Errorf("config: verify_code.rate_limit.%s 的 window %s 写了两次", channel, r.Window)
		}
		seen[r.Window] = true
	}
	return nil
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

// normalizePublicURL 去掉末尾的 /（之后直接拼 "/oauth/callback"），要求是 http(s) 绝对地址、不带查询串和锚点。
func normalizePublicURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("config: public_url 必须形如 https://fp.example.com，收到 %q", raw)
	}
	return raw, nil
}
