package service

// MaxTokensPerEventForTest 是 maxTokensPerEvent 的导出别名，仅供测试引用。
//
// 常量本身刻意不导出：单条撤销事件的体积上限是这个包的内部实现细节，
// 外部调用方不该依赖它的具体数值。这个文件只在 `go test` 时参与编译，
// 不会泄漏进生产构建。
const MaxTokensPerEventForTest = maxTokensPerEvent

// AppCacheLenForTest 是本实例 Host 缓存与按 id 缓存的条目数。
func AppCacheLenForTest(s *ApplicationService) (hosts, ids int) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	return len(s.hostCache), len(s.idCache)
}

// SetAppCacheForTest 调小缓存上限，并在「查库之后、写缓存之前」插一段动作。
func SetAppCacheForTest(s *ApplicationService, max int, afterLoad func()) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if max > 0 {
		s.cacheMax = max
	}
	s.afterLoad = afterLoad
}
