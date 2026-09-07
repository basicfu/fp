import { test, expect, afterEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import ThemeColorSwitcher from './ThemeColorSwitcher'

afterEach(() => {
  document.documentElement.removeAttribute('data-theme')
  localStorage.clear()
})

test('选择配色后写入 data-theme 属性和 localStorage', async () => {
  render(<ThemeColorSwitcher />)

  const trigger = screen.getByRole('combobox', { name: '切换主题色' })
  // base-ui 的 Select 选项只在先收到 pointerdown 再收到 click 时才会选中
  // （纯 click 会被内部 allowMouseSelectionRef 判定成非真实鼠标点击而忽略），
  // 同 ConfigCenter.test.tsx 的先例手动补上同样的顺序。
  fireEvent.pointerDown(trigger)
  fireEvent.click(trigger)
  const option = await screen.findByRole('option', { name: '紫罗兰' })
  fireEvent.pointerDown(option)
  fireEvent.click(option)

  expect(document.documentElement.getAttribute('data-theme')).toBe('violet')
  expect(localStorage.getItem('fp-ui-theme')).toBe('violet')
})

test('挂载时读取 <html data-theme> 上已有的预设作为初始选中值', () => {
  document.documentElement.setAttribute('data-theme', 'emerald')
  render(<ThemeColorSwitcher />)
  expect(screen.getByText('翡翠绿')).toBeTruthy()
})
