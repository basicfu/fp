package domain

// SystemConfig 是 fp 自身启动配置的一个版本快照。Value 是管理端提交的
// YAML 原文，原样存储——语义与 Config.Value 相同，见
// internal/domain/config.go 顶部注释。
type SystemConfig struct {
	Seq       int64
	Value     string
	CreatedAt int64
}
