import { ref, onScopeDispose, watch, type Ref } from 'vue'
import { fetchServers, type Server } from '../api'

/**
 * useServers returns a reactive list of servers, polling on the supplied
 * refresh interval (in seconds; 0 disables polling).
 */
export function useServers(refreshSec: Ref<number>): {
  servers: Ref<Server[]>
  loading: Ref<boolean>
  error: Ref<string | null>
  refresh: () => Promise<void>
} {
  const servers = ref<Server[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)
	let requestSequence = 0

  async function refresh() {
		const requestID = ++requestSequence
    loading.value = true
    error.value = null
    try {
			const response = await fetchServers()
			if (requestID === requestSequence) servers.value = response
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

  watch(
    refreshSec,
    (s) => {
      arm(s)
    },
    { immediate: true },
  )

  onScopeDispose(() => {
		requestSequence++
		armedSeconds = 0
		if (timer) clearTimeout(timer)
  })

  refresh()

  return { servers, loading, error, refresh }
}
