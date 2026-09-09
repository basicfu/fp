import type { IMConfig } from '@/lib/types'

/**
 * disabledIMConfig 是新建应用的 IM 接入默认值：关着。
 *
 * 抽成共享常量而不是让每个测试各写一份：Application.im 加字段时，散落的
 * 副本会一个个报类型错误，而共享常量只需改一处。测试夹具的重复没有"各自
 * 表达不同意图"的正当理由——它们全都只是"一个没开 IM 的普通应用"。
 */
export const disabledIMConfig: IMConfig = {
  enabled: false,
  connPolicy: 'replace',
  connLimit: 5,
  allowGuest: false,
  guestIpRate: 20,
  bizAuth: null,
}
