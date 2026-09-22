import type { ReactNode } from 'react'
import { CircleAlert } from 'lucide-react'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

/** 贴在 Label 旁边的感叹号图标，悬浮显示该字段的说明/警告。 */
export function LabelHint({ children }: { children: ReactNode }) {
  return (
    <Tooltip>
      <TooltipTrigger className="text-muted-foreground" tabIndex={-1}>
        <CircleAlert className="size-3.5" />
      </TooltipTrigger>
      <TooltipContent>{children}</TooltipContent>
    </Tooltip>
  )
}
