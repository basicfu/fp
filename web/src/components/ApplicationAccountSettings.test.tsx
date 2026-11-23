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

test('只有域名和验签可改，没有总开关和逐项权限', () => {
  render(<ApplicationAccountSettings app={baseApp} onSaved={() => {}} />)
  expect(screen.getByLabelText('域名')).toBeTruthy()
  expect(screen.getByRole('switch', { name: '验签' })).toBeTruthy()
  expect(screen.queryByRole('switch', { name: '启用账号接口' })).toBeFalsy()
  expect(screen.queryByLabelText('设备头名称')).toBeFalsy()
  expect(screen.queryByLabelText('扫码回跳地址')).toBeFalsy()
  expect(screen.queryByLabelText('密码登录')).toBeFalsy()
  expect(screen.queryByLabelText('昵称')).toBeFalsy()
})

test('业务事件只读，按应用是否已开始接收显示', () => {
  const first = render(<ApplicationAccountSettings app={baseApp} onSaved={() => {}} />)
  expect(screen.getByText('未开启')).toBeTruthy()
  expect(screen.queryByRole('switch', { name: '业务事件' })).toBeFalsy()
  first.unmount()
  render(
    <ApplicationAccountSettings app={{ ...baseApp, account: { ...disabledAccountConfig, events: true } }} onSaved={() => {}} />,
  )
  expect(screen.getByText('已开启')).toBeTruthy()
})

test('保存时按行拆域名并带上验签开关', async () => {
  const calls = stubApi({ 'PUT /applications/app-1/account': baseApp })
  const onSaved = vi.fn()
  const app = { ...baseApp, domains: ['a.example.com'], account: { ...disabledAccountConfig, events: true } }
  const { container } = render(<ApplicationAccountSettings app={app} onSaved={onSaved} />)
  fireEvent.change(screen.getByLabelText('域名'), { target: { value: 'a.example.com\n\n b.example.com \n' } })
  fireEvent.click(screen.getByRole('switch', { name: '验签' }))
  fireEvent.submit(container.querySelector('form')!)

  await waitFor(() => expect(onSaved).toHaveBeenCalled())
  expect(calls[0].body).toEqual({
    domains: ['a.example.com', 'b.example.com'],
    account: { deviceBinding: true },
  })
})
