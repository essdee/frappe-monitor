import { ref, onScopeDispose, type Ref } from 'vue'
import { fetchDBTargets, type DBTarget } from '../api'
import { realtime, topics } from '../realtime'

/**
 * useDBTargets returns the reactive list of DB replication targets. Loads
 * once over REST, then keeps each row's replication status live via the
 * db.status WebSocket push, and adds/replaces/drops rows on
 * db.created/updated/deleted. No polling.
 */
export function useDBTargets(): {
  targets: Ref<DBTarget[]>
  loading: Ref<boolean>
  error: Ref<string | null>
  refresh: () => Promise<void>
} {
  const targets = ref<DBTarget[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)

  async function refresh() {
    loading.value = true
    error.value = null
    try {
      targets.value = await fetchDBTargets()
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
    }
  }
  refresh()

  function patch(id: number, fields: Partial<DBTarget>) {
    const i = targets.value.findIndex((t) => t.id === id)
    if (i >= 0) targets.value[i] = { ...targets.value[i], ...fields }
  }

  realtime.subscribe([topics.databases()])
  const offs = [
    realtime.on('db.status', (ev) => {
      const d = ev.data ?? {}
      patch(d.id, {
        status: d.status,
        server: d.server,
        io_running: d.io_running,
        sql_running: d.sql_running,
        lag_seconds: d.lag_seconds,
        heartbeat_lag_seconds: d.heartbeat_lag_seconds,
        last_error: d.last_error,
        last_checked_at: d.last_checked_at,
      })
    }),
    realtime.on('db.updated', (ev) => {
      const d = ev.data as DBTarget
      if (d && typeof d.id === 'number') patch(d.id, d)
    }),
    realtime.on('db.created', (ev) => {
      const d = ev.data as DBTarget
      if (d && typeof d.id === 'number' && !targets.value.some((t) => t.id === d.id)) {
        targets.value = [...targets.value, d]
      }
    }),
    realtime.on('db.deleted', (ev) => {
      const id = ev.data?.id
      targets.value = targets.value.filter((t) => t.id !== id)
    }),
  ]
  onScopeDispose(() => {
    offs.forEach((off) => off())
    realtime.unsubscribe([topics.databases()])
  })

  return { targets, loading, error, refresh }
}
