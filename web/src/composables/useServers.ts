import { ref, onScopeDispose, type Ref } from 'vue'
import { fetchServers, type Server } from '../api'
import { realtime, topics } from '../realtime'

/**
 * useServers returns a reactive list of servers. It loads once over REST,
 * then keeps the list live via WebSocket push — server.status patches a
 * card's reachability, and server.created/updated/deleted add/replace/drop
 * a row. No polling.
 */
export function useServers(): {
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

  refresh()

  function patch(id: number, fields: Partial<Server>) {
    const i = servers.value.findIndex((s) => s.id === id)
    if (i >= 0) servers.value[i] = { ...servers.value[i], ...fields }
  }

  realtime.subscribe([topics.servers()])
  const offs = [
    realtime.on('server.status', (ev) => {
      const d = ev.data ?? {}
      patch(d.id, {
        status: d.status,
        last_error: d.last_error,
        last_pinged_at: d.last_pinged_at,
      })
    }),
    realtime.on('server.updated', (ev) => {
      const d = ev.data as Server
      if (d && typeof d.id === 'number') patch(d.id, d)
    }),
    realtime.on('server.created', (ev) => {
      const d = ev.data as Server
      if (d && typeof d.id === 'number' && !servers.value.some((s) => s.id === d.id)) {
        servers.value = [...servers.value, d]
      }
    }),
    realtime.on('server.deleted', (ev) => {
      const id = ev.data?.id
      servers.value = servers.value.filter((s) => s.id !== id)
    }),
  ]

  onScopeDispose(() => {
    offs.forEach((off) => off())
    realtime.unsubscribe([topics.servers()])
  })

  return { servers, loading, error, refresh }
}
