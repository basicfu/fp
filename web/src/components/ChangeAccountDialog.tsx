import { useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { api } from '@/lib/api'
import { useAuth } from '@/lib/auth'
import { toastFormErrors } from '@/lib/formErrors'
import { errorMessage } from '@/lib/useResource'

const schema = z
  .object({
    username: z.string().trim().min(1, '请输入登录名').max(64, '登录名最长 64 个字符'),
    oldPassword: z.string().min(1, '请输入旧密码'),
    newPassword: z.string(),
    confirmPassword: z.string(),
  })
  .refine((v) => v.newPassword === v.confirmPassword, {
    message: '两次输入的新密码不一致',
    path: ['confirmPassword'],
  })
type Values = z.infer<typeof schema>

/** 右上角「修改密码」：登录名与密码在同一个对话框里改，新密码留空表示只改登录名。 */
export function ChangeAccountDialog() {
  const { username, accountDialogOpen, setAccountDialogOpen, endSession } = useAuth()
  const { register, handleSubmit, reset, formState } = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { username: username ?? '', oldPassword: '', newPassword: '', confirmPassword: '' },
  })

  // 每次打开都重置：上次填的旧密码不该留在表单里，登录名要跟着最新值走。
  useEffect(() => {
    if (accountDialogOpen) {
      reset({ username: username ?? '', oldPassword: '', newPassword: '', confirmPassword: '' })
    }
  }, [accountDialogOpen, username, reset])

  async function onSubmit(v: Values) {
    try {
      await api.put('/me', {
        username: v.username,
        oldPassword: v.oldPassword,
        newPassword: v.newPassword,
      })
      // 后端已作废全部会话（含当前这个）：不再给本页换新会话，回登录页用新凭据重进。
      toast.success('账号信息已修改，请用新的登录名和密码重新登录')
      endSession()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <Dialog open={accountDialogOpen} onOpenChange={setAccountDialogOpen}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>修改账号</DialogTitle>
        </DialogHeader>
        <form
          id="change-account-form"
          onSubmit={handleSubmit(onSubmit, toastFormErrors)}
          className="space-y-4"
          noValidate
        >
          <div className="space-y-2">
            <Label htmlFor="ca-username">登录名</Label>
            <Input id="ca-username" autoComplete="username" {...register('username')} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="ca-old">旧密码</Label>
            <Input id="ca-old" type="password" autoComplete="current-password" {...register('oldPassword')} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="ca-new">新密码</Label>
            <Input
              id="ca-new"
              type="password"
              autoComplete="new-password"
              placeholder="留空表示不改密码"
              {...register('newPassword')}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="ca-confirm">确认新密码</Label>
            <Input id="ca-confirm" type="password" autoComplete="new-password" {...register('confirmPassword')} />
          </div>
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={() => setAccountDialogOpen(false)}>
            取消
          </Button>
          <Button type="submit" form="change-account-form" disabled={formState.isSubmitting}>
            保存
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
