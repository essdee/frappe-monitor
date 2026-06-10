import { ref, watch, onScopeDispose, type Ref } from 'vue'
import { queryMetrics, type MetricsQueryResponse, type TimeRange } from '../api'

/**
 * useMetricsRange runs a PromQL query over the supplied time range,
 * re-running whenever query or range changes, and re-polling at the
 * supplied refresh interval (seconds; 0 disables polling).
 */
export function useMetricsRange(
  query: Ref<string>,
  range: Ref<TimeRange>,
  refreshSec: Ref<number>,
): {
  result: Ref<MetricsQueryResponse | null>
  loading: Ref<boolean>
  error: Ref<string | null>
  refresh: () => Promise<void>
} {
  const result = ref<MetricsQueryResponse | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)

  // Monotonic request id so an earlier poll that resolves late can't
  // clobber a newer one (last-finisher-wins → last-started-wins).
  let seq = 0

  async function refresh() {
    if (!query.value) return
    const mySeq = ++seq
    // Re-anchor the window to "now" on every poll, preserving the span,
    // so a live (relative) range actually advances instead of re-querying
    // the frozen window captured when the range computed first evaluated.
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

  let timer: ReturnType<typeof setInterval> | null = null
  function arm(secs: number) {
    if (timer) {
      clearInterval(timer)
      timer = null
    }
    if (secs > 0) {
      timer = setInterval(refresh, secs * 1000)
    }
  }

  watch([query, range], refresh, { immediate: true })
  watch(refreshSec, (s) => arm(s), { immediate: true })

  onScopeDispose(() => {
    if (timer) clearInterval(timer)
  })

  return { result, loading, error, refresh }
}
