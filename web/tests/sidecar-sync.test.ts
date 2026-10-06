import assert from 'node:assert/strict'
import { QueryClient, QueryObserver } from '@tanstack/react-query'
import { onTestFinished, test, vi } from 'vitest'

import {
  panKeys,
  panSidecarSyncOptions,
  runPanSidecarSyncOptions,
  type PanSidecarSyncConfig
} from '@/api/pan'

const config: PanSidecarSyncConfig = {
  enabled: true,
  account_id: '100',
  parent_id: '10',
  destination: '',
  default_destination: '/media',
  download_directory: { id: '', name: '', path: '' },
  child_directories: [],
  interval_minutes: 30,
  files_downloaded: 0,
  files_generated: 0,
  files_skipped: 0,
  running: false
}

test('202 updates running state while GET completion supplies final results', async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  onTestFinished(() => client.clear())
  vi.stubGlobal('window', { location: { origin: 'http://localhost' } })
  const fetch = vi.spyOn(globalThis, 'fetch').mockImplementation(async (path, init) => {
    if (init?.method === 'POST') {
      assert.equal(path, '/api/pan/sidecar-sync/run')
      return Response.json({ ...config, running: true }, { status: 202 })
    }
    assert.equal(path, '/api/pan/sidecar-sync')
    return Response.json({ ...config, running: false, last_result: 'success', files_generated: 4 })
  })
  const mutation = client.getMutationCache().build(client, runPanSidecarSyncOptions(client))
  await mutation.execute(undefined)
  assert.equal(client.getQueryData<PanSidecarSyncConfig>(panKeys.sidecarSync)?.running, true)
  assert.equal(
    client.getQueryData<PanSidecarSyncConfig>(panKeys.sidecarSync)?.last_result,
    undefined
  )
  const options = panSidecarSyncOptions()
  const query = client
    .getQueryCache()
    .build<PanSidecarSyncConfig, Error, PanSidecarSyncConfig, typeof panKeys.sidecarSync>(
      client,
      options
    )
  const interval = options.refetchInterval
  assert.ok(typeof interval === 'function')
  assert.equal(interval(query), 2000)
  await client.fetchQuery(options)
  assert.equal(interval(query), false)
  assert.equal(client.getQueryData<PanSidecarSyncConfig>(panKeys.sidecarSync)?.files_generated, 4)
  assert.equal(fetch.mock.calls.length, 2)
})

test('an unconfirmed start refreshes backend status without marking the task failed', async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  onTestFinished(() => client.clear())
  vi.stubGlobal('window', { location: { origin: 'http://localhost' } })
  client.setQueryData(panKeys.sidecarSync, config)
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (_path, init) =>
    init?.method === 'POST'
      ? Response.json({ error: 'proxy timeout' }, { status: 524 })
      : Response.json({ ...config, running: true })
  )
  const observer = new QueryObserver(client, panSidecarSyncOptions())
  const unsubscribe = observer.subscribe(() => {})
  onTestFinished(unsubscribe)
  const mutation = client.getMutationCache().build(client, runPanSidecarSyncOptions(client))
  await assert.rejects(mutation.execute(undefined))
  await client.refetchQueries({ queryKey: panKeys.sidecarSync })
  assert.equal(client.getQueryData<PanSidecarSyncConfig>(panKeys.sidecarSync)?.running, true)
  assert.notEqual(
    client.getQueryData<PanSidecarSyncConfig>(panKeys.sidecarSync)?.last_result,
    'failed'
  )
})
