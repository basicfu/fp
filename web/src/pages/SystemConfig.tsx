import { useState } from 'react'
import { Link } from 'react-router'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { useYamlEditor, validateYAML } from '@/lib/useYamlEditor'
import { cn } from '@/lib/utils'
import type { ConfigSnapshot, SaveConfigResponse } from '@/lib/types'

const PLACEHOLDER = [
  'env: dev                # dev / prod',
  'log:',
  '  level: info',
  'http:',
  '  addr: ":8080"',
  'grpc:',
  '  addr: ":9090"',
  'bootstrap_admin:',
  '  user: admin',
  '  password: admin',
  'sms:',
  '  aliyun:',
  '    access_key_id: ""',
].join('\n')

export default function SystemConfig() {
  const snapshot = useResource(() => api.get<ConfigSnapshot>('/system-config'), [])
  const { draft, setDraft, validationError, dirty, handleTextareaKeyDown } = useYamlEditor(snapshot.data)
  const [saving, setSaving] = useState(false)

  async function handleSave() {
    const err = validateYAML(draft)
    if (err) {
      toast.error(err)
      return
    }
    setSaving(true)
    try {
      const res = await api.put<SaveConfigResponse>('/system-config', { value: draft })
      toast.success(`已保存，seq=${res.seq}，重启 fp 后生效`)
      snapshot.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setSaving(false)
    }
  }

  if (snapshot.error) return <p className="text-sm text-destructive">{snapshot.error}</p>

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-2">
        <h1 className="text-lg font-medium">系统配置</h1>
        <div className="flex-1" />
        <Button variant="outline" render={<Link to="/system-config/versions" />}>
          版本历史
        </Button>
      </div>

      <p className="text-sm text-muted-foreground">
        fp 进程自身的启动配置——env、监听地址、首次启动的管理员账号、阿里云短信凭据。保存后需要重启 fp 才会生效。
      </p>

      {snapshot.data === null && snapshot.loading && <p className="text-sm text-muted-foreground">加载中…</p>}

      {snapshot.data !== null && (
        <>
          <Label htmlFor="system-config-yaml" className="sr-only">
            系统配置（YAML）
          </Label>
          <textarea
            id="system-config-yaml"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={handleTextareaKeyDown}
            spellCheck={false}
            placeholder={PLACEHOLDER}
            className={cn(
              'h-[60vh] max-h-[60vh] w-full resize-y overflow-y-auto rounded-lg border bg-transparent p-3 font-mono text-sm leading-relaxed outline-none',
              validationError ? 'border-destructive' : 'border-input',
            )}
          />

          <div className="flex items-center gap-2">
            <Button onClick={() => void handleSave()} disabled={saving || !dirty}>
              保存
            </Button>
          </div>
        </>
      )}
    </div>
  )
}
