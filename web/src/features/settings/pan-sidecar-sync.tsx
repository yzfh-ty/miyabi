import { useMemo, useState } from 'react'
import {
  ChevronLeftIcon,
  ChevronRightIcon,
  CloudDownloadIcon,
  LoaderCircleIcon
} from 'lucide-react'
import { toast } from 'sonner'

import { describeApiError } from '@/api/client'
import {
  type PanDirectory,
  type PanSidecarSyncUpdate,
  usePanFiles,
  usePanSidecarSyncConfig,
  useRunPanSidecarSync,
  useUpdatePanSidecarSync
} from '@/api/pan'
import { InlineError } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { SettingRow } from './shared'

const emptyDirectory: PanDirectory = { id: '', name: '', path: '' }

export function PanSidecarSync({ accountID, parent }: { accountID: string; parent: PanDirectory }) {
  const config = usePanSidecarSyncConfig()
  const [page, setPage] = useState(1)
  const files = usePanFiles(accountID, parent.id, page)
  const update = useUpdatePanSidecarSync()
  const run = useRunPanSidecarSync()
  const [form, setForm] = useState<PanSidecarSyncUpdate | null>(null)
  const sameMount = config.data?.account_id === accountID && config.data.parent_id === parent.id
  const mountedConfig = sameMount ? config.data : undefined

  const initial: PanSidecarSyncUpdate = {
    enabled: config.data?.enabled ?? false,
    destination: config.data?.destination ?? '',
    download_directory: mountedConfig?.download_directory ?? emptyDirectory,
    child_directories: mountedConfig?.child_directories ?? [],
    interval_minutes: config.data?.interval_minutes || 30
  }
  const current = form ?? initial
  const selected = useMemo(
    () => new Map(current.child_directories.map(directory => [directory.id, directory] as const)),
    [current.child_directories]
  )
  const isDirty = Boolean(
    config.data &&
    (!sameMount ||
      (form !== null &&
        (current.enabled !== config.data.enabled ||
          current.destination !== config.data.destination ||
          current.download_directory.id !== (sameMount ? config.data.download_directory.id : '') ||
          current.interval_minutes !== (config.data.interval_minutes || 30) ||
          JSON.stringify(
            current.child_directories.map(item => [item.id, item.mode ?? 'sync']).sort()
          ) !==
            JSON.stringify(
              (sameMount
                ? config.data.child_directories.map(item => [item.id, item.mode ?? 'sync'])
                : []
              ).sort()
            ))))
  )
  if (form !== null && !isDirty && !update.isPending) setForm(null)
  const folders = files.data?.files.filter(file => file.is_directory) ?? []

  function change<K extends keyof typeof current>(key: K, value: (typeof current)[K]) {
    setForm(prev => ({ ...(prev ?? current), [key]: value }))
  }

  function setDirectoryMode(id: string, name: string, mode: string) {
    const next = new Map(selected)
    if (mode === 'scrape' || mode === 'sync') next.set(id, { id, name, path: name, mode })
    else next.delete(id)
    change(
      'child_directories',
      [...next.values()].sort((a, b) => a.name.localeCompare(b.name))
    )
  }

  function chooseDownload(directory: PanDirectory) {
    setForm({
      ...current,
      download_directory: directory,
      child_directories: current.child_directories.filter(item => item.id !== directory.id)
    })
  }

  function save() {
    update.mutate(
      {
        enabled: current.enabled,
        destination: current.destination.trim(),
        download_directory: current.download_directory,
        child_directories: current.child_directories,
        interval_minutes: current.interval_minutes
      },
      {
        onSuccess: next => {
          setForm({
            enabled: next.enabled,
            destination: next.destination,
            download_directory: next.download_directory,
            child_directories: next.child_directories,
            interval_minutes: next.interval_minutes
          })
          toast.success('目录设置已保存')
        }
      }
    )
  }

  const busy = config.isLoading || update.isPending || run.isPending

  return (
    <div className="space-y-4 rounded-md border p-4">
      <div className="text-sm font-medium">115 下载目录与媒体处理</div>
      <SettingRow
        title="磁链下载目录"
        description="手动磁链下载及订阅自动下载共用此目录；默认仅此目录及下级进行刮削"
      >
        <div className="flex flex-wrap items-center gap-2">
          <span className="min-w-0 text-sm break-words">
            {current.download_directory.name || '未选择，下载到媒体根目录并仅同步元数据'}
          </span>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={busy || config.isError || current.download_directory.id === parent.id}
            onClick={() => chooseDownload(parent)}
          >
            使用媒体根目录
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            disabled={busy || config.isError || !current.download_directory.id}
            onClick={() => chooseDownload(emptyDirectory)}
          >
            取消选择
          </Button>
        </div>
      </SettingRow>
      <SettingRow
        title="启用 STRM 与元数据同步"
        description="其他目录默认保留原结构，生成同名 STRM 并下载 115 已有 NFO 和图片；本地已有文件跳过"
        inline
      >
        <Switch
          checked={current.enabled}
          disabled={busy || config.isError}
          onCheckedChange={value => change('enabled', value)}
        />
      </SettingRow>
      <SettingRow
        title="本地同步目录（可选）"
        description={`留空自动使用 Emby 本地输出目录${config.data?.default_destination ? ` ${config.data.default_destination}` : ''}，保留 115 原目录结构；miyabi 子目录专用于项目刮削。自定义目录须填写服务器上的绝对路径，STRM 使用 Emby 设置中的对外地址。`}
      >
        <Input
          value={current.destination}
          disabled={busy || config.isError}
          placeholder="留空自动使用 Emby 本地输出目录"
          onChange={event => change('destination', event.target.value)}
        />
      </SettingRow>
      <SettingRow
        title="同步间隔（分钟）"
        description="定时处理仅同步元数据的目录，默认 30 分钟；暂停同步不改变刮削规则"
      >
        <Input
          type="number"
          min={1}
          max={1440}
          value={current.interval_minutes}
          disabled={busy || config.isError}
          onChange={event => change('interval_minutes', Number(event.target.value))}
        />
      </SettingRow>
      <div className="space-y-2">
        <div className="text-xs text-muted-foreground">
          为子目录选择下载位置或处理方式，规则包括所有下级目录。默认只有下载目录刮削，其他目录仅同步元数据。
        </div>
        {files.isLoading ? (
          <LoaderCircleIcon className="size-4 animate-spin text-muted-foreground" />
        ) : null}
        {files.isError ? (
          <InlineError>无法读取挂载目录，请检查 115 连接后重试。</InlineError>
        ) : null}
        {folders.map(folder => {
          const isDownload = current.download_directory.id === folder.id
          const defaultMode =
            isDownload || current.download_directory.id === parent.id ? 'scrape' : 'sync'
          const rule = selected.get(folder.id)
          const disabled = busy || config.isError || files.isError || files.isFetching
          return (
            <div
              key={folder.id}
              className="flex flex-col gap-2 rounded-md border p-3 sm:flex-row sm:items-center"
            >
              <span className="min-w-0 flex-1 text-sm break-words">{folder.name}</span>
              <div className="flex flex-wrap items-center gap-2">
                <Button
                  type="button"
                  variant={isDownload ? 'secondary' : 'outline'}
                  size="sm"
                  disabled={disabled || isDownload}
                  onClick={() =>
                    chooseDownload({ id: folder.id, name: folder.name, path: folder.name })
                  }
                >
                  {isDownload ? '下载目录' : '设为下载目录'}
                </Button>
                <Select
                  value={rule ? (rule.mode ?? 'sync') : 'auto'}
                  disabled={disabled}
                  onValueChange={value => setDirectoryMode(folder.id, folder.name, value)}
                >
                  <SelectTrigger aria-label={`${folder.name} 的处理方式`}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="auto">
                      默认：{defaultMode === 'scrape' ? '项目刮削' : '仅同步元数据'}
                    </SelectItem>
                    <SelectItem value="scrape">项目刮削</SelectItem>
                    <SelectItem value="sync">仅生成 STRM、同步元数据</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
          )
        })}
        {!files.isLoading && !files.isError && folders.length === 0 ? (
          <div className="text-xs text-muted-foreground">当前页没有直接子目录。</div>
        ) : null}
      </div>
      <div className="flex flex-wrap items-center justify-end gap-2">
        {config.data?.last_run_at ? (
          <span className="mr-auto text-xs text-muted-foreground">
            最近同步：{new Date(config.data.last_run_at).toLocaleString()} · 下载{' '}
            {config.data.files_downloaded} · 生成 STRM {config.data.files_generated ?? 0} · 跳过{' '}
            {config.data.files_skipped}
          </span>
        ) : null}
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={busy || isDirty || !config.data?.enabled}
          onClick={() => run.mutate()}
        >
          {run.isPending ? (
            <LoaderCircleIcon className="mr-1.5 size-3.5 animate-spin" />
          ) : (
            <CloudDownloadIcon className="mr-1.5 size-3.5" />
          )}
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
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label="上一页目录"
            disabled={page <= 1 || files.isFetching || busy}
            onClick={() => setPage(value => value - 1)}
          >
            <ChevronLeftIcon className="size-4" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label="下一页目录"
            disabled={!files.data.has_more || files.isFetching || busy}
            onClick={() => setPage(value => value + 1)}
          >
            <ChevronRightIcon className="size-4" />
          </Button>
        </div>
      ) : null}
      {config.data?.last_result && config.data.last_result !== 'success' ? (
        <InlineError>最近同步有文件处理失败：{config.data.last_result}</InlineError>
      ) : null}
      {config.data?.errors?.slice(0, 3).map((error, index) => (
        <div key={`${index}-${error}`} className="text-xs text-destructive">
          {error}
        </div>
      ))}
      {config.isError ? (
        <InlineError>无法读取同步配置：{describeApiError(config.error)}</InlineError>
      ) : null}
      {update.isError ? (
        <InlineError>同步配置保存失败：{describeApiError(update.error)}</InlineError>
      ) : null}
      {run.isError ? <InlineError>同步未完成：{describeApiError(run.error)}</InlineError> : null}
    </div>
  )
}
