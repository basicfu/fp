import { test, expect, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { DynamicForm } from './DynamicForm'
import type { Field } from '@/lib/types'

function submitForm() {
  fireEvent.click(screen.getByRole('button', { name: '保存' }))
}

test('按 schema 渲染出每个字段', () => {
  const fields: Field[] = [
    { key: 'apiKey', label: 'API 密钥', type: 'string', required: false },
    { key: 'enabled', label: '开启', type: 'bool', required: false },
  ]
  render(<DynamicForm fields={fields} values={{}} onSubmit={vi.fn()} />)

  expect(screen.getByLabelText('API 密钥')).toBeDefined()
  // bool 字段渲染成 Switch：可见的 role="switch" 根元素不是 htmlFor 关联的那个
  // DOM 节点（见 DynamicForm.tsx 顶部注释），要用 getByRole 按可访问名定位，
  // 不能用 getByLabelText——后者会同时命中隐藏的原生 checkbox 而报"多个元素"。
  expect(screen.getByRole('switch', { name: '开启' })).toBeDefined()
})

test('展示字段的 help 文案', () => {
  const fields: Field[] = [
    { key: 'autoRegister', label: '自动注册', type: 'bool', required: false, help: '关闭后未注册手机号无法登录' },
  ]
  render(<DynamicForm fields={fields} values={{}} onSubmit={vi.fn()} />)
  expect(screen.getByText('关闭后未注册手机号无法登录')).toBeDefined()
})

test('必填字段为空时不提交并给出提示', async () => {
  const onSubmit = vi.fn()
  const fields: Field[] = [{ key: 'apiKey', label: 'API 密钥', type: 'string', required: true }]
  render(<DynamicForm fields={fields} values={{}} onSubmit={onSubmit} />)

  submitForm()
  await waitFor(() => expect(screen.getByText(/必填|不能为空|请填写/)).toBeDefined())
  expect(onSubmit).not.toHaveBeenCalled()
})

// 【辨别力】int 字段必须提交 number 而不是 string。
//
// <input type="number"> 经 react-hook-form 拿到的是字符串 "30"。直接提交的话
// 后端 decodeJSON 会因为类型不符返回 400，而错误消息只说"请求体解析失败"，
// 排查时几乎不可能联想到是这里。断言必须同时检查值和类型——只写
// toEqual({ttl: 30}) 的话，"30" 和 30 在某些断言下会被判等而放过去。
test('int 字段提交的是数字而不是字符串', async () => {
  const onSubmit = vi.fn()
  const fields: Field[] = [{ key: 'ttl', label: '有效期', type: 'int', required: false }]
  render(<DynamicForm fields={fields} values={{}} onSubmit={onSubmit} />)

  fireEvent.change(screen.getByLabelText('有效期'), { target: { value: '30' } })
  submitForm()

  await waitFor(() => expect(onSubmit).toHaveBeenCalled())
  const payload = onSubmit.mock.calls[0][0]
  expect(payload.ttl).toBe(30)
  expect(typeof payload.ttl).toBe('number')
})

// 【辨别力】schema 里的 default 必须体现在界面初始状态上。
//
// password 与 sms_code 的开关默认值都是 true。若实现用 defaultValues: {}
// 起手，开关会显示成"关"，而实际生效的是 connector 代码里的 true——
// 界面在撒谎，管理员据此做的判断全是错的。
test('未配置过时采用 schema 声明的默认值', async () => {
  const onSubmit = vi.fn()
  const fields: Field[] = [
    { key: 'allowPhone', label: '允许手机号', type: 'bool', required: false, default: true },
    { key: 'allowEmail', label: '允许邮箱', type: 'bool', required: false, default: false },
  ]
  render(<DynamicForm fields={fields} values={{}} onSubmit={onSubmit} />)

  expect(screen.getByRole('switch', { name: '允许手机号' }).getAttribute('aria-checked')).toBe('true')
  expect(screen.getByRole('switch', { name: '允许邮箱' }).getAttribute('aria-checked')).toBe('false')

  submitForm()
  await waitFor(() => expect(onSubmit).toHaveBeenCalled())
  expect(onSubmit.mock.calls[0][0]).toEqual({ allowPhone: true, allowEmail: false })
})

// 【辨别力】已有配置必须覆盖 schema 的 default。
//
// 顺序写反的话，任何被管理员显式关掉的开关，每次打开页面都会显示成"开"。
test('已有配置优先于 schema 默认值', () => {
  const fields: Field[] = [{ key: 'allowPhone', label: '允许手机号', type: 'bool', required: false, default: true }]
  render(<DynamicForm fields={fields} values={{ allowPhone: false }} onSubmit={vi.fn()} />)

  expect(screen.getByRole('switch', { name: '允许手机号' }).getAttribute('aria-checked')).toBe('false')
})

test('secret 字段渲染成密码输入框', () => {
  const fields: Field[] = [{ key: 'sk', label: '密钥', type: 'secret', required: false }]
  render(<DynamicForm fields={fields} values={{}} onSubmit={vi.fn()} />)
  expect(screen.getByLabelText('密钥').getAttribute('type')).toBe('password')
})

// 载荷规则：非必填的空字符串省略，让 connector 用自己代码里的默认值。
test('非必填的空字符串不进入提交载荷', async () => {
  const onSubmit = vi.fn()
  const fields: Field[] = [
    { key: 'note', label: '备注', type: 'string', required: false },
    { key: 'name', label: '名称', type: 'string', required: false },
  ]
  render(<DynamicForm fields={fields} values={{}} onSubmit={onSubmit} />)

  fireEvent.change(screen.getByLabelText('名称'), { target: { value: 'x' } })
  submitForm()

  await waitFor(() => expect(onSubmit).toHaveBeenCalled())
  expect(onSubmit.mock.calls[0][0]).toEqual({ name: 'x' })
})

// 载荷规则的 int 版本：非必填的 int 字段留空，提交载荷里必须**没有**这个键，
// 而不是变成 0。如果实现把空值转成 0 提交上去，后端 connector 里
// ConfigInt(cfg, key, 30) 这样的默认值就会被 0 静默覆盖——配置文件里显式写
// 一个 0 和"没配置这一项"是两个不同的语义，界面不能替管理员做这个决定。
test('非必填的 int 字段留空不进入提交载荷', async () => {
  const onSubmit = vi.fn()
  const fields: Field[] = [{ key: 'ttl', label: '有效期', type: 'int', required: false }]
  render(<DynamicForm fields={fields} values={{}} onSubmit={onSubmit} />)

  submitForm()

  await waitFor(() => expect(onSubmit).toHaveBeenCalled())
  expect(onSubmit.mock.calls[0][0]).toEqual({})
})

// 【辨别力】未来后端加了新的 FieldType，前端不许白屏。
//
// 这个控制台的整个卖点就是"后端加登录方式，前端零改动"。如果一个不认识的
// 类型会让页面崩掉，这个卖点就是假的——而且崩的是**已有的**配置页面，
// 连带把能正常配置的登录方式一起带走。
test('遇到不认识的字段类型时退化成文本框而不是崩溃', async () => {
  const onSubmit = vi.fn()
  const fields = [{ key: 'weird', label: '未来字段', type: 'duration' }] as unknown as Field[]
  render(<DynamicForm fields={fields} values={{}} onSubmit={onSubmit} />)

  const input = screen.getByLabelText('未来字段')
  expect(input.getAttribute('type')).toBe('text')
  fireEvent.change(input, { target: { value: '5m' } })
  submitForm()

  await waitFor(() => expect(onSubmit).toHaveBeenCalled())
  expect(onSubmit.mock.calls[0][0]).toEqual({ weird: '5m' })
})

test('没有任何字段时也能渲染并提交空配置', async () => {
  const onSubmit = vi.fn()
  render(<DynamicForm fields={[]} values={{}} onSubmit={onSubmit} />)

  submitForm()
  await waitFor(() => expect(onSubmit).toHaveBeenCalled())
  expect(onSubmit.mock.calls[0][0]).toEqual({})
})

// 【终审必须修】DOM id 未命名空间化。ConnectorsPanel 会把每个 connector 的
// DynamicForm 渲染在同一个页面上；两个 connector 各自声明一个同名字段
// （"enabled"、"autoRegister" 之类都是极自然的命名）时，若 id/htmlFor 直接
// 用 f.key，两个开关的可访问名会互相串——终审实测点了"B 的开关"文案，
// 翻的却是 A。这不是可访问性瑕疵，是管理员在登录方式页上一次没人授意的
// 配置写入。用两个渲染在同一文档里、字段 key 相同但 label 不同的
// DynamicForm 实例模拟两个 connector，而不是断言具体 id 字符串——后者会
// 绑死实现细节（idPrefix vs useId），这条测试只关心可观察的行为：按可访问名
// 各自唯一命中，点其中一个不连带翻另一个。
test('两个字段 key 相同的 DynamicForm 实例（模拟两个 connector）互不串扰', () => {
  const fieldsA: Field[] = [{ key: 'enabled', label: 'A 的开关', type: 'bool', required: false }]
  const fieldsB: Field[] = [{ key: 'enabled', label: 'B 的开关', type: 'bool', required: false }]
  render(
    <>
      <DynamicForm fields={fieldsA} values={{}} onSubmit={vi.fn()} />
      <DynamicForm fields={fieldsB} values={{}} onSubmit={vi.fn()} />
    </>,
  )

  // getByRole 本身要求“恰好一个”命中；命名空间没做对的话，这两行会先炸：
  // 要么两个开关的可访问名都被解析成同一个 label（2 个命中），要么另一个
  // 变成 0 个命中——终审报告里实测到的正是这种情况。
  const switchA = screen.getByRole('switch', { name: 'A 的开关' })
  const switchB = screen.getByRole('switch', { name: 'B 的开关' })
  expect(switchA.getAttribute('aria-checked')).toBe('false')
  expect(switchB.getAttribute('aria-checked')).toBe('false')

  fireEvent.click(switchB)

  expect(switchB.getAttribute('aria-checked')).toBe('true')
  // 关键断言：点 B 不能连带把 A 也翻了。
  expect(switchA.getAttribute('aria-checked')).toBe('false')
})
