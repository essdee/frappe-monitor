import { ref, type Ref } from 'vue'

// realtime.ts — a single WebSocket connection per browser tab that
// replaces the dashboard's old setInterval polling. Components subscribe
// to topics (server-side "rooms") and register handlers for event types;
// the server pushes status, metrics, alerts, and log lines. There is no
// long-polling and no repeated REST polling — one socket, server push.

export interface RealtimeEvent {
  type: string
  topic?: string
  data?: any
  ts: number
}

export type ConnState = 'connecting' | 'open' | 'closed'

type Handler = (ev: RealtimeEvent) => void

// Topic name builders — MUST match the Go side (internal/realtime/event.go).
export const topics = {
  servers: () => 'servers',
  server: (id: number) => `server:${id}`,
  alerts: () => 'alerts',
  logs: (serverId: number) => `logs:${serverId}`,
}

class RealtimeClient {
  private ws: WebSocket | null = null
  private readonly handlers = new Map<string, Set<Handler>>()
  // topic → active subscriber count, so N components sharing a topic
  // produce one server-side subscription and it drops on the last unmount.
  private readonly topicRefs = new Map<string, number>()
  private backoff = 1000
  private readonly maxBackoff = 30000
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null

  /** Reactive connection state for a UI indicator. */
  readonly state: Ref<ConnState> = ref('closed')

  private endpoint(): string {
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    return `${proto}://${location.host}/api/v1/ws`
  }

  /** Open the socket if not already open/connecting. Idempotent. */
  connect() {
    if (
      this.ws &&
      (this.ws.readyState === WebSocket.OPEN || this.ws.readyState === WebSocket.CONNECTING)
    ) {
      return
    }
    this.state.value = 'connecting'
    const ws = new WebSocket(this.endpoint())
    this.ws = ws

    ws.onopen = () => {
      this.state.value = 'open'
      this.backoff = 1000
      // Re-assert every active subscription after a (re)connect.
      const active = [...this.topicRefs.keys()]
      if (active.length) this.sendRaw({ action: 'subscribe', topics: active })
    }
    ws.onmessage = (e) => {
      let ev: RealtimeEvent
      try {
        ev = JSON.parse(e.data)
      } catch {
        return
      }
      const set = this.handlers.get(ev.type)
      if (set) {
        for (const h of [...set]) {
          try {
            h(ev)
          } catch (err) {
            console.error('realtime handler error', err)
          }
        }
      }
    }
    ws.onclose = () => {
      this.state.value = 'closed'
      this.ws = null
      this.scheduleReconnect()
    }
    ws.onerror = () => {
      // onclose fires next; reconnect is handled there.
    }
  }

  private scheduleReconnect() {
    if (this.reconnectTimer) return
    // Only bother reconnecting if something still cares about a topic.
    if (this.topicRefs.size === 0) return
    const delay = this.backoff
    this.backoff = Math.min(this.backoff * 2, this.maxBackoff)
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null
      this.connect()
    }, delay)
  }

  private sendRaw(msg: unknown) {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(msg))
    }
  }

  /** Subscribe to topics (ref-counted). Lazily connects on first use. */
  subscribe(topicList: string[]) {
    const fresh: string[] = []
    for (const t of topicList) {
      const n = this.topicRefs.get(t) ?? 0
      if (n === 0) fresh.push(t)
      this.topicRefs.set(t, n + 1)
    }
    this.connect()
    if (fresh.length) this.sendRaw({ action: 'subscribe', topics: fresh })
  }

  /** Release a subscription (ref-counted). */
  unsubscribe(topicList: string[]) {
    const gone: string[] = []
    for (const t of topicList) {
      const n = this.topicRefs.get(t) ?? 0
      if (n <= 1) {
        this.topicRefs.delete(t)
        gone.push(t)
      } else {
        this.topicRefs.set(t, n - 1)
      }
    }
    if (gone.length) this.sendRaw({ action: 'unsubscribe', topics: gone })
  }

  /** Register a handler for an event type. Returns an unsubscribe fn. */
  on(type: string, handler: Handler): () => void {
    let set = this.handlers.get(type)
    if (!set) {
      set = new Set()
      this.handlers.set(type, set)
    }
    set.add(handler)
    return () => set!.delete(handler)
  }
}

export const realtime = new RealtimeClient()
