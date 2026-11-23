import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import ApplicationAccountSettings from './ApplicationAccountSettings'
import { stubApi } from '@/lib/testApi'
import { disabledAccountConfig, disabledIMConfig } from '@/lib/testFixtures'
import type { Application } from '@/lib/types'

afterEach(() => vi.unstubAllGlobals())

const baseApp: Application = {
  id: 'app-1',
  name: '测试应用',
  code: 'test-app',
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
  domains: [],
  account: disabledAccountConfig,
  createdAt: 1700000000000,
  updatedAt: 1700000000000,
}

test('账号接口未启用时只显示总开关和域名', () => {
  render(<ApplicationAccountSettings app={baseApp} onSaved={() => {}} />)
  expect(screen.getByRole('switch', { name: '启用账号接口' })).toBeTruthy()
  expect(screen.getByLabelText('域名')).toBeTruthy()
  expect(screen.queryByLabelText('设备头名称')).toBeFalsy()
})

test('设备头名称只在开启设备绑定后出现', () => {
  const app = { ...baseApp, account: { ...disabledAccountConfig, enabled: true } }
  const first = render(<ApplicationAccountSettings app={app} onSaved={() => {}} />)
  expect(screen.queryByLabelText('设备头名称')).toBeFalsy()
  first.unmount()
  render(
    <ApplicationAccountSettings
      app={{ ...app, account: { ...app.account, deviceBinding: true, deviceHeader: 'X-Device' } }}
      onSaved={() => {}}
    />,
  )
  expect((screen.getByLabelText('设备头名称') as HTMLInputElement).value).toBe('X-Device')
})

test('保存时按行拆域名并带上勾选项', async () => {
  const calls = stubApi({ 'PUT /applications/app-1/account': baseApp })
  const onSaved = vi.fn()
  const app = {
    ...baseApp,
    domains: ['a.example.com'],
    account: { ...disabledAccountConfig, enabled: true, loginTypes: ['sms'] },
  }
  const { container } = render(<ApplicationAccountSettings app={app} onSaved={onSaved} />)
  fireEvent.change(screen.getByLabelText('域名'), { target: { value: 'a.example.com\n\n b.example.com \n' } })
  fireEvent.click(screen.getByLabelText('密码登录'))
  fireEvent.click(screen.getByLabelText('昵称'))
  fireEvent.submit(container.querySelector('form')!)

  await waitFor(() => expect(onSaved).toHaveBeenCalled())
  expect(calls[0].body).toEqual({
    domains: ['a.example.com', 'b.example.com'],
    account: {
      enabled: true,
      loginTypes: ['sms', 'password'],
      editableFields: ['nickname'],
      deviceBinding: false,
      deviceHeader: '',
      events: false,
      wechatReturnUrl: '',
    },
  })
})

test('开启设备绑定却没填设备头时不提交', async () => {
  const calls = stubApi({})
  const app = { ...baseApp, account: { ...disabledAccountConfig, enabled: true, deviceBinding: true } }
  const { container } = render(<ApplicationAccountSettings app={app} onSaved={() => {}} />)
  fireEvent.submit(container.querySelector('form')!)
  await new Promise((r) => setTimeout(r, 20))
  expect(calls.length).toBe(0)
})
