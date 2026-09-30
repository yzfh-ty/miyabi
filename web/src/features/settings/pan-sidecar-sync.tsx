import { useEffect, useMemo, useState } from 'react'
import { ChevronLeftIcon, ChevronRightIcon, CloudDownloadIcon, LoaderCircleIcon } from 'lucide-react'

import {
  type PanDirectory,
  type PanSidecarSyncConfig,
  usePanFiles,
  usePanSidecarSyncConfig,
  useRunPanSidecarSync,
  useUpdatePanSidecarSync
} from '@/api/pan'
import { InlineError } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { SettingRow } from './shared'

export function PanSidecarSync({ accountID, parent }: { accountID: string; parent: PanDirectory }) {
  const config = usePanSidecarSyncConfig()
  const [page, setPage] = useState(1)
  const files = usePanFiles(accountID, parent.id, page)
  const update = useUpdatePanSidecarSync()
  const run = useRunPanSidecarSync()
  const [form, setForm] = useState<Pick<PanSidecarSyncConfig,
    'enabled' | 'destination' | 'child_directories' | 'interval_minutes'> | null>(null)

  useEffect(() => {
    if (config.data && form === null) {
      setForm({
        enabled: config.data.enabled,
        destination: config.data.destination,
        child_directories: config.data.account_id === accountID && config.data.parent_id === parent.id
          ? config.data.child_directories
          : [],
        interval_minutes: config.data.interval_minutes || 30
      })
    }
  }, [accountID, config.data, form, parent.id])

  const current = form ?? {
    enabled: false,
    destination: '',
    child_directories: [] as PanDirectory[],
    interval_minutes: 30
  }
  const selected = useMemo(
    () => new Map(current.child_directories.map(directory => [directory.id, directory] as const)),
    [current.child_directories]
  )
  const isDirty = Boolean(form && config.data && (
    current.enabled !== config.data.enabled ||
    current.destination !== config.data.destination ||
    current.interval_minutes !== (config.data.interval_minutes || 30) ||
    JSON.stringify(current.child_directories.map(item => item.id).sort()) !==
      JSON.stringify((config.data.account_id === accountID && config.data.parent_id === parent.id
        ? config.data.child_directories.map(item => item.id)
        : []).sort())
  ))
  const folders = files.data?.files.filter(file => file.is_directory) ?? []

  function change<K extends keyof typeof current>(key: K, value: (typeof current)[K]) {
    setForm(prev => ({ ...(prev ?? current), [key]: value }))
  }

  function toggleDirectory(id: string, name: string, checked: boolean) {
    const next = new Map(selected)
    if (checked) next.set(id, { id, name, path: name })
    else next.delete(id)
    change('child_directories', [...next.values()].sort((a, b) => a.name.localeCompare(b.name)))
  }

  function save() {
    update.mutate({
      enabled: current.enabled,
      destination: current.destination.trim(),
      child_directories: current.child_directories,
      interval_minutes: current.interval_minutes
    }, {
      onSuccess: next => setForm({
        enabled: next.enabled,
        destination: next.destination,
        child_directories: next.child_directories,
        interval_minutes: next.interval_minutes
      })
    })
  }

  const busy = config.isLoading || update.isPending || run.isPending

  return (
    <div className="space-y-4 rounded-md border p-4">
      <div className="text-sm font-medium">115 旁挂 NFO 与图片同步</div>
      <SettingRow title="启用同步" description="只下载所选子目录内的 NFO 和图片；本地已有文件始终跳过" inline>
        <Switch checked={current.enabled} disabled={busy || config.isError} onCheckedChange={value => change('enabled', value)} />
      </SettingRow>
      <SettingRow title="本地同步目录" description="填写运行 Miyabi 的服务器上的绝对路径">
        <Input value={current.destination} disabled={busy || config.isError} placeholder="/data/115-sidecars" onChange={event => change('destination', event.target.value)} />
      </SettingRow>
      <SettingRow title="同步间隔（分钟）" description="每隔指定时间检查白名单目录，默认 30 分钟">
        <Input type="number" min={1} max={1440} value={current.interval_minutes} disabled={busy || config.isError} onChange={event => change('interval_minutes', Number(event.target.value))} />
      </SettingRow>
      <div className="space-y-2">
        <div className="text-xs text-muted-foreground">选择挂载目录的直接子目录；未选择的目录及其内容不会下载。</div>
        {files.isLoading ? <LoaderCircleIcon className="size-4 animate-spin text-muted-foreground" /> : null}
        {files.isError ? <InlineError>无法读取挂载目录，请检查 115 连接后重试。</InlineError> : null}
        {folders.map(folder => (
          <label key={folder.id} className="flex items-center gap-2 text-sm">
            <Checkbox
              checked={selected.has(folder.id)}
              disabled={busy || config.isError || files.isError || files.isFetching}
              onCheckedChange={checked => toggleDirectory(folder.id, folder.name, checked === true)}
            />
            <span>{folder.name}</span>
          </label>
        ))}
        {!files.isLoading && !files.isError && folders.length === 0 ? (
          <div className="text-xs text-muted-foreground">当前页没有直接子目录。</div>
        ) : null}
      </div>
      <div className="flex flex-wrap items-center justify-end gap-2">
        {config.data?.last_run_at ? (
          <span className="mr-auto text-xs text-muted-foreground">
            最近同步：{new Date(config.data.last_run_at).toLocaleString()} · 下载 {config.data.files_downloaded} · 跳过 {config.data.files_skipped}
          </span>
        ) : null}
        <Button type="button" variant="outline" size="sm" disabled={busy || isDirty || !config.data?.enabled || selected.size === 0} onClick={() => run.mutate()}>
          {run.isPending ? <LoaderCircleIcon className="mr-1.5 size-3.5 animate-spin" /> : <CloudDownloadIcon className="mr-1.5 size-3.5" />}
          立即同步
        </Button>
        <Button type="button" size="sm" disabled={busy || !isDirty} onClick={save}>
          {update.isPending ? <LoaderCircleIcon className="mr-1.5 size-3.5 animate-spin" /> : null}
          保存
        </Button>
      </div>
      {files.data ? (
        <div className="flex items-center justify-end gap-2">
          <span className="mr-auto text-xs text-muted-foreground">
            第 {page} 页 · 共 {files.data.total} 项
          </span>
          <Button type="button" variant="ghost" size="icon-sm" disabled={page <= 1 || files.isFetching || busy} onClick={() => setPage(value => value - 1)}>
            <ChevronLeftIcon className="size-4" />
          </Button>
          <Button type="button" variant="ghost" size="icon-sm" disabled={!files.data.has_more || files.isFetching || busy} onClick={() => setPage(value => value + 1)}>
            <ChevronRightIcon className="size-4" />
          </Button>
        </div>
      ) : null}
      {config.data?.last_result && config.data.last_result !== 'success' ? (
        <InlineError>最近同步有文件处理失败：{config.data.last_result}</InlineError>
      ) : null}
      {config.data?.errors?.slice(0, 3).map((error, index) => (
        <div key={`${index}-${error}`} className="text-xs text-destructive">{error}</div>
      ))}
      {config.isError ? <InlineError>无法读取同步配置，请检查后端服务。</InlineError> : null}
      {update.isError ? <InlineError>同步配置保存失败，请检查目录路径和所选子目录。</InlineError> : null}
      {run.isError ? <InlineError>同步未完成，请查看最近同步状态和后端日志。</InlineError> : null}
    </div>
  )
}
