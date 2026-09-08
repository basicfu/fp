import { test, expect } from 'vitest'
import { buildBreadcrumb } from './breadcrumb'

test('应用列表页只有一段面包屑', () => {
  expect(buildBreadcrumb('/applications')).toEqual([{ label: '应用列表' }])
})

test('用户和角色相关路径', () => {
  expect(buildBreadcrumb('/users')).toEqual([{ label: '用户管理' }])
  expect(buildBreadcrumb('/users/u1')).toEqual([
    { label: '用户管理', to: '/users' },
    { label: '用户详情' },
  ])
  expect(buildBreadcrumb('/roles')).toEqual([{ label: '角色管理' }])
  expect(buildBreadcrumb('/roles/r1')).toEqual([
    { label: '角色管理', to: '/roles' },
    { label: '角色详情' },
  ])
})

test('权限管理和配置中心/版本历史', () => {
  expect(buildBreadcrumb('/permissions')).toEqual([{ label: '权限管理' }])
  expect(buildBreadcrumb('/config')).toEqual([{ label: '配置中心' }])
  expect(buildBreadcrumb('/config/versions')).toEqual([
    { label: '配置中心', to: '/config' },
    { label: '版本历史' },
  ])
})

test('未知路径回退成一段「fp」，不抛错', () => {
  expect(buildBreadcrumb('/unknown')).toEqual([{ label: 'fp' }])
  expect(buildBreadcrumb('/')).toEqual([{ label: 'fp' }])
})
