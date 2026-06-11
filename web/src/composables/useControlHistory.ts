import { onScopeDispose, ref, type Ref } from 'vue'
import { fetchControlHistory, type ControlAction } from '../api'
import { realtime, topics } from '../realtime'

// useControlHistory keeps a live, newest-first list of control-panel runs.
// The initial list is fetched once; thereafter control.started / control.updated
// events from the WebSocket prepend new runs and patch their state in place
// (pending → running → success/failed) with no polling.
export function useControlHistory(limit = 50): {
  history: Ref<ControlAction[]>
  loading: Ref<boolean>
  error: Ref<string | null>
  refresh: () => Promise<void>
} {
  const history = ref<ControlAction[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)

  async function refresh() {
    loading.value = true
    error.value = null
    try {
      history.value = await fetchControlHistory({ limit })
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
    }
  }
  refresh()

  function upsert(a: ControlAction) {
    const i = history.value.findIndex((x) => x.id === a.id)
    if (i >= 0) {
      history.value[i] = { ...history.value[i], ...a }
    } else {
      // New run: prepend, keep the list bounded so a long-lived tab doesn't
      // grow without limit.
      history.value = [a, ...history.value].slice(0, limit + 25)
    }
  }

  realtime.subscribe([topics.control()])
  const offs = [
    realtime.on('control.started', (ev) => {
      const d = ev.data as ControlAction
      if (d && typeof d.id === 'number') upsert(d)
    }),
    realtime.on('control.updated', (ev) => {
      const d = ev.data as ControlAction
      if (d && typeof d.id === 'number') upsert(d)
    }),
  ]
  onScopeDispose(() => {
    offs.forEach((off) => off())
    realtime.unsubscribe([topics.control()])
  })

  return { history, loading, error, refresh }
}
