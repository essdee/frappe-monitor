import { ref, watch, type Ref } from 'vue'
import { fetchServers } from '../api'

/**
 * useServerId resolves a server NAME (as it appears in VM labels and the
 * hierarchy routes) to its numeric id (as used by the realtime topics).
 * It fetches the server list once and caches it for the component's life.
 */
export function useServerId(serverName: Ref<string>): Ref<number | null> {
  const id = ref<number | null>(null)
  let cache: { name: string; id: number }[] | null = null

  async function load() {
    try {
      cache = (await fetchServers()).map((s) => ({ name: s.name, id: s.id }))
    } catch {
      cache = cache ?? []
    }
  }

  async function resolve() {
    if (!cache) await load()
    let found = cache!.find((s) => s.name === serverName.value)
    if (!found) {
      // Cache may be stale (a server was renamed/added since we loaded it —
      // useServers patches its own list live, but ours is private). Refresh
      // once before giving up, so a live rename/add doesn't permanently
      // leave this view's metrics subscription dead.
      await load()
      found = cache!.find((s) => s.name === serverName.value)
    }
    id.value = found ? found.id : null
  }

  watch(serverName, resolve, { immediate: true })
  return id
}
