import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Card, CardContent } from '@/components/ui/card'
import { LabelHint } from '@/components/ui/label-hint'
import { api } from '@/lib/api'
import { errorMessage } from '@/lib/useResource'
import { toastFormErrors } from '@/lib/formErrors'
import type { Application } from '@/lib/types'

const schema = z.object({
  domains: z.string(),
  deviceBinding: z.boolean(),
})

type Values = z.infer<typeof schema>

function splitDomains(text: string): string[] {
  return text
    .split('\n')
    .map((d) => d.trim())
    .filter(Boolean)
}

// 能用哪些登录方式看「登录方式」标签页的启停，昵称头像都允许改，这里不再逐项开权限。
export default function ApplicationAccountSettings({ app, onSaved }: { app: Application; onSaved: () => void }) {
  const { register, handleSubmit, watch, setValue, formState } = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: {
      domains: app.domains.join('\n'),
      deviceBinding: app.account.deviceBinding,
    },
  })

  const deviceBinding = watch('deviceBinding')

  async function onSubmit(v: Values) {
    try {
      await api.put(`/applications/${app.id}/account`, {
        domains: splitDomains(v.domains),
        account: { deviceBinding: v.deviceBinding },
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
          <div className="space-y-2">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="acct-domains">域名</Label>
              <LabelHint>
                每行一个，只填主机名（不带协议、端口、路径）。填了即对外开放账号接口，按请求的 Host
                精确匹配来识别应用，域名在所有应用间唯一。可用的登录方式看「登录方式」里的启停。
              </LabelHint>
            </div>
            <textarea
              id="acct-domains"
              rows={3}
              className="w-full rounded-md border bg-transparent px-3 py-2 text-sm"
              placeholder="api.example.com"
              {...register('domains')}
            />
          </div>

          <div className="flex items-center justify-between gap-4 border-t pt-4">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="acct-deviceBinding">验签</Label>
              <LabelHint>
                登录时记下设备公钥，之后每个请求都校验 X-Sign 头里的设备签名；登录前经 Watch 流向应用确认设备。
              </LabelHint>
            </div>
            <Switch
              id="acct-deviceBinding"
              checked={deviceBinding}
              onCheckedChange={(c) => setValue('deviceBinding', c, { shouldDirty: true })}
            />
          </div>

          <div className="flex items-center justify-between gap-4">
            <div className="flex items-center gap-1.5">
              <span className="text-sm font-medium">业务事件</span>
              <LabelHint>
                应用的 Watch 流声明接收业务事件后自动开启，此后登录、退出、注销等事件写入事件表并可靠投递。不会自动关闭。
              </LabelHint>
            </div>
            <span className="text-sm text-muted-foreground">{app.account.events ? '已开启' : '未开启'}</span>
          </div>

          <Button type="submit" disabled={formState.isSubmitting}>
            保存
          </Button>
        </form>
      </CardContent>
    </Card>
  )
}
