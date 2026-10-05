import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * NativeSelect 是原生 <select> 加上与 Input 一致的外观。
 *
 * 通知中心的表单里有一堆只有两三个取值的枚举（渠道、模式、请求方式……），用原生控件
 * 就够了：键盘与读屏开箱即用，测试里也不用模拟 base-ui Select 的指针事件序列。
 */
export function NativeSelect({ className, ...props }: ComponentProps<'select'>) {
  return (
    <select
      className={cn(
        'h-8 w-full min-w-0 rounded-lg border border-input bg-transparent px-2 text-sm outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 dark:bg-input/30',
        className,
      )}
      {...props}
    />
  )
}
