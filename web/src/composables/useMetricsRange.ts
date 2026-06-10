import { ref, watch, onScopeDispose, type Ref } from 'vue'
import { queryMetrics, type MetricsQueryResponse, type TimeRange } from '../api'
import { realtime, topics } from '../realtime'

/**
 * useMetricsRange runs a PromQL query over the supplied time range,
 * re-running whenever query or range changes. When a `serverId` ref is
 * given, it also subscribes to that server's metrics topic and re-pulls
 * the window each time a new collection cycle pushes data — event-driven,
 * NOT a timer. There is no polling.
 */
export function useMetricsRange(
  query: Ref<string>,
  range: Ref<TimeRange>,
  serverId?: Ref<number | null | undefined>,
): {
  result: Ref<MetricsQueryResponse | null>
  loading: Ref<boolean>
  error: Ref<string | null>
  refresh: () => Promise<void>
} {
  const result = ref<MetricsQueryResponse | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)

  // Monotonic request id so an earlier request that resolves late can't
  // clobber a newer one (last-finisher-wins → last-started-wins).
  let seq = 0

  async function refresh() {
    if (!query.value) return
    const mySeq = ++seq
    // Re-anchor the window to "now", preserving the span, so a live
    // (relative) range advances instead of re-querying a frozen window.
    const base = range.value
    const span = Math.max(1, base.to - base.from)
    const now = Math.floor(Date.now() / 1000)
    const live: TimeRange = { from: now - span, to: now, step: base.step }

    loading.value = true
    error.value = null
    try {
      const res = await queryMetrics(query.value, live)
      if (mySeq === seq) result.value = res
    } catch (e) {
      if (mySeq === seq) error.value = e instanceof Error ? e.message : String(e)
    } finally {
      if (mySeq === seq) loading.value = false
    }
  }

  watch([query, range], refresh, { immediate: true })

  // Live updates: re-pull when this server's next collection lands. The
  // push only fires when data actually changes (~once per collection
  // cycle), so this is event-driven, not interval polling.
  let topic: string | null = null
  let offMetrics: (() => void) | null = null
  function resubscribe(id: number | null | undefined) {
    if (offMetrics) {
      offMetrics()
      offMetrics = null
    }
    if (topic) {
      realtime.unsubscribe([topic])
      topic = null
    }
    if (typeof id === 'number') {
      topic = topics.server(id)
      realtime.subscribe([topic])
      offMetrics = realtime.on('metrics', (ev) => {
        if (ev.data?.server_id === id) void refresh()
      })
    }
  }
  if (serverId) {
    watch(serverId, (id) => resubscribe(id), { immediate: true })
  }

  onScopeDispose(() => {
    if (offMetrics) offMetrics()
    if (topic) realtime.unsubscribe([topic])
  })

  return { result, loading, error, refresh }
}
