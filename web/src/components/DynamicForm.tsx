import { useId } from 'react'
import { Controller, useForm } from 'react-hook-form'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { toastFormErrors } from '@/lib/formErrors'
import type { Field } from '@/lib/types'

type Values = Record<string, unknown>

interface Props {
  /** 后端 ConfigSchema() 声明的字段。 */
  fields: Field[]
  /** 该应用当前已保存的配置；未配置过时传 {}。 */
  values: Values
  onSubmit: (config: Values) => void | Promise<void>
  submitLabel?: string
}

/**
 * initialValue 决定一个字段的初始状态。
 *
 * 优先级：已保存的配置 > schema 声明的 default > 按类型兜底。
 * 顺序不能反——反了的话，任何被管理员显式关掉的开关，每次打开页面
 * 都会重新显示成"开"。
 */
function initialValue(f: Field, saved: Values): unknown {
  if (Object.prototype.hasOwnProperty.call(saved, f.key)) return saved[f.key]
  if (f.default !== undefined && f.default !== null) return f.default
  return f.type === 'bool' ? false : ''
}

/**
 * DynamicForm 按后端声明的字段元数据渲染配置表单。
 *
 * 存在的理由：新增一种登录方式时，后端写好 ConfigSchema() 就够了，
 * 前端不需要任何改动。所以这里**不许**出现任何针对具体 connector 的
 * 特判——一旦出现，这个组件就退化成了几个写死表单的集合。
 *
 * 约束：Field.Key 必须是简单标识符，不能含 `.` 或 `[`——react-hook-form
 * 会把它们解释成嵌套路径。后端所有 ConfigSchema 目前都满足。
 *
 * bool 字段的可访问名：<Switch id={f.key}> 底层是 base-ui，nativeButton
 * 默认 false 时会把这个 id 挪给内部隐藏的原生 checkbox，可见的
 * role="switch" 根元素用的是自己生成的 id；但 base-ui 会自动探测兄弟
 * <Label htmlFor={f.key}>，把它接到根元素的 aria-labelledby 上（具体是
 * `${htmlFor}-label` 这个 id）。所以测试要用 getByRole('switch', { name })
 * 定位（只有根元素带 role="switch"，隐藏 checkbox 是 aria-hidden，不会被
 * role 查询命中），不能用 getByLabelText（隐藏 checkbox 经 label[for] 也
 * 关联得上，两个元素都命中会报"找到多个元素"）。这样 Label 保留 htmlFor，
 * 点文案依然能切换开关。
 *
 * 【终审必须修】DOM id 必须按组件实例命名空间化，不能直接用 f.key。
 * ConnectorsPanel 会把每个已注册 connector 的 DynamicForm 渲染在同一个
 * 页面上；两个 connector 各自声明一个同名字段（"enabled"/"autoRegister"
 * 都是极自然的命名）时，若两处都直接 id={f.key}，两个开关会共享同一个
 * `${f.key}-label` id，浏览器/jsdom 对重复 id 的解析会让两个开关的
 * 可访问名都解析成文档序里第一个——点第二个连带把第一个也翻了，且是在
 * "登录方式"这种直接改后端落库配置的页面上。用 useId() 而不是让调用方
 * 传 idPrefix：后者要求 ConnectorsPanel 记得传且传的值全局唯一，前者由
 * React 保证每个组件实例天然唯一，不存在"调用方忘了传"这一整类回归。
 */
export function DynamicForm({ fields, values, onSubmit, submitLabel = '保存' }: Props) {
  const uid = useId()
  const domId = (key: string) => `${uid}-${key}`

  const defaults: Values = {}
  for (const f of fields) defaults[f.key] = initialValue(f, values)

  const { register, control, handleSubmit, formState } = useForm<Values>({ defaultValues: defaults })

  function buildPayload(raw: Values): Values {
    const out: Values = {}
    for (const f of fields) {
      const v = raw[f.key]
      if (f.type === 'bool') {
        // 开关没有"空"状态：界面显示什么就提交什么，否则界面在撒谎。
        out[f.key] = Boolean(v)
        continue
      }
      if (f.type === 'int') {
        // <input type="number"> 给出来的是字符串。不转成 number 的话，
        // 后端 decodeJSON 会因类型不符返回一个看不出原因的 400。
        if (v === '' || v === undefined || v === null) continue
        out[f.key] = Number(v)
        continue
      }
      // string / secret / 未知类型
      if (v === '' || v === undefined || v === null) {
        // 非必填留空 → 省略该键，让 connector 用自己代码里的默认值。
        // 写成空串的话，会把 connector 的默认值覆盖成""。
        if (!f.required) continue
      }
      out[f.key] = v
    }
    return out
  }

  return (
    <form
      onSubmit={handleSubmit((raw) => onSubmit(buildPayload(raw)), toastFormErrors)}
      className="space-y-4"
      noValidate
    >
      {fields.map((f) => (
        <div key={f.key} className="space-y-2">
          {f.type === 'bool' ? (
            <div className="flex items-center gap-3">
              <Controller
                control={control}
                name={f.key}
                render={({ field }) => (
                  <Switch
                    id={domId(f.key)}
                    checked={Boolean(field.value)}
                    onCheckedChange={field.onChange}
                  />
                )}
              />
              <Label htmlFor={domId(f.key)}>{f.label}</Label>
            </div>
          ) : (
            <>
              <Label htmlFor={domId(f.key)}>
                {f.label}
                {f.required && <span className="ml-1 text-destructive">*</span>}
              </Label>
              <Input
                id={domId(f.key)}
                type={inputType(f.type)}
                {...register(f.key, {
                  required: f.required ? `${f.label}不能为空` : false,
                })}
              />
            </>
          )}
          {f.help && <p className="text-xs text-muted-foreground">{f.help}</p>}
        </div>
      ))}
      <Button type="submit" disabled={formState.isSubmitting}>
        {submitLabel}
      </Button>
    </form>
  )
}

/**
 * inputType 把后端的字段类型映射成 input 的 type。
 *
 * default 分支刻意退化成 text 而不是抛错：这个控制台的卖点就是"后端加
 * 登录方式、前端零改动"。将来后端加一种新 FieldType 时，旧版前端应当
 * 还能把它当字符串编辑，而不是整页崩掉——崩掉会连带把同一页上本来能
 * 正常配置的其他登录方式一起带走。
 */
function inputType(t: string): string {
  switch (t) {
    case 'int':
      return 'number'
    case 'secret':
      return 'password'
    default:
      return 'text'
  }
}
