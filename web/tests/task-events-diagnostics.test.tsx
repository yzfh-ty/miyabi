import assert from 'node:assert/strict'
import { QueryClient } from '@tanstack/react-query'
import { renderToStaticMarkup } from 'react-dom/server'
import { onTestFinished, test, vi } from 'vitest'

import { notifyUnauthorized } from '@/api/client'
import { TaskEventsProvider } from '@/features/tasks/task-events'

const runtime = vi.hoisted(() => ({
  effect: undefined as undefined | (() => (() => void) | void),
  client: undefined as unknown as QueryClient
}))

vi.mock('react', async original => ({
  ...(await original<typeof import('react')>()),
  useEffect: (effect: () => (() => void) | void) => {
    runtime.effect = effect
  },
  useState: (value: unknown) => [value, vi.fn()]
}))
vi.mock('@tanstack/react-query', async original => ({
  ...(await original<typeof import('@tanstack/react-query')>()),
  useQueryClient: () => runtime.client
}))
vi.mock('@/api/client', async original => ({
  ...(await original<typeof import('@/api/client')>()),
  notifyUnauthorized: vi.fn()
}))

class Events {
  static OPEN = 1
  static CLOSED = 2
  static current: Events
  readyState = 0
  onerror?: () => void
  constructor(public url: string) {
    Events.current = this
  }
  addEventListener() {}
  close() {
    this.readyState = Events.CLOSED
  }
}

function fixture() {
  vi.useFakeTimers()
  vi.stubGlobal('EventSource', Events)
  runtime.client = new QueryClient()
  renderToStaticMarkup(<TaskEventsProvider>tasks</TaskEventsProvider>)
  const cleanup = runtime.effect!()!
  onTestFinished(() => {
    cleanup()
    runtime.client.clear()
  })
  return { events: Events.current, cleanup }
}

test('a closed SSE checks authentication with a finite endpoint and prevents duplicate diagnostics', async () => {
  const response = Promise.withResolvers<Response>()
  const fetch = vi.spyOn(globalThis, 'fetch').mockReturnValue(response.promise)
  const { events } = fixture()
  events.readyState = Events.CLOSED
  events.onerror?.()
  events.onerror?.()
  assert.equal(fetch.mock.calls.length, 1)
  assert.equal(fetch.mock.calls[0]![0], '/api/tasks')
  response.resolve(Response.json([]))
  await vi.advanceTimersByTimeAsync(0)
  assert.equal(vi.mocked(notifyUnauthorized).mock.calls.length, 0)
})

test('unmount aborts the diagnostic and an old 401 cannot log out a later session', async () => {
  const response = Promise.withResolvers<Response>()
  const fetch = vi.spyOn(globalThis, 'fetch').mockReturnValue(response.promise)
  const { events, cleanup } = fixture()
  events.readyState = Events.CLOSED
  events.onerror?.()
  const signal = fetch.mock.calls[0]![1]?.signal
  cleanup()
  assert.equal(signal?.aborted, true)
  response.resolve(Response.json({ code: 'UNAUTHORIZED' }, { status: 401 }))
  await vi.advanceTimersByTimeAsync(0)
  assert.equal(vi.mocked(notifyUnauthorized).mock.calls.length, 0)
})

test('an active authentication rejection still notifies the access gate', async () => {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(
    Response.json({ code: 'UNAUTHORIZED' }, { status: 401 })
  )
  const { events } = fixture()
  events.readyState = Events.CLOSED
  events.onerror?.()
  await vi.advanceTimersByTimeAsync(0)
  assert.equal(vi.mocked(notifyUnauthorized).mock.calls.length, 1)
})
