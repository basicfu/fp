import { test, expect, vi, afterEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import UserRolesCard from './UserRolesCard'

afterEach(() => vi.unstubAllGlobals())

test('说明所有用户自动拥有 GUEST', async () => {
  const data: Record<string, unknown> = {
    '/admin/api/users/u1/roles': { roles: [] },
    '/admin/api/roles': [],
    '/admin/api/applications': [],
  }
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) =>
      url in data ? Promise.resolve(new Response(JSON.stringify(data[url]), { status: 200 })) : Promise.reject(new Error(url)),
    ),
  )
  render(<UserRolesCard userId="u1" />)
  expect(await screen.findByText(/自动拥有内置角色/)).toBeTruthy()
})
