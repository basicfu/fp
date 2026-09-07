import { test, expect } from 'vitest'
import { buildBreadcrumb } from './breadcrumb'

test('应用列表页只有一段面包屑', () => {
  expect(buildBreadcrumb('/applications', null)).toEqual([{ label: '应用' }])
})

test('应用详情页展示应用名，取不到名字时退化成"应用详情"', () => {
  expect(buildBreadcrumb('/applications/app-1', 'Acme')).toEqual([
    { label: '应用', to: '/applications' },
    { label: 'Acme' },
  ])
  expect(buildBreadcrumb('/applications/app-1', null)).toEqual([
    { label: '应用', to: '/applications' },
    { label: '应用详情' },
  ])
})

test('配置中心和版本历史页拼出完整层级', () => {
  expect(buildBreadcrumb('/applications/app-1/config', 'Acme')).toEqual([
    { label: '应用', to: '/applications' },
    { label: 'Acme', to: '/applications/app-1' },
    { label: '配置中心' },
  ])
  expect(buildBreadcrumb('/applications/app-1/config/versions', 'Acme')).toEqual([
    { label: '应用', to: '/applications' },
    { label: 'Acme', to: '/applications/app-1' },
    { label: '配置中心', to: '/applications/app-1/config' },
    { label: '版本历史' },
  ])
})

test('用户和角色相关路径', () => {
  expect(buildBreadcrumb('/users', null)).toEqual([{ label: '用户' }])
  expect(buildBreadcrumb('/users/u1', null)).toEqual([
    { label: '用户', to: '/users' },
    { label: '用户详情' },
  ])
  expect(buildBreadcrumb('/roles', null)).toEqual([{ label: '角色' }])
  expect(buildBreadcrumb('/roles/r1', null)).toEqual([
    { label: '角色', to: '/roles' },
    { label: '角色详情' },
  ])
})

test('未知路径回退成一段「fp」，不抛错', () => {
  expect(buildBreadcrumb('/unknown', null)).toEqual([{ label: 'fp' }])
  expect(buildBreadcrumb('/', null)).toEqual([{ label: 'fp' }])
})
