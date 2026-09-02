import { useCallback, useEffect, useState } from 'react'

/** errorMessage 把任意抛出物转成能显示给人看的字符串。 */
export function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : '请求失败'
}

interface Resource<T> {
  data: T | null
  loading: boolean
  error: string
  /** reload 重新拉取，用于写操作之后刷新列表。 */
  reload: () => void
}

/**
 * useResource 是五个页面共用的"拉数据"钩子。
 *
 * alive 守卫不是可有可无的卫生措施：依赖变化（翻页、改搜索词）会连续发出
 * 多个请求，网络乱序时先发的可能后到。没有守卫的话，界面会显示上一个
 * 关键词的结果，而输入框里是新关键词——真机上偶发且难复现。
 */
export function useResource<T>(load: () => Promise<T>, deps: unknown[]): Resource<T> {
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [nonce, setNonce] = useState(0)

  useEffect(() => {
    let alive = true
    setLoading(true)
    setError('')
    load()
      .then((d) => {
        if (!alive) return
        setData(d)
        setLoading(false)
      })
      .catch((e) => {
        if (!alive) return
        setError(errorMessage(e))
        setLoading(false)
      })
    return () => {
      alive = false
    }
    // load 每次渲染都是新函数，不能进依赖数组；由调用方通过 deps 声明。
  }, [...deps, nonce]) // eslint-disable-line react-hooks/exhaustive-deps

  const reload = useCallback(() => setNonce((n) => n + 1), [])
  return { data, loading, error, reload }
}
