import { useMemo, useState } from 'react'
import { LoaderCircleIcon, RefreshCwIcon, TvMinimalIcon } from 'lucide-react'
import { toast } from 'sonner'

import { describeApiError } from '@/api/client'
import { type EmbyConfig, useEmbyConfig, useTestEmbyConfig, useUpdateEmbyConfig } from '@/api/emby'
import { InlineError } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { cn } from 'cn'
import { combineServerUrl, splitServerUrl } from './emby-url'
import { SettingRow, SettingsSection } from './shared'

interface EmbyFormData {
  enabled: boolean
  host: string
  port: string
  api_key: string
  media_path: string
  sync_actors: boolean
  public_url: string
  proxy_enabled: boolean
  proxy_listen: string
  proxy_public_url: string
}

function configToFormData(config?: EmbyConfig | null): EmbyFormData {
  const { host, port } = splitServerUrl(config?.server_url ?? '')
  return {
    enabled: config?.enabled ?? false,
    host,
    port,
    api_key: config?.api_key ?? '',
    media_path: config?.media_path ?? '',
    sync_actors: config?.sync_actors ?? true,
    public_url: config?.public_url ?? '',
    proxy_enabled: config?.proxy_enabled ?? false,
    proxy_listen: config?.proxy_listen || '0.0.0.0:8099',
    proxy_public_url: config?.proxy_public_url ?? ''
  }
}

export function EmbySection() {
  const emby = useEmbyConfig()
  const updateConfig = useUpdateEmbyConfig()
  const testConfig = useTestEmbyConfig()

  const config = emby.data
  const initial = useMemo(() => configToFormData(config), [config])
  const [form, setForm] = useState<EmbyFormData | null>(null)

  const current = form ?? initial

  const isDirty =
    Boolean(config) &&
    form !== null &&
    (current.enabled !== initial.enabled ||
      current.host !== initial.host ||
      current.port !== initial.port ||
      current.api_key !== initial.api_key ||
      current.media_path !== initial.media_path ||
      current.public_url !== initial.public_url ||
      current.proxy_enabled !== initial.proxy_enabled ||
      current.proxy_listen !== initial.proxy_listen ||
      current.proxy_public_url !== initial.proxy_public_url ||
      current.sync_actors !== initial.sync_actors)

  // Keep the saved form visible until the query observer receives the same
  // values. Clearing it in the success callback can briefly reveal old data.
  if (form !== null && !isDirty && !updateConfig.isPending) setForm(null)

  const disabled = emby.isLoading || emby.isError || updateConfig.isPending

  function updateField<K extends keyof EmbyFormData>(key: K, value: EmbyFormData[K]) {
    setForm(prev => ({
      ...(prev ?? initial),
      [key]: value
    }))
  }

  function handleToggle(checked: boolean) {
    if (checked) {
      updateField('enabled', true)
      return
    }

    setForm(null)
    if (!config?.enabled) return

    updateConfig.mutate(
      { ...config, enabled: false },
      {
        onSuccess: next => {
          setForm(configToFormData(next))
          toast.success('已关闭 Emby')
        },
        onError: error => {
          toast.error(describeApiError(error))
        }
      }
    )
  }

  function handleHostBlur() {
    const raw = current.host.trim()
    if (!raw) return
    const { host, port } = splitServerUrl(raw)
    if (host !== raw) {
      setForm(prev => ({
        ...(prev ?? initial),
        host,
        port
      }))
    }
  }

  function handleTest() {
    const trimmedHost = current.host.trim()
    const trimmedKey = current.api_key.trim()

    if (!trimmedHost) {
      toast.error('请填写 Emby 服务器地址')
      return
    }
    if (!trimmedKey) {
      toast.error('请填写 Emby API Key')
      return
    }

    const serverUrl = combineServerUrl(trimmedHost, current.port)

    testConfig.mutate(
      { server_url: serverUrl, api_key: trimmedKey },
      {
        onSuccess: data => {
          toast.success('Emby 连接成功', {
            description: `${data.server_name} (v${data.version})`
          })
        },
        onError: error => {
          toast.error(describeApiError(error))
        }
      }
    )
  }

  function handleSave() {
    const trimmedHost = current.host.trim()
    const trimmedKey = current.api_key.trim()

    if (current.enabled) {
      if (!trimmedHost) {
        toast.error('启用 Emby 集成时必须填写服务器地址')
        return
      }
      if (!trimmedKey) {
        toast.error('启用 Emby 集成时必须填写 API Key')
        return
      }
    }

    const serverUrl = combineServerUrl(trimmedHost, current.port)

    const payload: EmbyConfig = {
      enabled: current.enabled,
      server_url: serverUrl,
      api_key: trimmedKey,
      media_path: current.media_path.trim(),
      sync_actors: current.sync_actors,
      public_url: current.public_url.trim(),
      proxy_enabled: current.proxy_enabled,
      proxy_listen: current.proxy_listen.trim(),
      proxy_public_url: current.proxy_public_url.trim()
    }

    updateConfig.mutate(payload, {
      onSuccess: next => {
        setForm(configToFormData(next))
        toast.success('Emby 设置已保存')
      },
      onError: error => {
        toast.error(describeApiError(error))
      }
    })
  }

  return (
    <SettingsSection icon={<TvMinimalIcon className="size-4" />} title="Emby">
      <SettingRow
        title="启用 Emby"
        description="媒体扫描与刮削完成后，主动通知 Emby 增量刷新"
        inline
      >
        <Switch checked={current.enabled} disabled={disabled} onCheckedChange={handleToggle} />
      </SettingRow>

      {current.enabled ? (
        <>
          <SettingRow
            title="同步演员头像"
            description="自动匹配 GFriends 高清女优头像并同步至 Emby"
            inline
          >
            <Switch
              checked={current.sync_actors}
              disabled={disabled}
              onCheckedChange={checked => updateField('sync_actors', checked)}
            />
          </SettingRow>

          <SettingRow
            title="播放反代"
            description="播放器连接反代入口后，115 视频将直接从 CDN 加载"
            inline
          >
            <Switch
              checked={current.proxy_enabled}
              disabled={disabled}
              onCheckedChange={checked => updateField('proxy_enabled', checked)}
            />
          </SettingRow>

          {current.proxy_enabled ? (
            <>
              <SettingRow
                title="反代监听地址"
                description="默认 0.0.0.0:8099；Docker 部署需映射该端口"
              >
                <Input
                  value={current.proxy_listen}
                  placeholder="0.0.0.0:8099"
                  disabled={disabled}
                  onChange={e => updateField('proxy_listen', e.target.value)}
                />
              </SettingRow>
              <SettingRow
                title="反代访问地址"
                description="播放器连接使用的完整地址；公网 HTTPS 或端口映射时请填写，不能填原 Emby 地址"
              >
                <Input
                  value={current.proxy_public_url}
                  placeholder="http://192.168.1.100:8099"
                  disabled={disabled}
                  onChange={e => updateField('proxy_public_url', e.target.value)}
                />
              </SettingRow>
              <div className="space-y-1 text-sm text-muted-foreground">
                <p>
                  {isDirty
                    ? '保存后生效'
                    : config?.proxy_running
                      ? '播放反代已运行，请将播放器中的 Emby 地址改为反代入口。'
                      : '播放反代未运行'}
                </p>
                {!isDirty && config?.proxy_error ? (
                  <InlineError>{config.proxy_error}</InlineError>
                ) : null}
                <p>115 视频仅直接播放；客户端需支持原视频格式，播放失败不会回退到服务器转码。</p>
              </div>
            </>
          ) : null}

          <SettingRow title="服务器地址" description="原 Emby 服务的 IP 或域名，不能填反代入口">
            <Input
              autoFocus={form?.enabled === true && !initial.enabled}
              value={current.host}
              placeholder="http://192.168.1.100"
              disabled={disabled}
              onChange={e => updateField('host', e.target.value)}
              onBlur={handleHostBlur}
            />
          </SettingRow>

          <SettingRow title="端口" description="Emby 服务端口，默认 8096">
            <Input
              value={current.port}
              placeholder="8096"
              disabled={disabled}
              onChange={e => updateField('port', e.target.value)}
            />
          </SettingRow>

          <SettingRow
            title="Miyabi 对外服务地址"
            description="生成 STRM 播放文件时写入的对外地址，供播放器直接访问。留空自动使用局域网 IP"
          >
            <Input
              value={current.public_url}
              placeholder="http://<局域网IP>:8080"
              disabled={disabled}
              onChange={e => updateField('public_url', e.target.value)}
            />
          </SettingRow>

          <SettingRow title="API Key" description="在 Emby 管理后台「高级」→「API 密钥」中生成">
            <Input
              type="password"
              value={current.api_key}
              placeholder="填入 Emby API Key"
              disabled={disabled}
              onChange={e => updateField('api_key', e.target.value)}
            />
          </SettingRow>

          <SettingRow
            title="Emby 媒体库路径"
            description="Emby 容器内挂载的对应目录。留空则默认使用本地导出路径"
          >
            <Input
              value={current.media_path}
              placeholder="/media"
              disabled={disabled}
              onChange={e => updateField('media_path', e.target.value)}
            />
          </SettingRow>

          <div className="flex items-center justify-end gap-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={disabled || testConfig.isPending}
              onClick={handleTest}
            >
              <RefreshCwIcon className={cn('size-3.5', testConfig.isPending && 'animate-spin')} />
              测试连接
            </Button>
            <Button type="button" size="sm" disabled={disabled || !isDirty} onClick={handleSave}>
              {updateConfig.isPending ? (
                <LoaderCircleIcon className="mr-1.5 size-3.5 animate-spin" />
              ) : null}
              保存
            </Button>
          </div>
        </>
      ) : null}

      {emby.isError ? <InlineError>后端服务暂不可用，无法读取 Emby 配置。</InlineError> : null}
    </SettingsSection>
  )
}
