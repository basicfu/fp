package domain

// FieldType 决定管理 UI 用什么控件渲染该配置项。
type FieldType string

const (
	FieldTypeString FieldType = "string"
	FieldTypeInt    FieldType = "int"
	FieldTypeBool   FieldType = "bool"
	// FieldTypeSecret 与 string 相同，但 UI 上脱敏显示、落库加密。
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
