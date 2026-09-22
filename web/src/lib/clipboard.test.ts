import { test, expect, vi, afterEach } from 'vitest'
import { copyToClipboard } from './clipboard'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  Reflect.deleteProperty(navigator, 'clipboard')
  Reflect.deleteProperty(document, 'execCommand')
})

/** jsdom 压根没定义 document.execCommand，不能直接 vi.spyOn 一个不存在的属性。 */
function stubExecCommand(impl: (cmd: string) => boolean) {
  Object.defineProperty(document, 'execCommand', { value: vi.fn(impl), configurable: true })
  return document.execCommand as unknown as ReturnType<typeof vi.fn>
}

test('Clipboard API 可用时优先用它，不碰 execCommand', async () => {
  const writeText = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
  const execSpy = stubExecCommand(() => true)

  const ok = await copyToClipboard('hello')

  expect(ok).toBe(true)
  expect(writeText).toHaveBeenCalledWith('hello')
  expect(execSpy).not.toHaveBeenCalled()
})

// 【辨别力】非安全上下文（http 且非 localhost）下 navigator.clipboard 整个
// 是 undefined——这是这个后台的真实使用场景（内网 http 直接访问），不能
// 因为现代 API 不存在就让复制按钮形同虚设，必须退化到 execCommand。
test('navigator.clipboard 不存在时退化到 execCommand，且真的把文本放进了一个可选中的元素', async () => {
  Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
  let selectedValue = ''
  const execSpy = stubExecCommand(() => {
    const active = document.activeElement as HTMLTextAreaElement | null
    selectedValue = active?.value ?? ''
    return true
  })

  const ok = await copyToClipboard('fallback-text')

  expect(ok).toBe(true)
  expect(execSpy).toHaveBeenCalledWith('copy')
  expect(selectedValue).toBe('fallback-text')
  // 临时元素用完要清理掉，不能残留在 DOM 里。
  expect(document.querySelector('textarea')).toBeNull()
})

// Clipboard API 存在但 writeText 被拒绝（常见于权限策略限制）时，同样要
// 退化到 execCommand，而不是直接判定失败。
test('navigator.clipboard.writeText 被拒绝时也退化到 execCommand', async () => {
  const writeText = vi.fn().mockRejectedValue(new Error('denied'))
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
  stubExecCommand(() => true)

  const ok = await copyToClipboard('hello')

  expect(ok).toBe(true)
})

test('两条路都不通时返回 false，不抛错', async () => {
  Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
  stubExecCommand(() => {
    throw new Error('not implemented')
  })

  const ok = await copyToClipboard('hello')

  expect(ok).toBe(false)
})
