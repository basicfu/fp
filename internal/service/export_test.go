package service

// MaxTokensPerEventForTest 是 maxTokensPerEvent 的导出别名，仅供测试引用。
//
// 常量本身刻意不导出：单条撤销事件的体积上限是这个包的内部实现细节，
// 外部调用方不该依赖它的具体数值。这个文件只在 `go test` 时参与编译，
// 不会泄漏进生产构建。
const MaxTokensPerEventForTest = maxTokensPerEvent
