import { onScopeDispose, watch, type Ref } from 'vue'
import { realtime, topics, type ConnState, type RealtimeEvent } from '../realtime'

/** Reactive WebSocket connection state for a UI indicator. */
export function useRealtimeStatus(): Ref<ConnState> {
  return realtime.state
}

/**
 * useRealtimeTopic subscribes to one or more topics and wires event
 * handlers for the lifetime of the calling component, unsubscribing and
 * detaching automatically on unmount. `handlers` maps event-type →
 * callback (e.g. { 'server.status': ev => ... }).
 */
export function useRealtimeTopic(
  topic: string | string[],
  handlers: Record<string, (ev: RealtimeEvent) => void>,
): void {
  const topicList = Array.isArray(topic) ? topic : [topic]
  realtime.subscribe(topicList)
  const offs = Object.entries(handlers).map(([type, h]) => realtime.on(type, h))
  onScopeDispose(() => {
    offs.forEach((off) => off())
    realtime.unsubscribe(topicList)
  })
}

/**
 * useServerMetrics subscribes to a server's metrics topic (resolved from a
 * possibly-async id ref) and calls `onMetrics` each time that server's next
 * collection cycle pushes data. Re-binds when the id changes; cleans up on
 * unmount. Event-driven — no polling.
 */
export function useServerMetrics(
  serverId: Ref<number | null | undefined>,
  onMetrics: () => void,
): void {
  let topic: string | null = null
  let off: (() => void) | null = null
  watch(
    serverId,
    (id) => {
      if (off) {
        off()
        off = null
      }
      if (topic) {
        realtime.unsubscribe([topic])
        topic = null
      }
      if (typeof id === 'number') {
        topic = topics.server(id)
        realtime.subscribe([topic])
        off = realtime.on('metrics', (ev) => {
          if (ev.data?.server_id === id) onMetrics()
        })
      }
    },
    { immediate: true },
  )
  onScopeDispose(() => {
    if (off) off()
    if (topic) realtime.unsubscribe([topic])
  })
}

export interface LogLineData {
  server_id: number
  file_id: string
  line: string
  ts: number
}

/**
 * useServerLogs subscribes to a server's live log feed (resolved from a
 * possibly-async id ref) and invokes `onLine` for each pushed line. Used
 * for the live log tail. Re-binds on id change; cleans up on unmount.
 */
export function useServerLogs(
  serverId: Ref<number | null | undefined>,
  onLine: (data: LogLineData) => void,
): void {
  let topic: string | null = null
  let off: (() => void) | null = null
  watch(
    serverId,
    (id) => {
      if (off) {
        off()
        off = null
      }
      if (topic) {
        realtime.unsubscribe([topic])
        topic = null
      }
      if (typeof id === 'number') {
        topic = topics.logs(id)
        realtime.subscribe([topic])
        off = realtime.on('log.line', (ev) => {
          if (ev.data?.server_id === id) onLine(ev.data as LogLineData)
        })
      }
    },
    { immediate: true },
  )
  onScopeDispose(() => {
    if (off) off()
    if (topic) realtime.unsubscribe([topic])
  })
}

/**
 * useRealtimeRefetch subscribes to topics and calls `refresh` (debounced)
 * whenever any of the given event types arrive. Used by list views that
 * are cheaper to re-derive than to patch — the refetch is event-driven
 * (only when data changes), never a timer, so there's no polling.
 */
export function useRealtimeRefetch(
  topic: string | string[],
  eventTypes: string[],
  refresh: () => void,
  debounceMs = 1000,
): void {
  const topicList = Array.isArray(topic) ? topic : [topic]
  realtime.subscribe(topicList)
  let timer: ReturnType<typeof setTimeout> | null = null
  const trigger = () => {
    if (timer) return
    timer = setTimeout(() => {
      timer = null
      refresh()
    }, debounceMs)
  }
  const offs = eventTypes.map((t) => realtime.on(t, trigger))
  onScopeDispose(() => {
    if (timer) clearTimeout(timer)
    offs.forEach((off) => off())
    realtime.unsubscribe(topicList)
  })
}
