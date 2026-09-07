import { useEffect, useState } from 'react'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

export const THEME_COLORS = [
  { value: 'blue', label: '科技蓝' },
  { value: 'violet', label: '紫罗兰' },
  { value: 'emerald', label: '翡翠绿' },
  { value: 'mono', label: '极简黑白' },
] as const

export type ThemeColor = (typeof THEME_COLORS)[number]['value']

const STORAGE_KEY = 'fp-ui-theme'
const DEFAULT_THEME: ThemeColor = 'blue'

function isThemeColor(v: string | null): v is ThemeColor {
  return THEME_COLORS.some((t) => t.value === v)
}

/** 和 index.html 里防闪烁脚本读取的是同一个来源，保证初始值一致。 */
function currentTheme(): ThemeColor {
  const attr = document.documentElement.getAttribute('data-theme')
  return isThemeColor(attr) ? attr : DEFAULT_THEME
}

export default function ThemeColorSwitcher() {
  const [theme, setThemeState] = useState<ThemeColor>(DEFAULT_THEME)

  useEffect(() => {
    setThemeState(currentTheme())
  }, [])

  return (
    <Select
      items={THEME_COLORS}
      value={theme}
      onValueChange={(v) => {
        if (!isThemeColor(v)) return
        document.documentElement.setAttribute('data-theme', v)
        localStorage.setItem(STORAGE_KEY, v)
        setThemeState(v)
      }}
    >
      <SelectTrigger aria-label="切换主题色" className="w-28">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {THEME_COLORS.map((t) => (
          <SelectItem key={t.value} value={t.value}>
            {t.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
