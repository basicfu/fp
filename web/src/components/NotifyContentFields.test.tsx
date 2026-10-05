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

// 【辨别力】逗号分隔的变量框同 @ 列表：刚敲下的逗号与空格不能被"解析再回写"吃掉，
// 否则第二个变量只能粘贴、没法手敲。每一键之后输入框都得原样显示已敲的内容。
test('vendor 模式：逐键敲 "code, minutes"，输入框始终显示已敲的内容', () => {
  render(<Harness channel="sms" mode="vendor" />)
  const input = screen.getByLabelText('变量') as HTMLInputElement
  let typed = ''
  for (const ch of 'code, minutes') {
    typed += ch
    fireEvent.change(input, { target: { value: input.value + ch } })
    expect(input.value).toBe(typed)
  }
  expect(current().variables).toEqual(['code', 'minutes'])
})

// 输入框的文字归本地所有后，value 被外部改掉（这里是"从原文提取"）时显示必须跟着变。
test('vendor 模式：点"从原文提取"后，变量框显示提取结果而不是之前敲的字', () => {
  render(<Harness channel="sms" mode="vendor" />)
  fireEvent.change(screen.getByLabelText(/供应商模板原文/), { target: { value: '验证码 ${code}' } })
  const input = screen.getByLabelText('变量') as HTMLInputElement
  fireEvent.change(input, { target: { value: 'abc' } })
  expect(input.value).toBe('abc')

  fireEvent.click(screen.getByRole('button', { name: '从原文提取' }))
  expect(input.value).toBe('code')
  expect(current().variables).toEqual(['code'])
})

// 同一类外部改动的另一条来路：custom 模式的变量由正文推出，只读框里显示的也得是推出的结果。
test('custom 模式：只读的变量框显示由正文推出的变量', () => {
  render(<Harness channel="telegram" mode="custom" />)
  fireEvent.change(screen.getByLabelText('消息模板'), { target: { value: '订单 {id}，{name}' } })
  expect((screen.getByLabelText('变量') as HTMLInputElement).value).toBe('id, name')
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
