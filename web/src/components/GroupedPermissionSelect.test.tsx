import { afterEach, expect, test, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { GroupedPermissionSelect, type SelectablePoint } from './GroupedPermissionSelect'

afterEach(cleanup)

const pts: SelectablePoint[] = [
  ...['GET', 'POST', 'PUT', 'DELETE'].map((m, i) => ({ id: `o${i}`, key: `${m}:/orders/{id}`, name: `订单${m}` })),
  { id: 'ping', key: 'GET:/ping', name: '' },
]

function setup(selected: string[] = []) {
  const onChange = vi.fn()
  render(<GroupedPermissionSelect points={pts} selected={selected} onChange={onChange} />)
  fireEvent.click(screen.getByRole('combobox'))
  return onChange
}

const groupRow = () => screen.getByRole('checkbox', { name: '选中分组 /orders/{id}' }).closest('[role="option"]') as HTMLElement
const keyText = (key: string) => (_: string, el: Element | null) => el?.getAttribute('data-key') === key

test('4 个及以上才成分组，默认展开；点分组勾选框整组授权', () => {
  const onChange = setup()
  expect(screen.getByText(keyText('DELETE:/orders/{id}'))).toBeTruthy()
  expect(screen.getByText(keyText('GET:/ping'))).toBeTruthy()

  fireEvent.click(screen.getByRole('checkbox', { name: '选中分组 /orders/{id}' }))
  expect(onChange).toHaveBeenLastCalledWith(['o0', 'o1', 'o2', 'o3'])
})

test('点分组行折叠/展开，勾选单个权限点；部分选中时分组为半选', () => {
  const onChange = setup(['o0'])
  expect(screen.getByRole('checkbox', { name: '选中分组 /orders/{id}' }).hasAttribute('data-indeterminate')).toBe(true)

  fireEvent.click(groupRow())
  expect(screen.queryByText(keyText('POST:/orders/{id}'))).toBeNull()
  fireEvent.click(groupRow())
  fireEvent.click(screen.getByRole('checkbox', { name: '选中 POST:/orders/{id}' }))
  expect(onChange).toHaveBeenLastCalledWith(['o0', 'o1'])
})

test('搜索时只显示命中的权限点；选中全部只作用于当前搜索结果；清空收回全部', () => {
  const onChange = setup()
  fireEvent.change(screen.getByPlaceholderText('搜索'), { target: { value: '订单DELETE' } })
  expect(screen.getByText(keyText('DELETE:/orders/{id}'))).toBeTruthy()
  expect(screen.queryByText(keyText('GET:/ping'))).toBeNull()

  fireEvent.click(screen.getByRole('checkbox', { name: '选中全部' }))
  expect(onChange).toHaveBeenLastCalledWith(['o3'])

  fireEvent.click(screen.getByRole('button', { name: '清空' }))
  expect(onChange).toHaveBeenLastCalledWith([])
})
