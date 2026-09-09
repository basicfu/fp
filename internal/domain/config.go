package domain

import (
	"regexp"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// ConfigTypeDefault 是唯一"系统保留"的分区名：应用创建后天然就有这个
// 分区，管理端 UI 永远把它展示成默认标签页——即使还没有对它保存过任何
// 版本。后端服务默认读的也是这个分区。
//
// 分区不再局限于固定的两个值：管理端可以为一个应用建任意数量、任意名字
// 的分区（比如 WEB、MOBILE、ADMIN_PANEL），每个分区各有各的值与版本
// 序列（设计文档第三节）。是不是"会被转发到浏览器之类的地方"完全取决于
// 业务代码怎么用这个分区的名字调 fpsdk.BindType，不再是靠一个叫 WEB 的
// 保留名字硬编码出来的行为——这一点从设计上收窄了 IsConfigType，但没有
// 改变"DEFAULT 是唯一安全、默认要落密钥的地方"这条纪律。
const ConfigTypeDefault = "DEFAULT"

// ConfigTypeWeb 不再是唯一的"另一个"保留分区——只是一个常见到值得给个
// 名字的约定：业务方转发给浏览器的分区大多习惯叫这个名字，但 IsConfigType
// 不会因为分区名不是它就拒绝。仍然保留这个常量纯粹是为了少打几个字符串
// 字面量，语义上它跟管理端随手建的 MOBILE、ADMIN_PANEL 没有任何不同。
const ConfigTypeWeb = "WEB"

// configTypePattern 限定分区名的字符集：字母开头，后接字母/数字/下划线，
// 最长 64 字符——够表达 WEB、MOBILE_APP 这类名字，同时不让空白、斜杠、
// 问号这类字符混进 URL 查询参数和展示文案里添乱。
var configTypePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

// IsConfigType 报告 s 是否是合法的分区名。大小写敏感。
func IsConfigType(s string) bool {
	return configTypePattern.MatchString(s)
}

// Config 是一个分区的一个版本快照。
//
// Value 是管理端提交的 YAML 原文，原样存储——不经过"解析成对象再序列化
// 回来"这一圈：那样会丢注释（现在注释就是备注，不是可有可无的装饰）、
// 打乱 key 顺序、还可能悄悄改写数字的书写形式。存储层只关心"这是一份
// 能解析的 YAML 文档"，解析成什么样的对象是读取时（GetConfig）才做的事。
type Config struct {
	ApplicationID uuid.UUID
	Type          string
	Seq           int64
	Value         string
	CreatedAt     int64
}

// ParseConfigYAML 把 YAML 原文解析成扁平的 {key: value} 对象，供 gRPC
// GetConfig 序列化成 JSON 吐给 SDK。
//
// 顶层必须是映射（不能是裸标量或列表）——SDK 期待的是 key 能查到 value
// 这样的对象，顶层不是映射就没有 key 可言，yaml.Unmarshal 解到
// map[string]any 时对此天然会报错（"cannot unmarshal !!seq into
// map[string]interface {}" 之类），不需要额外判断。
//
// 空文本（未保存过、或整份清空）解析成空对象，不是错误——这与旧模型下
// "一个版本都没有时 Fields 是空 map"是同一件事的延续。
//
// 【已知取舍】yaml.v3 的默认解析器会把形如 2026-09-08 这种"像日期"的标量
// 自动识别成 time.Time 再由 encoding/json 转成带引号的 RFC3339 字符串，
// 不是用户原样输入的那个字符串。这是 yaml.v3 本身的行为，这里不做额外
// 拦截或转义——真的写了一个日期形状的字符串值这种情况足够少见，为它
// 引入自定义 Resolver 的复杂度不划算。
func ParseConfigYAML(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	var v map[string]any
	if err := yaml.Unmarshal([]byte(raw), &v); err != nil {
		return nil, Fail(ErrInvalidArgument, CodeConfigValueInvalid, "配置不是合法的 YAML").
			WithDesc("解析失败: %v", err)
	}
	if v == nil {
		v = map[string]any{}
	}
	return v, nil
}

// colonMissingSpace 匹配一行开头"key:value"这种冒号后没跟空格的写法
// （可选带 "- " 列表前缀）。YAML 规定冒号后必须跟空白才会被当成映射，
// 缺这一个空格会让整份文本被解析成一个裸标量、直接报"顶层不是映射"，
// 而这是控制台手填配置时最常见的笔误（比如 port:4379），不该因为一个
// 空格就拒绝保存。
var colonMissingSpace = regexp.MustCompile(`^(\s*(?:-\s+)?[^:\s][^:]*):(\S)`)

// NormalizeConfigYAML 逐行给缺空格的 "key:value" 补上那个空格。注释行
// （# 开头，去掉前导空白后判断）原样跳过——注释是自由文本，这条启发式
// 规则不该伸手进去改写。
//
// 【已知取舍】这之后保存回显不再保证与提交的原文逐字节相同：这条规则会
// 真的改写用户提交的文本。这是设计上认下的例外——空格本身不影响任何
// 语义，用户看到的后果只是"保存后编辑框里的冒号后面多了一个空格"，用
// 这个换来"不用因为漏打一个空格就报错"，划算。
func NormalizeConfigYAML(raw string) string {
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		lines[i] = colonMissingSpace.ReplaceAllString(line, "$1: $2")
	}
	return strings.Join(lines, "\n")
}
