import { test, expect } from 'vitest'
import { allowedModes, extractVariables, needsRecipient, parseLines } from './notify'

test('extractVariables 取出去重后的占位符，支持三种写法', () => {
  expect(extractVariables('恭喜{name}登录成功')).toEqual(['name'])
  expect(extractVariables('{a}{b}{a}')).toEqual(['a', 'b'])
  expect(extractVariables('验证码 ${code}，{{minutes}} 分钟内有效')).toEqual(['code', 'minutes'])
  // 主题与正文一起提取：邮件的变量要覆盖两处。
  expect(extractVariables('{s}', '{c}')).toEqual(['s', 'c'])
})

// JSON 的花括号后面是引号，不是占位符。
test('extractVariables 不会把 JSON 花括号当成占位符', () => {
  expect(extractVariables('{"text":"hi {name}","n":{count}}')).toEqual(['name', 'count'])
  expect(extractVariables('{}')).toEqual([])
  expect(extractVariables('{ name }')).toEqual([])
  expect(extractVariables('{1abc}')).toEqual([])
})

test('parseLines 去掉空行与首尾空白', () => {
  expect(parseLines(' a \n\n b\n')).toEqual(['a', 'b'])
  expect(parseLines('')).toEqual([])
})

test('渠道规则与后端一致', () => {
  expect(allowedModes.sms).toEqual(['vendor'])
  expect(allowedModes.email).toEqual(['custom', 'vendor'])
  expect(allowedModes.webhook).toEqual(['custom'])
  expect(needsRecipient('sms')).toBe(true)
  expect(needsRecipient('email')).toBe(true)
  expect(needsRecipient('telegram')).toBe(false)
  expect(needsRecipient('webhook')).toBe(false)
})

// 新建模板默认取第一个允许的模式。内置的邮件类型只有 smtp，它只能发自定义模板：
// 默认成供应商模板的话，按默认值建出来的邮件模板每次发送都失败。
test('邮件的默认模板模式是自定义内容', () => {
  expect(allowedModes.email[0]).toBe('custom')
})
