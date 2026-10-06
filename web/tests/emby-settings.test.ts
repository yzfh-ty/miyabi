import assert from 'node:assert/strict'
import { setImmediate } from 'node:timers/promises'
import { QueryClient } from '@tanstack/react-query'
import { onTestFinished, test, vi } from 'vitest'

import { embyKeys, updateEmbyConfigOptions, type EmbyConfig } from '@/api/emby'

const previous: EmbyConfig = {
  enabled: true,
  server_url: 'http://old:8096',
  api_key: 'key',
  media_path: '/media',
  local_dir: 'E:\\media',
  sync_actors: true,
  public_url: 'http://miyabi:8080'
}

function fixture() {
  const client = new QueryClient()
  client.setQueryData(embyKeys.config, previous)
  onTestFinished(() => client.clear())
  const mutation = client.getMutationCache().build(client, updateEmbyConfigOptions(client))
  return { client, mutation }
}

test('Emby saves optimistically preserve the output directory and apply the server response', async () => {
  const { client, mutation } = fixture()
  const response = Promise.withResolvers<Response>()
  vi.stubGlobal(
    'fetch',
    vi.fn(() => response.promise)
  )
  const next = { ...previous, server_url: 'http://new:8096', local_dir: undefined, public_url: '' }
  const pending = mutation.execute(next)
  await setImmediate()
  assert.equal(mutation.state.status, 'pending')
  assert.deepEqual(client.getQueryData(embyKeys.config), { ...next, local_dir: previous.local_dir })
  const saved = { ...next, local_dir: previous.local_dir, public_url: 'http://192.168.1.2:8080' }
  response.resolve(Response.json(saved))
  await pending
  assert.deepEqual(client.getQueryData(embyKeys.config), saved)
  assert.equal(client.getQueryState(embyKeys.config)?.isInvalidated, false)
})

test('failed Emby saves restore cached settings and invalidate for partially saved responses', async () => {
  const { client, mutation } = fixture()
  const response = Promise.withResolvers<Response>()
  vi.stubGlobal(
    'fetch',
    vi.fn(() => response.promise)
  )
  const pending = mutation.execute({ ...previous, enabled: false })
  const rejected = assert.rejects(pending)
  await setImmediate()
  assert.equal(client.getQueryData<EmbyConfig>(embyKeys.config)?.enabled, false)
  response.resolve(Response.json({ error: 'scan enqueue failed' }, { status: 503 }))
  await rejected
  assert.deepEqual(client.getQueryData(embyKeys.config), previous)
  assert.equal(client.getQueryState(embyKeys.config)?.isInvalidated, true)
})

test('an older settings read cannot overwrite an optimistic Emby save', async () => {
  const { client, mutation } = fixture()
  const readResponse = Promise.withResolvers<EmbyConfig>()
  let readSignal: AbortSignal | undefined
  const read = client
    .fetchQuery({
      queryKey: embyKeys.config,
      queryFn: ({ signal }) => {
        readSignal = signal
        return readResponse.promise
      }
    })
    .catch(() => undefined)
  const response = Promise.withResolvers<Response>()
  vi.stubGlobal(
    'fetch',
    vi.fn(() => response.promise)
  )
  const next = { ...previous, media_path: '/new-media' }
  const pending = mutation.execute(next)
  await setImmediate()
  assert.equal(readSignal?.aborted, true)
  readResponse.resolve(previous)
  await read
  assert.deepEqual(client.getQueryData(embyKeys.config), next)
  response.resolve(Response.json(next))
  await pending
  assert.deepEqual(client.getQueryData(embyKeys.config), next)
})

test('proxy saves apply actual running status and failed port changes restore the saved listener', async () => {
  const { client, mutation } = fixture()
  const next = {
    ...previous,
    proxy_enabled: true,
    proxy_listen: '0.0.0.0:8099',
    proxy_public_url: 'https://play.example'
  }
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValueOnce(Response.json({ ...next, proxy_running: true }))
  )
  await mutation.execute(next)
  assert.equal(client.getQueryData<EmbyConfig>(embyKeys.config)?.proxy_running, true)
  const saved = client.getQueryData<EmbyConfig>(embyKeys.config)
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValueOnce(Response.json({ error: 'port occupied' }, { status: 400 }))
  )
  await assert.rejects(mutation.execute({ ...next, proxy_listen: '0.0.0.0:8080' }))
  assert.deepEqual(client.getQueryData(embyKeys.config), saved)
  assert.equal(client.getQueryState(embyKeys.config)?.isInvalidated, true)
})
