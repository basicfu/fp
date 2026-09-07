import { test, expect } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import ThemeModeToggle from './ThemeModeToggle'

test('渲染明暗切换按钮，点击不抛错', () => {
  render(<ThemeModeToggle />)
  const button = screen.getByRole('button', { name: '切换明暗模式' })
  fireEvent.click(button)
  expect(button).toBeTruthy()
})
