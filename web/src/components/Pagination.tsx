import { Button } from '@/components/ui/button'

interface Props {
  page: number
  total: number
  pageSize: number
  onPage: (p: number) => void
}

/**
 * 极简分页：上一页 / 下一页 / 第 x 页共 y 页。
 *
 * 没做页码跳转按钮——用户列表主要靠搜索定位，翻很多页的场景不存在。
 * 真需要时再加。
 */
export default function Pagination({ page, total, pageSize, onPage }: Props) {
  const pages = Math.max(1, Math.ceil(total / pageSize))
  return (
    <div className="flex items-center justify-end gap-3 text-sm">
      <span className="text-muted-foreground">
        共 {total} 条，第 {page} / {pages} 页
      </span>
      <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => onPage(page - 1)}>
        上一页
      </Button>
      <Button variant="outline" size="sm" disabled={page >= pages} onClick={() => onPage(page + 1)}>
        下一页
      </Button>
    </div>
  )
}
