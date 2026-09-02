// vitest 全局测试初始化。
//
// 项目刻意关闭了 vitest 的 globals（见 vite.config.ts 里的注释），每个测试
// 文件顶部都要显式 import { test, expect, ... } from 'vitest'。但这也意味着
// @testing-library/react 的自动清理失效了：它的 dist/index.js 在模块顶层用
// `typeof afterEach === 'function'` 探测全局作用域来决定要不要注册
// afterEach(cleanup)——globals 关闭后全局作用域里没有 afterEach，这个探测
// 恒为 false，组件测试之间不会自动 unmount。
//
// 后果很隐蔽：同一个测试文件里，前一个 test 渲染出的 DOM 会残留到下一个
// test，screen.getByTestId 之类的查询会因为"匹配到多个元素"而失败，
// 报错信息和真正的业务逻辑毫无关系，容易把人带偏去怀疑组件本身有 bug。
//
// 用这个 setup 文件手动补上同等效果：每个 test 结束后调用 cleanup()。
import { afterEach } from 'vitest'
import { cleanup } from '@testing-library/react'

afterEach(() => {
  cleanup()
})
