import { test, expect, vi, afterEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import ApplicationIMSettings from './ApplicationIMSettings'
import { disabledIMConfig } from '@/lib/testFixtures'
import type { Application } from '@/lib/types'

afterEach(() => vi.unstubAllGlobals())

const baseApp: Application = {
  id: 'app-1',
  name: '测试应用',
  slug: 'test-app',
  appId: 'appid-123',
  status: 'ACTIVE',
  cookieDomain: '',
  defaultRoleKey: '',
  session: {
    idleTimeoutSeconds: 604800,
    idleTimeoutMobileSeconds: 2592000,
    maxLifetimeSeconds: 7776000,
    rotateIntervalSeconds: 86400,
    extendIntervalSeconds: 600,
    tokenCacheTtlSeconds: 30,
  },
  im: disabledIMConfig,
  createdAt: 1700000000000,
  updatedAt: 1700000000000,
}

function withIM(over: Partial<Application['im']>): Application {
  return { ...baseApp, im: { ...disabledIMConfig, ...over } }
}

// 关着的时候其余字段全部折叠：还没打开就摊开一屏参数，读的人分不清哪些
// 是生效的。这也与后端"Enabled=false 时不校验其余项"的取舍一致。
test('IM 未启用时不展示其余配置项', () => {
  render(<ApplicationIMSettings app={baseApp} onSaved={() => {}} />)
  expect(screen.getByRole('switch', { name: '启用 IM 接入' })).toBeTruthy()
  expect(screen.queryByLabelText("连接策略")).toBeFalsy()
  expect(screen.queryByRole('switch', { name: '允许访客' })).toBeFalsy()
})

// connLimit 只在 limit 策略下有意义。别的策略下摆着一个不生效的输入框，
// 会让人以为自己配的数字在起作用。
test('并发上限只在 limit 策略下出现', () => {
  // 两次独立渲染而不是 rerender：useForm 的 defaultValues 只在挂载时读一次，
  // rerender 换 prop 不会重置表单状态，测出来的还是第一次那个策略。
  const first = render(
    <ApplicationIMSettings app={withIM({ enabled: true, connPolicy: 'replace' })} onSaved={() => {}} />,
  )
  expect(screen.queryByLabelText('并发上限')).toBeFalsy()
  first.unmount()

  render(
    <ApplicationIMSettings app={withIM({ enabled: true, connPolicy: 'limit', connLimit: 3 })} onSaved={() => {}} />,
  )
  expect(screen.getByLabelText('并发上限')).toBeTruthy()
})
