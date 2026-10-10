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
	let requestSequence = 0

  async function refresh() {
		const requestID = ++requestSequence
		const querySnapshot = query.value
		if (!querySnapshot) {
			result.value = null
			error.value = null
			return
		}
		const rangeSnapshot = range.value
    loading.value = true
    error.value = null
    try {
			const response = await queryMetrics(querySnapshot, rangeSnapshot)
			if (requestID === requestSequence) result.value = response
    } catch (e) {
			if (requestID === requestSequence) {
				error.value = e instanceof Error ? e.message : String(e)
			}
    } finally {
			if (requestID === requestSequence) loading.value = false
    }
  }

	let timer: ReturnType<typeof setTimeout> | null = null
	let armedSeconds = 0
  function arm(secs: number) {
    if (timer) {
			clearTimeout(timer)
      timer = null
    }
		armedSeconds = secs
		if (secs > 0) scheduleNext()
	}
	function scheduleNext() {
		timer = setTimeout(async () => {
			await refresh()
			if (armedSeconds > 0) scheduleNext()
		}, armedSeconds * 1000)
  }

  watch([query, range], refresh, { immediate: true })
  watch(refreshSec, (s) => arm(s), { immediate: true })

  onScopeDispose(() => {
		requestSequence++
		armedSeconds = 0
		if (timer) clearTimeout(timer)
  })

  return { result, loading, error, refresh }
}
