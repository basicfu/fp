import { useState } from 'react'
import { test, expect } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import NotifyContentFields from './NotifyContentFields'
import type { NotifyChannel, NotifyContent, NotifyMode } from '@/lib/types'

/** Harness 持有 value 并把最新值暴露在 DOM 里，测试靠它断言 onChange 的结果。 */
function Harness({ channel, mode, initial }: { channel: NotifyChannel; mode: NotifyMode; initial?: NotifyContent }) {
  const [value, setValue] = useState<NotifyContent>(initial ?? { content: '', variables: [] })
  return (
    <>
      <NotifyContentFields channel={channel} mode={mode} value={value} onChange={setValue} />
      <pre data-testid="value">{JSON.stringify(value)}</pre>
    </>
  )
}

const current = () => JSON.parse(screen.getByTestId('value').textContent!) as NotifyContent

test('custom 模式：变量由内容（与邮件主题）自动推出，且不可手填', () => {
  render(<Harness channel="email" mode="custom" />)
  fireEvent.change(screen.getByLabelText('邮件主题'), { target: { value: '你好 {name}' } })
  fireEvent.change(screen.getByLabelText('正文模板'), { target: { value: '订单 {id} 已发货，{name}' } })
  expect(current().variables).toEqual(['name', 'id'])
  expect((screen.getByLabelText('变量') as HTMLInputElement).readOnly).toBe(true)
  expect(screen.queryByRole('button', { name: '从原文提取' })).toBeNull()
})

test('vendor 模式：变量手填，也可以从供应商模板原文提取', () => {
  render(<Harness channel="sms" mode="vendor" />)
  fireEvent.change(screen.getByLabelText(/供应商模板原文/), { target: { value: '验证码 ${code}，{{minutes}} 分钟内有效' } })
  expect(current().variables).toEqual([])
  fireEvent.click(screen.getByRole('button', { name: '从原文提取' }))
  expect(current().variables).toEqual(['code', 'minutes'])

  fireEvent.change(screen.getByLabelText('变量'), { target: { value: 'code, ' } })
  expect(current().variables).toEqual(['code'])
})

test('webhook：GET 没有 Content-Type，正文标签变成 Query 模板', () => {
  render(<Harness channel="webhook" mode="custom" />)
  expect(screen.getByLabelText('Content-Type')).toBeTruthy()
  expect(screen.getByLabelText('Body 模板')).toBeTruthy()

  fireEvent.change(screen.getByLabelText('请求方式'), { target: { value: 'GET' } })
  expect(current().method).toBe('GET')
  expect(screen.queryByLabelText('Content-Type')).toBeNull()
  expect(screen.getByLabelText(/Query 模板/)).toBeTruthy()
})

// 【辨别力】"每行一项"的输入：刚敲下的换行不能被解析丢掉，否则根本没法输入第二行。
test('企业微信的 @ 列表按行输入，可以连续输入多行', () => {
  render(<Harness channel="wecom_bot" mode="custom" />)
  const box = screen.getByLabelText(/@ 成员/) as HTMLTextAreaElement
  fireEvent.change(box, { target: { value: '@all\n' } })
  expect(box.value).toBe('@all\n')
  fireEvent.change(box, { target: { value: '@all\nzhangsan' } })
  expect(current().mentionedList).toEqual(['@all', 'zhangsan'])
})

test('钉钉：@ 手机号与 @ 所有人', () => {
  render(<Harness channel="dingtalk_bot" mode="custom" />)
  fireEvent.change(screen.getByLabelText(/@ 手机号/), { target: { value: '13800138000' } })
  fireEvent.click(screen.getByRole('switch', { name: '@ 所有人' }))
  expect(current().atMobiles).toEqual(['13800138000'])
  expect(current().isAtAll).toBe(true)
})
