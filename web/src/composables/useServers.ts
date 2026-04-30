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

  async function refresh() {
    loading.value = true
    error.value = null
    try {
      servers.value = await fetchServers()
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

  watch(
    refreshSec,
    (s) => {
      arm(s)
    },
    { immediate: true },
  )

  onScopeDispose(() => {
    if (timer) clearInterval(timer)
  })

  refresh()

  return { servers, loading, error, refresh }
}
