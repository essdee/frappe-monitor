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

  async function refresh() {
    if (!query.value) return
    loading.value = true
    error.value = null
    try {
      result.value = await queryMetrics(query.value, range.value)
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
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
