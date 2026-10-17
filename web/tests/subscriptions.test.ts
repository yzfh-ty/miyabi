import assert from 'node:assert/strict'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, test, onTestFinished, vi } from 'vitest'
import {
  QueryClient,
  QueryObserver,
  useQuery,
  type QueryObserverOptions,
  type QueryObserverResult,
  type QueryKey
} from '@tanstack/react-query'

import {
  subscriptionKeys,
  SUBSCRIPTION_PAGE_SIZE,
  useSubscriptionTargets,
  type SubscriptionTarget
} from '@/api/subscriptions'

// Run the hook's actual query options through QueryObserver without a browser.
vi.mock('@tanstack/react-query', async importOriginal => ({
  ...(await importOriginal<typeof import('@tanstack/react-query')>()),
  useQuery: vi.fn()
}))

beforeEach(() => {
  vi.clearAllMocks()
  vi.stubGlobal('window', { location: { origin: 'http://localhost' } })
})

function createTestClient() {
  const client = new QueryClient({
    defaultOptions: {
      queries: {
        retry: false
      }
    }
  })
  onTestFinished(() => client.clear())
  return client
}

function observe<TQuery, TError = Error, TData = TQuery, TKey extends QueryKey = QueryKey>(
  client: QueryClient,
  options: QueryObserverOptions<TQuery, TError, TData, TQuery, TKey>
) {
  const observer = new QueryObserver(client, options)
  const unsubscribe = observer.subscribe(() => {})
  onTestFinished(unsubscribe)
  return observer
}

function settled<TQuery, TError, TData, TKey extends QueryKey>(
  observer: QueryObserver<TQuery, TError, TData, TQuery, TKey>
) {
  if (observer.getCurrentResult().fetchStatus === 'idle') {
    return Promise.resolve(observer.getCurrentResult())
  }
  return new Promise<QueryObserverResult<TData, TError>>(resolve => {
    const unsubscribe = observer.subscribe(result => {
      if (result.fetchStatus !== 'idle') return
      unsubscribe()
      resolve(result)
    })
  })
}

test.each([
  { kind: 'movie', path: '/api/subscriptions/targets?kind=movie' },
  { kind: 'actor', path: '/api/subscriptions/targets?kind=actor' },
  { kind: undefined, path: '/api/subscriptions/targets' }
] as const)(
  'subscription targets from $path retain and index more than 100 items',
  async ({ kind, path }) => {
    const client = createTestClient()
    const mockTargets: SubscriptionTarget[] = Array.from({ length: 150 }, (_, i) => {
      const targetKind = kind ?? (i % 2 === 0 ? 'movie' : 'actor')
      return {
        id: i + 1,
        kind: targetKind,
        target_id: `${targetKind}-${i + 1}`,
        status: targetKind === 'actor' ? 'active' : i % 2 === 0 ? 'waiting' : 'added'
      }
    })
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockImplementation(async () => Response.json(mockTargets))
    vi.stubGlobal('fetch', fetch)

    function Targets() {
      useSubscriptionTargets(kind)
      return null
    }
    renderToStaticMarkup(createElement(Targets))
    const options = vi.mocked(useQuery).mock.calls.at(-1)![0]
    assert.deepEqual(options.queryKey, subscriptionKeys.targets(kind))

    const observer = observe(client, options)
    const result = await settled(observer)
    assert.equal(fetch.mock.calls.length, 1)
    assert.equal(fetch.mock.calls[0]![0], path)
    assert.ok(fetch.mock.calls[0]![1]?.signal instanceof AbortSignal)
    const data = result.data as ReturnType<typeof useSubscriptionTargets>['data']
    assert.ok(data)
    assert.deepEqual(data.list, mockTargets)
    assert.equal(data.map.size, 150)

    for (const index of [0, 100, 149]) {
      const target = mockTargets[index]!
      assert.deepEqual(data.map.get(target.target_id), target)
    }
    assert.equal(data.map.get('missing-target'), undefined)
  }
)

test('subscriptionKeys.all invalidates all target and paginated list queries', async () => {
  const client = createTestClient()

  let targetsCalls = 0
  let listCalls = 0

  const targetsObserver = observe(client, {
    queryKey: subscriptionKeys.targets('movie'),
    queryFn: () => {
      targetsCalls++
      return [{ id: 1, kind: 'movie', target_id: 'm-1', status: 'waiting' }]
    }
  })

  const listObserver = observe(client, {
    queryKey: subscriptionKeys.list('movie', 1, SUBSCRIPTION_PAGE_SIZE),
    queryFn: () => {
      listCalls++
      return [{ id: 1, kind: 'movie', target_id: 'm-1', code: 'M-1', title: 'Movie 1' }]
    }
  })

  await settled(targetsObserver)
  await settled(listObserver)
  assert.equal(targetsCalls, 1)
  assert.equal(listCalls, 1)

  // Invalidate all subscriptions
  await client.invalidateQueries({ queryKey: subscriptionKeys.all })

  assert.equal(targetsCalls, 2)
  assert.equal(listCalls, 2)
})
