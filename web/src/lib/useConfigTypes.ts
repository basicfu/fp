import { api } from '@/lib/api'
import { useResource } from '@/lib/useResource'
import { DEFAULT_PARTITION, type ConfigPartition } from '@/lib/types'

/**
 * useConfigTypes 拉这个应用下已经保存过版本的分区列表（后端如实反映
 * 数据库里有什么），并且总是把 DEFAULT 排在最前面——即使这个应用还没对
 * DEFAULT 存过任何东西，它也是应用天然就有的分区，标签页要一直展示它、
 * 能被选中，不能因为"只列出真的存过版本的分区"这条后端规则而在 UI 上
 * 缺一个。
 *
 * ConfigCenter（编辑页）与 ConfigVersions（版本历史页）共用这一份逻辑：
 * 两边标签页显示的必须是同一份分区列表，不能各拉各的、萝卜青菜各算一遍。
 */
export function useConfigTypes(appId: string) {
  const resource = useResource(
    () =>
      appId
        ? api.get<ConfigPartition[]>(`/applications/${appId}/config/types`)
        : Promise.resolve<ConfigPartition[]>([]),
    [appId],
  )
  const types = resource.data
    ? [DEFAULT_PARTITION, ...resource.data.filter((t) => t !== DEFAULT_PARTITION)]
    : [DEFAULT_PARTITION]
  return { types, loading: resource.loading, error: resource.error, reload: resource.reload }
}
