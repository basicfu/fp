import { useEffect, useState, type KeyboardEvent } from 'react'
import { load as loadYAML } from 'js-yaml'
import type { ConfigSnapshot } from '@/lib/types'

/**
 * normalizeYAML 给"key:value"这种冒号后漏了空格的行补上那个空格（列表
 * 项前缀、缩进都保留，注释行不动）——跟后端 domain.NormalizeConfigYAML
 * 是同一条启发式规则的前端版本，只用来在打字时就让校验通过，真正落库的
 * 补全动作由后端做一遍权威的。
 */
export function normalizeYAML(text: string): string {
  return text
    .split('\n')
    .map((line) => {
      const trimmed = line.trimStart()
      if (trimmed === '' || trimmed.startsWith('#')) return line
      return line.replace(/^(\s*(?:-\s+)?[^:\s][^:]*):(\S)/, '$1: $2')
    })
    .join('\n')
}

/**
 * validateYAML 只做一件事：这段文本（补完冒号空格之后）能不能被解析、且
 * 顶层是不是一个映射。不做任何值级别的校验——YAML 自己的字面量语法就是
 * 类型信息。跟后端 domain.ParseConfigYAML 是同一条校验规则的前端版本，
 * 只是提前到打字时就告诉人，不用等点保存才知道。
 */
export function validateYAML(text: string): string {
  const normalized = normalizeYAML(text)
  if (normalized.trim() === '') return ''
  let parsed: unknown
  try {
    parsed = loadYAML(normalized)
  } catch (e) {
    return e instanceof Error ? e.message : '不是合法的 YAML'
  }
  if (parsed === null || parsed === undefined) return ''
  if (typeof parsed !== 'object' || Array.isArray(parsed)) {
    return '顶层必须是一个映射（key: value 的形式），不能是列表或裸标量'
  }
  return ''
}

/**
 * useYamlEditor 是 ConfigCenter（应用配置中心）与 SystemConfig（fp 自身
 * 系统配置）共用的 YAML 编辑框状态。draft 是编辑框里的原文；remote 一到
 * （首次加载、切分区/重新拉取、保存成功后的 reload）就用它整体覆盖
 * draft——不做"合并本地改动"的尝试，YAML 编辑框本来就是"整份替换"的
 * 心智模型，不是逐字段增量编辑。
 *
 * 依赖的是 remote 这个对象引用本身（不是拆出来的字符串）：调用方每次
 * reload 都会拿到一个新对象，即使内容没变，这样才能保证"保存成功后
 * dirty 归零"在内容意外没变时依然生效。
 */
export function useYamlEditor(remote: ConfigSnapshot | null) {
  const [draft, setDraft] = useState('')

  useEffect(() => {
    if (!remote) return
    setDraft(remote.value)
  }, [remote])

  const validationError = validateYAML(draft)
  const dirty = remote !== null && draft !== remote.value

  /** Tab 在这个编辑框里是缩进，不是切到下一个控件——YAML 靠缩进表达层级，浏览器默认的"移走焦点"在这没用。 */
  function handleTextareaKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key !== 'Tab') return
    e.preventDefault()
    const el = e.currentTarget
    const start = el.selectionStart
    const end = el.selectionEnd
    setDraft(draft.slice(0, start) + '  ' + draft.slice(end))
    requestAnimationFrame(() => {
      el.selectionStart = el.selectionEnd = start + 2
    })
  }

  return { draft, setDraft, validationError, dirty, handleTextareaKeyDown }
}
