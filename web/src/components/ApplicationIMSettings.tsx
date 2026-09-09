import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Card, CardContent } from '@/components/ui/card'
import { api } from '@/lib/api'
import { errorMessage } from '@/lib/useResource'
import { toastFormErrors } from '@/lib/formErrors'
import type { Application } from '@/lib/types'

// verifyUrl 必须是 https：client 的令牌明文走在请求体里，明文传输等于把
// 所有业务方令牌交给中间人。后端也会拦，这里拦一道是为了不让人白填一屏
// 再被服务端打回来。
const schema = z
  .object({
    enabled: z.boolean(),
    connPolicy: z.enum(['replace', 'reject', 'limit']),
    connLimit: z.coerce.number().int(),
    allowGuest: z.boolean(),
    guestIpRate: z.coerce.number().int(),
    bizAuthEnabled: z.boolean(),
    verifyUrl: z.string(),
    timeoutMs: z.coerce.number().int(),
    cacheSize: z.coerce.number().int(),
  })
  .superRefine((v, ctx) => {
    // 关着的时候不校验其余项：还没打开就先拦人，等于逼人一次填全才能存
    // 草稿。与后端 domain.IMConfig.Validate 的取舍一致。
    if (!v.enabled) return
    if (v.connPolicy === 'limit' && v.connLimit < 1) {
      ctx.addIssue({ code: 'custom', path: ['connLimit'], message: '策略为 limit 时并发上限必须 ≥ 1' })
    }
    if (v.allowGuest && v.guestIpRate < 1) {
      ctx.addIssue({ code: 'custom', path: ['guestIpRate'], message: '允许访客时限流必须 ≥ 1' })
    }
    if (!v.bizAuthEnabled) return
    if (!v.verifyUrl.startsWith('https://')) {
      ctx.addIssue({ code: 'custom', path: ['verifyUrl'], message: '回调地址必须是 https' })
    }
    if (v.timeoutMs <= 0) {
      ctx.addIssue({ code: 'custom', path: ['timeoutMs'], message: '超时必须大于 0' })
    }
    if (v.cacheSize <= 0) {
      ctx.addIssue({ code: 'custom', path: ['cacheSize'], message: '缓存容量必须大于 0' })
    }
  })

type Values = z.infer<typeof schema>

export default function ApplicationIMSettings({ app, onSaved }: { app: Application; onSaved: () => void }) {
  const { register, handleSubmit, watch, setValue, formState } = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: {
      enabled: app.im.enabled,
      connPolicy: app.im.connPolicy,
      connLimit: app.im.connLimit,
      allowGuest: app.im.allowGuest,
      guestIpRate: app.im.guestIpRate,
      bizAuthEnabled: app.im.bizAuth !== null,
      verifyUrl: app.im.bizAuth?.verifyUrl ?? '',
      timeoutMs: app.im.bizAuth?.timeoutMs ?? 2000,
      cacheSize: app.im.bizAuth?.cacheSize ?? 10000,
    },
  })

  const enabled = watch('enabled')
  const connPolicy = watch('connPolicy')
  const allowGuest = watch('allowGuest')
  const bizAuthEnabled = watch('bizAuthEnabled')

  async function onSubmit(v: Values) {
    try {
      await api.put(`/applications/${app.id}/im`, {
        enabled: v.enabled,
        connPolicy: v.connPolicy,
        connLimit: v.connLimit,
        allowGuest: v.allowGuest,
        guestIpRate: v.guestIpRate,
        // 整组置 null 而不是留一份空对象：null 是"不支持业务方令牌"的
        // 唯一表示，一路对应到库里的 SQL NULL。
        bizAuth: v.bizAuthEnabled
          ? { verifyUrl: v.verifyUrl, timeoutMs: v.timeoutMs, cacheSize: v.cacheSize }
          : null,
      })
      toast.success('已保存')
      onSaved()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <Card>
      <CardContent className="pt-6">
        <form onSubmit={handleSubmit(onSubmit, toastFormErrors)} className="max-w-md space-y-5" noValidate>
          <div className="flex items-center justify-between gap-4">
            <div className="space-y-1">
              <Label htmlFor="im-enabled">启用 IM 接入</Label>
              <p className="text-xs text-muted-foreground">
                关着时这个应用连不上 fp-im：业务 server 接不进来，client 握手也会被拒。
                关闭**不会**断开已在线的连接，只挡新连接。
              </p>
            </div>
            <Switch
              id="im-enabled"
              checked={enabled}
              onCheckedChange={(c) => setValue('enabled', c, { shouldDirty: true })}
            />
          </div>

          {enabled && (
            <>
              <div className="space-y-2">
                <Label htmlFor="im-connPolicy">连接策略</Label>
                <select
                  id="im-connPolicy"
                  className="h-9 w-full rounded-md border bg-transparent px-3 text-sm"
                  {...register('connPolicy')}
                >
                  <option value="replace">replace —— 新连接顶掉同一主体的旧连接</option>
                  <option value="reject">reject —— 已有连接时拒绝新连接</option>
                  <option value="limit">limit —— 限制并发条数，超出拒新</option>
                </select>
              </div>

              {connPolicy === 'limit' && (
                <div className="space-y-2">
                  <Label htmlFor="im-connLimit">并发上限</Label>
                  <Input id="im-connLimit" type="number" {...register('connLimit')} />
                </div>
              )}

              <div className="flex items-center justify-between gap-4">
                <div className="space-y-1">
                  <Label htmlFor="im-allowGuest">允许访客</Label>
                  <p className="text-xs text-muted-foreground">访客握手不带 token，只按 IP 限流。</p>
                </div>
                <Switch
                  id="im-allowGuest"
                  checked={allowGuest}
                  onCheckedChange={(c) => setValue('allowGuest', c, { shouldDirty: true })}
                />
              </div>

              {allowGuest && (
                <div className="space-y-2">
                  <Label htmlFor="im-guestIpRate">访客限流（每 IP）</Label>
                  <Input id="im-guestIpRate" type="number" {...register('guestIpRate')} />
                </div>
              )}

              <div className="flex items-center justify-between gap-4 border-t pt-4">
                <div className="space-y-1">
                  <Label htmlFor="im-bizAuth">业务方令牌</Label>
                  <p className="text-xs text-muted-foreground">
                    开启后，client 用 kind:biz 握手时网关会回调你的接口验证。
                  </p>
                </div>
                <Switch
                  id="im-bizAuth"
                  checked={bizAuthEnabled}
                  onCheckedChange={(c) => setValue('bizAuthEnabled', c, { shouldDirty: true })}
                />
              </div>

              {bizAuthEnabled && (
                <>
                  <div className="space-y-2">
                    <Label htmlFor="im-verifyUrl">回调地址</Label>
                    <Input id="im-verifyUrl" placeholder="https://biz.example.com/verify" {...register('verifyUrl')} />
                    <p className="text-xs text-muted-foreground">
                      必须是 https —— 令牌明文走在请求体里。
                    </p>
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor="im-timeoutMs">回调超时（毫秒）</Label>
                    <Input id="im-timeoutMs" type="number" {...register('timeoutMs')} />
                    <p className="text-xs text-muted-foreground">
                      必须明显小于握手的 5 秒上限：配大了 client 会先被握手超时踢掉，
                      拿到的关闭码方向完全反了。
                    </p>
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor="im-cacheSize">验证结果缓存容量</Label>
                    <Input id="im-cacheSize" type="number" {...register('cacheSize')} />
                  </div>
                </>
              )}
            </>
          )}

          <Button type="submit" disabled={formState.isSubmitting}>
            保存
          </Button>
        </form>
      </CardContent>
    </Card>
  )
}
