import { describe, it, expect, vi, beforeEach } from 'vitest'
import { ref, effectScope, nextTick } from 'vue'
import type { MetricsQueryResponse, TimeRange } from '../api'

// useMetricsRange runs a PromQL query over a range and re-runs on change. Its
// critical invariant is the monotonic seq guard: a slow EARLIER request that
// resolves after a newer one must NOT clobber the newer result. We mock the
// network (queryMetrics) with manually-resolved deferreds to force the
// out-of-order completion, and stub the realtime singleton so the composable
// can subscribe without a socket.

// --- controllable queryMetrics mock ---------------------------------------

type Deferred = {
  promise: Promise<MetricsQueryResponse>
  resolve: (v: MetricsQueryResponse) => void
  reject: (e: unknown) => void
}
const deferreds: Deferred[] = []
const queryMetricsMock = vi.fn((): Promise<MetricsQueryResponse> => {
  let resolve!: (v: MetricsQueryResponse) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<MetricsQueryResponse>((res, rej) => {
    resolve = res
    reject = rej
  })
  deferreds.push({ promise, resolve, reject })
  return promise
})

vi.mock('../api', () => ({
  queryMetrics: (...args: unknown[]) => queryMetricsMock(...(args as [])),
}))

// realtime stub: record subscribe/unsubscribe/on so the composable's
// optional serverId path doesn't explode, and so we can test resubscribe.
const subscribeSpy = vi.fn()
const unsubscribeSpy = vi.fn()
const onSpy = vi.fn(() => () => {})
vi.mock('../realtime', () => ({
  realtime: {
    subscribe: (...a: unknown[]) => subscribeSpy(...(a as [])),
    unsubscribe: (...a: unknown[]) => unsubscribeSpy(...(a as [])),
    on: (...a: unknown[]) => onSpy(...(a as [])),
  },
  topics: {
    server: (id: number) => `server:${id}`,
  },
}))

import { useMetricsRange } from '../composables/useMetricsRange'

function resp(label: string): MetricsQueryResponse {
  return {
    status: 'success',
    data: { resultType: 'matrix', result: [{ metric: { tag: label }, values: [[1, '1']] }] },
  }
}

beforeEach(() => {
  deferreds.length = 0
  queryMetricsMock.mockClear()
  subscribeSpy.mockClear()
  unsubscribeSpy.mockClear()
  onSpy.mockClear()
})

describe('useMetricsRange seq guard', () => {
  it('a slow earlier request does not overwrite a newer result', async () => {
    const scope = effectScope()
    let api!: ReturnType<typeof useMetricsRange>
    const query = ref('up')
    const range = ref<TimeRange>({ from: 100, to: 200, step: 15 })

    scope.run(() => {
      api = useMetricsRange(query, range)
    })

    // Immediate watch fired request #0.
    await nextTick()
    expect(queryMetricsMock).toHaveBeenCalledTimes(1)

    // Trigger a second, newer request by changing the query.
    query.value = 'up{job="x"}'
    await nextTick()
    expect(queryMetricsMock).toHaveBeenCalledTimes(2)

    // Resolve the NEWER request (#1) first...
    deferreds[1].resolve(resp('new'))
    await deferreds[1].promise
    await nextTick()
    expect(api.result.value?.data.result[0].metric.tag).toBe('new')
    expect(api.loading.value).toBe(false)

    // ...then resolve the STALE older request (#0). It must be ignored.
    deferreds[0].resolve(resp('stale'))
    await deferreds[0].promise
    await nextTick()
    expect(api.result.value?.data.result[0].metric.tag).toBe('new')

    scope.stop()
  })

  it('a stale request that REJECTS late does not set the error', async () => {
    const scope = effectScope()
    let api!: ReturnType<typeof useMetricsRange>
    const query = ref('up')
    const range = ref<TimeRange>({ from: 100, to: 200, step: 15 })
    scope.run(() => {
      api = useMetricsRange(query, range)
    })
    await nextTick()

    query.value = 'up2'
    await nextTick()
    expect(deferreds.length).toBe(2)

    // Newer (#1) succeeds.
    deferreds[1].resolve(resp('new'))
    await deferreds[1].promise
    await nextTick()

    // Older (#0) rejects late — must NOT overwrite result or set error.
    deferreds[0].reject(new Error('boom'))
    await deferreds[0].promise.catch(() => {})
    await nextTick()
    expect(api.error.value).toBeNull()
    expect(api.result.value?.data.result[0].metric.tag).toBe('new')

    scope.stop()
  })

  it('manual refresh() does not return early and issues a query when query is set', async () => {
    const scope = effectScope()
    let api!: ReturnType<typeof useMetricsRange>
    const query = ref('up')
    const range = ref<TimeRange>({ from: 100, to: 200, step: 15 })
    scope.run(() => {
      api = useMetricsRange(query, range)
    })
    await nextTick()
    expect(queryMetricsMock).toHaveBeenCalledTimes(1)

    void api.refresh()
    await nextTick()
    expect(queryMetricsMock).toHaveBeenCalledTimes(2)

    scope.stop()
  })

  it('with an empty query, refresh is a no-op (no network call)', async () => {
    const scope = effectScope()
    let api!: ReturnType<typeof useMetricsRange>
    const query = ref('')
    const range = ref<TimeRange>({ from: 100, to: 200, step: 15 })
    scope.run(() => {
      api = useMetricsRange(query, range)
    })
    await nextTick()
    // immediate watch ran but query is empty → early return, no call.
    expect(queryMetricsMock).not.toHaveBeenCalled()

    await api.refresh()
    expect(queryMetricsMock).not.toHaveBeenCalled()

    scope.stop()
  })
})

describe('useMetricsRange live subscription', () => {
  it('subscribes to the server topic when a serverId is provided', async () => {
    const scope = effectScope()
    const query = ref('up')
    const range = ref<TimeRange>({ from: 100, to: 200, step: 15 })
    const serverId = ref<number | null>(7)
    scope.run(() => {
      useMetricsRange(query, range, serverId)
    })
    await nextTick()
    expect(subscribeSpy).toHaveBeenCalledWith(['server:7'])
    expect(onSpy).toHaveBeenCalledWith('metrics', expect.any(Function))

    scope.stop()
  })

  it('re-subscribes when the serverId changes and unsubscribes the old topic', async () => {
    const scope = effectScope()
    const query = ref('up')
    const range = ref<TimeRange>({ from: 100, to: 200, step: 15 })
    const serverId = ref<number | null>(1)
    scope.run(() => {
      useMetricsRange(query, range, serverId)
    })
    await nextTick()
    expect(subscribeSpy).toHaveBeenLastCalledWith(['server:1'])

    serverId.value = 2
    await nextTick()
    expect(unsubscribeSpy).toHaveBeenCalledWith(['server:1'])
    expect(subscribeSpy).toHaveBeenLastCalledWith(['server:2'])

    scope.stop()
  })
})
