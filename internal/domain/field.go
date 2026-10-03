package domain

// FieldType 决定管理 UI 用什么控件渲染该配置项。
type FieldType string

const (
	FieldTypeString FieldType = "string"
	FieldTypeInt    FieldType = "int"
	FieldTypeBool   FieldType = "bool"
	// FieldTypeSecret 与 string 相同，但控制台读取时脱敏显示（MaskSecrets），
	// 写回时收到 SecretMask 表示保持原值（MergeSecrets）。
	//
	// 落库**不加密**，与访问密钥的 SK 同一先例（docs/access-key.md）。目前只有通知供应商
	// 的配置走了这套脱敏；登录方式的 application_connector.config 仍是明文进出，
	// 新增带 secret 字段的 connector 之前要先把脱敏接上，否则 secret 会明文出现在
	// GET /admin/api/applications/{id}/connectors 的响应里。
	FieldTypeSecret FieldType = "secret"
)

// Field 是一个配置项的元数据。管理 UI 靠它自动生成表单，
// 因此新增登录方式或通知供应商都不需要改动前端代码。
type Field struct {
	Key      string    `json:"key"`
	Label    string    `json:"label"`
	Type     FieldType `json:"type"`
	Required bool      `json:"required"`
	Default  any       `json:"default,omitempty"`
	Help     string    `json:"help,omitempty"`
}

// SecretMask 是 secret 字段在控制台读取时的占位值；写回时收到它表示"保持原值"。
const SecretMask = "********"

// ConfigFieldError 是配置与 schema 不符：Key 是出问题的配置项。
type ConfigFieldError struct {
	Key string
	Msg string
}

func (e *ConfigFieldError) Error() string { return e.Key + ": " + e.Msg }

// NormalizeConfig 按 schema 校验并规整一份配置：未知键、缺失的必填项、类型不符都拒绝，
// 缺省项补上 Default。返回的是新 map，不改入参。
//
// 值来自 JSON 解码，所以 int 字段收到的是 float64，这里要求它是整数并转成 int64。
func NormalizeConfig(schema []Field, in map[string]any) (map[string]any, *ConfigFieldError) {
	known := make(map[string]Field, len(schema))
	for _, f := range schema {
		known[f.Key] = f
	}
	for k := range in {
		if _, ok := known[k]; !ok {
			return nil, &ConfigFieldError{Key: k, Msg: "未知的配置项"}
		}
	}

	out := make(map[string]any, len(schema))
	for _, f := range schema {
		v, present := in[f.Key]
		if !present || v == nil || v == "" {
			if f.Default != nil {
				out[f.Key] = f.Default
				continue
			}
			if f.Required {
				return nil, &ConfigFieldError{Key: f.Key, Msg: "不能为空"}
			}
			continue
		}
		switch f.Type {
		case FieldTypeString, FieldTypeSecret:
			s, ok := v.(string)
			if !ok {
				return nil, &ConfigFieldError{Key: f.Key, Msg: "需要字符串"}
			}
			out[f.Key] = s
		case FieldTypeInt:
			switch n := v.(type) {
			case float64:
				if n != float64(int64(n)) {
					return nil, &ConfigFieldError{Key: f.Key, Msg: "需要整数"}
				}
				out[f.Key] = int64(n)
			case int:
				out[f.Key] = int64(n)
			case int64:
				out[f.Key] = n
			default:
				return nil, &ConfigFieldError{Key: f.Key, Msg: "需要整数"}
			}
		case FieldTypeBool:
			b, ok := v.(bool)
			if !ok {
				return nil, &ConfigFieldError{Key: f.Key, Msg: "需要布尔值"}
			}
			out[f.Key] = b
		default:
			return nil, &ConfigFieldError{Key: f.Key, Msg: "不支持的字段类型 " + string(f.Type)}
		}
	}
	return out, nil
}

// MaskSecrets 返回一份副本，其中 secret 字段的非空值换成 SecretMask。
func MaskSecrets(schema []Field, config map[string]any) map[string]any {
	out := make(map[string]any, len(config))
	for k, v := range config {
		out[k] = v
	}
	for _, f := range schema {
		if f.Type != FieldTypeSecret {
			continue
		}
		if s, ok := out[f.Key].(string); ok && s != "" {
			out[f.Key] = SecretMask
		}
	}
	return out
}

// MergeSecrets 把 incoming 里等于 SecretMask 的 secret 字段换回 existing 里的值，
// 其余字段原样返回。不改入参。
func MergeSecrets(schema []Field, incoming, existing map[string]any) map[string]any {
	out := make(map[string]any, len(incoming))
	for k, v := range incoming {
		out[k] = v
	}
	for _, f := range schema {
		if f.Type != FieldTypeSecret {
			continue
		}
		if s, ok := out[f.Key].(string); ok && s == SecretMask {
			if old, has := existing[f.Key]; has {
				out[f.Key] = old
			} else {
				delete(out, f.Key)
			}
		}
	}
	return out
}
