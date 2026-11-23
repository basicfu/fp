import { test, expect } from 'vitest'
import { compactId, isUuidLike } from './format'

test('compactId 把带横杠的 UUID 去成 32 位', () => {
  expect(compactId('0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b')).toBe('0190a1b2c3d47e5f8a9b0c1d2e3f4a5b')
})

test('compactId 对已经是 32 位的与非 UUID 原样返回', () => {
  expect(compactId('0190a1b2c3d47e5f8a9b0c1d2e3f4a5b')).toBe('0190a1b2c3d47e5f8a9b0c1d2e3f4a5b')
  expect(compactId('user-1')).toBe('user-1')
  expect(compactId('')).toBe('')
})

test('isUuidLike 两种写法都认，其余不认', () => {
  expect(isUuidLike('0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b')).toBe(true)
  expect(isUuidLike('0190A1B2C3D47E5F8A9B0C1D2E3F4A5B')).toBe(true)
  expect(isUuidLike('13800138000')).toBe(false)
  expect(isUuidLike('0190a1b2c3d47e5f8a9b0c1d2e3f4a5')).toBe(false)
  expect(isUuidLike('0190a1b2c3d47e5f8a9b0c1d2e3f4a5g')).toBe(false)
  // 只认标准 36 位与 32 位两种，横杠漏一部分的半截写法不算。
  expect(isUuidLike('0190a1b2c3d4-7e5f-8a9b-0c1d2e3f4a5b')).toBe(false)
  expect(isUuidLike('0190a1b2-c3d47e5f8a9b0c1d2e3f4a5b')).toBe(false)
})
