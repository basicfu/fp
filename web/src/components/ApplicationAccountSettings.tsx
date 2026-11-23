import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Card, CardContent } from '@/components/ui/card'
import { LabelHint } from '@/components/ui/label-hint'
import { api } from '@/lib/api'
import { errorMessage } from '@/lib/useResource'
import { toastFormErrors } from '@/lib/formErrors'
import type { Application } from '@/lib/types'

const LOGIN_TYPES = [
  { value: 'sms', label: '短信验证码登录' },
  { value: 'password', label: '密码登录' },
  { value: 'wechat', label: '微信扫码登录' },
  { value: 'wechat_phone', label: '微信绑定手机号' },
]
const EDITABLE_FIELDS = [
  { value: 'nickname', label: '昵称' },
  { value: 'avatar', label: '头像' },
]

const schema = z
  .object({
    enabled: z.boolean(),
    domains: z.string(),
    loginTypes: z.array(z.string()),
    editableFields: z.array(z.string()),
    deviceBinding: z.boolean(),
    deviceHeader: z.string(),
    events: z.boolean(),
    wechatReturnUrl: z.string(),
  })
  .superRefine((v, ctx) => {
    // 与后端 AccountConfig.Validate 一致：关着时不校验，允许先存草稿。
    if (v.enabled && v.deviceBinding && v.deviceHeader.trim() === '') {
      ctx.addIssue({ code: 'custom', path: ['deviceHeader'], message: '开启设备绑定时必须填写设备头名称' })
    }
  })

type Values = z.infer<typeof schema>

function splitDomains(text: string): string[] {
  return text
    .split('\n')
    .map((d) => d.trim())
    .filter(Boolean)
}

export default function ApplicationAccountSettings({ app, onSaved }: { app: Application; onSaved: () => void }) {
  const { register, handleSubmit, watch, setValue, formState } = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: {
      enabled: app.account.enabled,
      domains: app.domains.join('\n'),
      loginTypes: app.account.loginTypes,
      editableFields: app.account.editableFields,
      deviceBinding: app.account.deviceBinding,
      deviceHeader: app.account.deviceHeader,
      events: app.account.events,
      wechatReturnUrl: app.account.wechatReturnUrl ?? '',
    },
  })

  const enabled = watch('enabled')
  const deviceBinding = watch('deviceBinding')
  const events = watch('events')
  const loginTypes = watch('loginTypes')
  const editableFields = watch('editableFields')

  function toggle(name: 'loginTypes' | 'editableFields', current: string[], value: string) {
    const next = current.includes(value) ? current.filter((x) => x !== value) : [...current, value]
    setValue(name, next, { shouldDirty: true })
  }

  async function onSubmit(v: Values) {
    try {
      await api.put(`/applications/${app.id}/account`, {
        domains: splitDomains(v.domains),
        account: {
          enabled: v.enabled,
          loginTypes: v.loginTypes,
          editableFields: v.editableFields,
          deviceBinding: v.deviceBinding,
          deviceHeader: v.deviceBinding ? v.deviceHeader.trim() : '',
          events: v.events,
          wechatReturnUrl: v.wechatReturnUrl.trim(),
        },
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
                每行一个，只填主机名（不带协议、端口、路径）。账号接口按请求的 Host 精确匹配来识别应用，
                域名在所有应用间唯一。
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

          <div className="flex items-center justify-between gap-4">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="acct-enabled">启用账号接口</Label>
              <LabelHint>关着时这些域名上的账号接口返回 404。</LabelHint>
            </div>
            <Switch
              id="acct-enabled"
              checked={enabled}
              onCheckedChange={(c) => setValue('enabled', c, { shouldDirty: true })}
            />
          </div>

          {enabled && (
            <>
              <fieldset className="space-y-2">
                <legend className="text-sm font-medium">允许的登录方式</legend>
                {LOGIN_TYPES.map((t) => (
                  <label key={t.value} className="flex items-center gap-2 text-sm">
                    <input
                      type="checkbox"
                      checked={loginTypes.includes(t.value)}
                      onChange={() => toggle('loginTypes', loginTypes, t.value)}
                    />
                    {t.label}
                  </label>
                ))}
              </fieldset>

              <fieldset className="space-y-2">
                <legend className="text-sm font-medium">用户可修改的资料</legend>
                {EDITABLE_FIELDS.map((f) => (
                  <label key={f.value} className="flex items-center gap-2 text-sm">
                    <input
                      type="checkbox"
                      checked={editableFields.includes(f.value)}
                      onChange={() => toggle('editableFields', editableFields, f.value)}
                    />
                    {f.label}
                  </label>
                ))}
              </fieldset>

              <div className="space-y-2">
                <div className="flex items-center gap-1.5">
                  <Label htmlFor="acct-wechatReturnUrl">扫码回跳地址</Label>
                  <LabelHint>
                    客户端发起微信扫码登录、绑定时用的回跳地址（如 myapp://wechat-login），必须同时填进微信登录方式的回跳白名单。留空则不开放扫码。
                  </LabelHint>
                </div>
                <Input id="acct-wechatReturnUrl" placeholder="myapp://wechat-login" {...register('wechatReturnUrl')} />
              </div>

              <div className="flex items-center justify-between gap-4 border-t pt-4">
                <div className="flex items-center gap-1.5">
                  <Label htmlFor="acct-deviceBinding">会话绑定设备</Label>
                  <LabelHint>
                    登录时记下设备公钥，之后每个请求都校验设备签名；登录前经 Watch 流向应用确认设备。
                  </LabelHint>
                </div>
                <Switch
                  id="acct-deviceBinding"
                  checked={deviceBinding}
                  onCheckedChange={(c) => setValue('deviceBinding', c, { shouldDirty: true })}
                />
              </div>

              {deviceBinding && (
                <div className="space-y-2">
                  <Label htmlFor="acct-deviceHeader">设备头名称</Label>
                  <Input id="acct-deviceHeader" placeholder="X-Device" {...register('deviceHeader')} />
                </div>
              )}

              <div className="flex items-center justify-between gap-4">
                <div className="flex items-center gap-1.5">
                  <Label htmlFor="acct-events">业务事件</Label>
                  <LabelHint>开启后登录、退出、注销等事件写入事件表，经 Watch 流可靠投递给该应用。</LabelHint>
                </div>
                <Switch
                  id="acct-events"
                  checked={events}
                  onCheckedChange={(c) => setValue('events', c, { shouldDirty: true })}
                />
              </div>
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
