import { test, expect } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { useResource } from './useResource'

function Probe({ dep, load }: { dep: number; load: (d: number) => Promise<string> }) {
  const { data, loading, error } = useResource(() => load(dep), [dep])
  return <div data-testid="out">{loading ? 'loading' : error ? `err:${error}` : `data:${data}`}</div>
}

test('加载成功后给出数据', async () => {
  render(<Probe dep={1} load={async (d) => `v${d}`} />)
  await waitFor(() => expect(screen.getByTestId('out').textContent).toBe('data:v1'))
})

test('加载失败给出错误消息', async () => {
  render(<Probe dep={1} load={async () => { throw new Error('炸了') }} />)
  await waitFor(() => expect(screen.getByTestId('out').textContent).toBe('err:炸了'))
})

// 【辨别力】依赖变化引发的乱序返回。
//
// 用户在搜索框里连打两个字，第一次请求慢、第二次快，第二次先回来。
// 没有 alive 守卫的实现会在第一次请求最终返回时把旧结果盖上去——
// 界面显示的是上一个关键词的结果，而搜索框里是新关键词。
// 这个 bug 在真机上偶发、极难复现，只能靠测试挡住。
test('依赖变化后，先发出的慢请求不许覆盖后发出的结果', async () => {
  const resolvers: Record<number, (v: string) => void> = {}
  const load = (d: number) =>
    new Promise<string>((resolve) => {
      resolvers[d] = resolve
    })

  const { rerender } = render(<Probe dep={1} load={load} />)
  rerender(<Probe dep={2} load={load} />)

  // 第二次（dep=2）先返回
  await waitFor(() => expect(resolvers[2]).toBeDefined())
  resolvers[2]('v2')
  await waitFor(() => expect(screen.getByTestId('out').textContent).toBe('data:v2'))

  // 第一次（dep=1）后返回，必须被丢弃
  resolvers[1]('v1')
  await new Promise((r) => setTimeout(r, 20))
  expect(screen.getByTestId('out').textContent).toBe('data:v2')
})
