package domain

// FieldType 决定管理 UI 用什么控件渲染该配置项。
type FieldType string

const (
	FieldTypeString FieldType = "string"
	FieldTypeInt    FieldType = "int"
	FieldTypeBool   FieldType = "bool"
	// FieldTypeSecret 与 string 相同，但 UI 上脱敏显示、落库加密。
	//
	// 【终审】脱敏与落库加密目前都还没实现：SetConnector 把 config 原样
	// json.Marshal 明文写进 application_connector.config（JSONB，见
	// service/application.go 的 SetConnector），ListConnectors 也原样
	// unmarshal 明文返回；前端 DynamicForm 把 secret 字段渲染成
	// <input type="password"> 只是纯视觉遮挡，不是加密。当前两个已注册的
	// connector（password、sms_code）都没有声明任何 secret 字段，所以现在
	// 没有实际泄露——这是本阶段计划里明确记为"本阶段不做"的一项。但新增
	// 任何带 secret 字段的 connector（比如微信登录的 AppSecret）之前，必须
	// 先把这两样补上：否则那个 secret 会明文出现在
	// GET /admin/api/applications/{id}/connectors 的响应体里。
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
