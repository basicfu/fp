package domain

import (
	"bytes"
	"encoding/json"

	"github.com/google/uuid"
)

// 配置分区。type 是分区不是标记：同名 key 在两个分区下是两个独立的配置项，
// 各有各的值与版本序列（设计文档第三节）。
const (
	// ConfigTypeDefault 是后端服务读的分区。密钥类**必须**建在这里——
	// 没有 secret 标记之后，分区是阻止密钥被下发到浏览器的唯一的闸。
	ConfigTypeDefault = "DEFAULT"
	// ConfigTypeWeb 会被业务方转发给浏览器。
	ConfigTypeWeb = "WEB"
)

// 配置项的值类型。刻意不带任何一门语言的特性——没有 duration，因为其他
// 语言没这个概念；Go 的 time.Duration 是 SDK 按毫秒当 int 处理的私事。
const (
	ConfigValueBool   = "bool"
	ConfigValueInt    = "int"
	ConfigValueFloat  = "float"
	ConfigValueString = "string"
	ConfigValueArray  = "array"
	ConfigValueObject = "object"
)

// IsConfigType 报告 s 是否是合法分区。大小写敏感。
func IsConfigType(s string) bool {
	return s == ConfigTypeDefault || s == ConfigTypeWeb
}

// IsConfigValueType 报告 s 是否是合法值类型。大小写敏感。
func IsConfigValueType(s string) bool {
	switch s {
	case ConfigValueBool, ConfigValueInt, ConfigValueFloat,
		ConfigValueString, ConfigValueArray, ConfigValueObject:
		return true
	}
	return false
}

// ConfigField 是 config.fields 里的一项。
type ConfigField struct {
	Type  string          `json:"type"`
	Desc  string          `json:"desc"`
	Value json.RawMessage `json:"value"`
}

// IsSet 报告这一项是否已配置。
//
// 判据只有一条：值不是 JSON null。false / 0 / "" 都是**有效值**，
// 不是"未配置"——把它们也算作未配置的话，一个刻意关掉的开关会在每次
// 启动时被 SDK 报成缺失。
func (f ConfigField) IsSet() bool {
	v := bytes.TrimSpace(f.Value)
	return len(v) > 0 && !bytes.Equal(v, []byte("null"))
}

// Config 是一个分区的一个版本快照。
type Config struct {
	ApplicationID uuid.UUID
	Type          string
	Seq           int64
	Fields        map[string]ConfigField
	CreatedAt     int64
}

// CoerceConfigValue 把提交上来的值按 valueType 转成规范 JSON。
//
// 弱约束（设计文档 6.3）：`"3"` 填进 int 字段照样过，**只有转不过去才报错**。
// 这里不做范围与枚举校验——那交给业务方在 Bind 之后自己判。
//
// JSON null 表示"未配置"，对任何类型都合法，原样返回。
func CoerceConfigValue(valueType string, raw json.RawMessage) (json.RawMessage, error) {
	if !IsConfigValueType(valueType) {
		return nil, Fail(ErrInvalidArgument, CodeConfigTypeInvalid, "配置项类型不合法").
			WithDesc("未知类型 %q", valueType)
	}

	s := bytes.TrimSpace(raw)
	if len(s) == 0 || bytes.Equal(s, []byte("null")) {
		return json.RawMessage("null"), nil
	}

	// 先看它是不是一个 JSON 字符串。是的话把字符串**内容**拿出来再解析一遍，
	// 这就是"弱"的全部含义：控制台上人手填的东西经常是 "3" 而不是 3。
	// string 类型例外——它要的就是这个字符串本身。
	if valueType != ConfigValueString {
		var unquoted string
		if json.Unmarshal(s, &unquoted) == nil {
			s = bytes.TrimSpace([]byte(unquoted))
		}
	}

	fail := func(err error) (json.RawMessage, error) {
		return nil, Fail(ErrInvalidArgument, CodeConfigValueInvalid, "配置值与类型不匹配").
			WithDesc("无法把 %s 解析成 %s: %v", raw, valueType, err)
	}

	switch valueType {
	case ConfigValueBool:
		var v bool
		if err := json.Unmarshal(s, &v); err != nil {
			return fail(err)
		}
		return json.Marshal(v)
	case ConfigValueInt:
		// int64 而不是 float64：3.7 进 int 必须报错，不能静默截断。
		var v int64
		if err := json.Unmarshal(s, &v); err != nil {
			return fail(err)
		}
		return json.Marshal(v)
	case ConfigValueFloat:
		var v float64
		if err := json.Unmarshal(s, &v); err != nil {
			return fail(err)
		}
		return json.Marshal(v)
	case ConfigValueString:
		var v string
		if err := json.Unmarshal(s, &v); err == nil {
			return json.Marshal(v)
		}
		// 不是 JSON 字符串（比如填了个裸数字 3），把原文当字符串收下。
		return json.Marshal(string(s))
	case ConfigValueArray:
		var v []any
		if err := json.Unmarshal(s, &v); err != nil {
			return fail(err)
		}
		return json.Marshal(v)
	default: // ConfigValueObject
		var v map[string]any
		if err := json.Unmarshal(s, &v); err != nil {
			return fail(err)
		}
		return json.Marshal(v)
	}
}
