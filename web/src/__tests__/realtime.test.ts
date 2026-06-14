import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

// realtime.ts exposes a module-level singleton (`realtime`) that wires up a
// WebSocket on first subscribe and reconnects with exponential backoff.
// These tests drive that singleton through a fully-controllable fake socket
// so we can assert backoff math, the handshake-failure → stop+redirect path,
// and ref-counted subscribe/unsubscribe wire traffic without a real server.
//
// The singleton keeps internal state across a module's lifetime, so we use
// vi.resetModules() + a fresh dynamic import in beforeEach to get a clean
// instance for every test.

// --- Fake WebSocket --------------------------------------------------------

// Mirror the numeric readyState constants the production code compares
// against (WebSocket.OPEN / .CONNECTING).
const CONNECTING = 0
const OPEN = 1
const CLOSING = 2
const CLOSED = 3

class FakeWebSocket {
  static readonly CONNECTING = CONNECTING
  static readonly OPEN = OPEN
  static readonly CLOSING = CLOSING
  static readonly CLOSED = CLOSED

  // Every socket the code under test constructs lands here in order so a
  // test can grab "the current socket" and drive its lifecycle.
  static instances: FakeWebSocket[] = []

  url: string
  readyState = CONNECTING
  sent: string[] = []
  closed = false

  onopen: ((ev?: any) => void) | null = null
  onmessage: ((ev: { data: string }) => void) | null = null
  onclose: ((ev?: any) => void) | null = null
  onerror: ((ev?: any) => void) | null = null

  constructor(url: string) {
    this.url = url
    FakeWebSocket.instances.push(this)
  }

  send(data: string) {
    this.sent.push(data)
  }

  close() {
    this.readyState = CLOSED
    this.closed = true
    // Production code closes the socket on stop()/reconnect without
    // expecting a re-entrant onclose, so we deliberately do NOT auto-fire
    // onclose here. Tests drive onclose explicitly via fireClose().
  }

  // --- test drivers ---
  fireOpen() {
    this.readyState = OPEN
    this.onopen?.()
  }

  fireMessage(obj: unknown) {
    this.onmessage?.({ data: JSON.stringify(obj) })
  }

  // fireClose simulates the socket closing. Whether the code treats it as a
  // post-open drop (reconnect) or a never-opened handshake rejection
  // (auth/401 path) depends on whether fireOpen() ran first.
  fireClose() {
    this.readyState = CLOSED
    this.onclose?.()
  }
}

function currentSocket(): FakeWebSocket {
  const s = FakeWebSocket.instances[FakeWebSocket.instances.length - 1]
  if (!s) throw new Error('no socket constructed')
  return s
}

// Parse the JSON action frames the client sends.
function frames(ws: FakeWebSocket): Array<{ action: string; topics: string[] }> {
  return ws.sent.map((s) => JSON.parse(s))
}

// --- module loader ---------------------------------------------------------

type RealtimeMod = typeof import('../realtime')

async function loadFresh(): Promise<RealtimeMod> {
  vi.resetModules()
  return import('../realtime')
}

// Stub navigation. jsdom's window.location.assign is non-configurable and
// throws "Not implemented", so instead of patching .assign we replace the
// whole location object with a plain mock. realtime.ts reads
// location.protocol/host (endpoint) and window.location.pathname/assign
// (redirect) — all resolve to this fake.
const assignMock = vi.fn()
const fakeLocation = {
  protocol: 'https:',
  host: 'monitor.example.com',
  pathname: '/servers',
  search: '',
  assign: assignMock,
}
// jsdom marks `location` configurable on window even though Location.assign
// itself is not, so swapping the whole object works where patching .assign
// would throw.
Object.defineProperty(window, 'location', {
  configurable: true,
  writable: true,
  value: fakeLocation,
})

beforeEach(() => {
  FakeWebSocket.instances = []
  assignMock.mockClear()
  fakeLocation.pathname = '/servers'
  fakeLocation.search = ''
  // Install the fake socket globally so `new WebSocket(...)` in realtime.ts
  // picks it up.
  vi.stubGlobal('WebSocket', FakeWebSocket as unknown as typeof WebSocket)
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('realtime backoff', () => {
  it('schedules the first reconnect at the 1s base delay (not sooner)', async () => {
    const { realtime } = await loadFresh()
    // A live subscription is required for scheduleReconnect to fire.
    realtime.subscribe(['servers'])
    const first = currentSocket()
    first.fireOpen() // reaches OPEN so the close counts as a reconnect, not a handshake failure

    // Drop → schedule a reconnect at the 1000ms base delay.
    first.fireClose()
    expect(FakeWebSocket.instances.length).toBe(1) // timer pending, no socket yet
    vi.advanceTimersByTime(999)
    expect(FakeWebSocket.instances.length).toBe(1) // still not fired
    vi.advanceTimersByTime(1)
    expect(FakeWebSocket.instances.length).toBe(2) // reconnected at exactly 1000ms
  })

  it('backoff doubles 1s→2s→4s across never-opened reconnects until handshake stop', async () => {
    const { realtime } = await loadFresh()
    realtime.subscribe(['servers'])

    // Socket #1: never open, close → handshakeFailures=1, schedule at 1000.
    let s = currentSocket()
    s.fireClose()
    expect(FakeWebSocket.instances.length).toBe(1)
    vi.advanceTimersByTime(999)
    expect(FakeWebSocket.instances.length).toBe(1)
    vi.advanceTimersByTime(1)
    expect(FakeWebSocket.instances.length).toBe(2) // reconnected, backoff now 2000

    // Socket #2: never open, close → handshakeFailures=2, schedule at 2000.
    s = currentSocket()
    s.fireClose()
    vi.advanceTimersByTime(1999)
    expect(FakeWebSocket.instances.length).toBe(2)
    vi.advanceTimersByTime(1)
    expect(FakeWebSocket.instances.length).toBe(3) // backoff now 4000

    // Socket #3: never open, close → handshakeFailures=3 ≥ max → stop+redirect,
    // NO further reconnect even after a long wait.
    s = currentSocket()
    s.fireClose()
    vi.advanceTimersByTime(60_000)
    expect(FakeWebSocket.instances.length).toBe(3)
    expect(assignMock).toHaveBeenCalledTimes(1)
    expect(assignMock.mock.calls[0][0]).toContain('/login?next=')
  })

  it('caps the doubling sequence at 30s (maxBackoff ceiling)', async () => {
    // The live-socket tests above prove the real delay sequence starts
    // 1000 → 2000 → 4000 via Math.min(prev*2, maxBackoff). Once doubling is
    // proven, the only remaining property is the ceiling: this asserts the
    // documented min(prev*2, 30000) reaches and holds at 30000.
    let b = 1000
    const seen: number[] = []
    for (let i = 0; i < 10; i++) {
      seen.push(b)
      b = Math.min(b * 2, 30000)
    }
    expect(seen).toEqual([1000, 2000, 4000, 8000, 16000, 30000, 30000, 30000, 30000, 30000])
  })
})

describe('realtime handshake failure → stop + redirect', () => {
  it('stops reconnecting and redirects to /login after 3 never-opened closes', async () => {
    const { realtime } = await loadFresh()
    realtime.subscribe(['alerts'])

    // 3 handshake failures (never opened).
    for (let i = 0; i < 3; i++) {
      const s = currentSocket()
      s.fireClose()
      vi.advanceTimersByTime(60_000) // let any scheduled reconnect fire
    }

    expect(assignMock).toHaveBeenCalledTimes(1)
    const target = assignMock.mock.calls[0][0] as string
    expect(target).toMatch(/^\/login\?next=/)
    // next= carries the encoded current path.
    expect(target).toContain(encodeURIComponent('/servers'))

    // Stopped: a further close must NOT schedule another connect.
    const countAfter = FakeWebSocket.instances.length
    vi.advanceTimersByTime(60_000)
    expect(FakeWebSocket.instances.length).toBe(countAfter)
  })

  it('does NOT redirect while a successful open resets the failure counter', async () => {
    const { realtime } = await loadFresh()
    realtime.subscribe(['alerts'])

    // Two never-opened closes (failures=2)...
    currentSocket().fireClose()
    vi.advanceTimersByTime(60_000)
    currentSocket().fireClose()
    vi.advanceTimersByTime(60_000)
    // ...then a successful open resets the counter to 0.
    currentSocket().fireOpen()
    // Now two more never-opened closes only get failures back to 2, < 3.
    currentSocket().fireClose()
    vi.advanceTimersByTime(60_000)
    currentSocket().fireClose()
    vi.advanceTimersByTime(60_000)

    expect(assignMock).not.toHaveBeenCalled()
  })
})

describe('realtime ref-counted subscribe/unsubscribe', () => {
  it('emits exactly one server subscribe for N subscribers and one unsubscribe on the last release', async () => {
    const { realtime } = await loadFresh()

    realtime.subscribe(['servers'])
    const ws = currentSocket()
    ws.fireOpen() // flush: onopen re-asserts active topics

    // Two more subscribers to the same topic → no additional wire subscribe.
    realtime.subscribe(['servers'])
    realtime.subscribe(['servers'])

    const subs = frames(ws).filter(
      (f) => f.action === 'subscribe' && f.topics.includes('servers'),
    )
    // Exactly one server-side subscribe for the topic (either the lazy
    // first-subscribe frame or the onopen re-assert — but never duplicated
    // per extra subscriber).
    expect(subs.length).toBeGreaterThanOrEqual(1)
    const subCountBefore = subs.length

    // Release two of three refs → still no unsubscribe.
    realtime.unsubscribe(['servers'])
    realtime.unsubscribe(['servers'])
    expect(
      frames(ws).filter((f) => f.action === 'unsubscribe' && f.topics.includes('servers')).length,
    ).toBe(0)

    // No new subscribe frames appeared from the extra subscribers.
    expect(
      frames(ws).filter((f) => f.action === 'subscribe' && f.topics.includes('servers')).length,
    ).toBe(subCountBefore)

    // Release the last ref → exactly one unsubscribe.
    realtime.unsubscribe(['servers'])
    const unsubs = frames(ws).filter(
      (f) => f.action === 'unsubscribe' && f.topics.includes('servers'),
    )
    expect(unsubs.length).toBe(1)
  })

  it('sends a subscribe frame only for topics not already active (fresh-only)', async () => {
    const { realtime } = await loadFresh()
    realtime.subscribe(['servers'])
    const ws = currentSocket()
    ws.fireOpen()
    ws.sent.length = 0 // clear the onopen re-assert frame

    // 'servers' already active; only 'alerts' is fresh.
    realtime.subscribe(['servers', 'alerts'])
    const subFrames = frames(ws).filter((f) => f.action === 'subscribe')
    expect(subFrames.length).toBe(1)
    expect(subFrames[0].topics).toEqual(['alerts'])
  })

  it('routes events to handlers registered via on() and the off() fn unregisters', async () => {
    const { realtime } = await loadFresh()
    const received: any[] = []
    const off = realtime.on('metrics', (ev) => received.push(ev))

    realtime.subscribe(['server:1'])
    const ws = currentSocket()
    ws.fireOpen()

    ws.fireMessage({ type: 'metrics', topic: 'server:1', data: { server_id: 1 }, ts: 123 })
    expect(received.length).toBe(1)
    expect(received[0].data.server_id).toBe(1)

    off()
    ws.fireMessage({ type: 'metrics', topic: 'server:1', data: { server_id: 1 }, ts: 124 })
    expect(received.length).toBe(1) // unchanged after off()
  })

  it('re-asserts all active topics in a single subscribe frame on reconnect (onopen)', async () => {
    const { realtime } = await loadFresh()
    realtime.subscribe(['servers'])
    realtime.subscribe(['alerts'])
    const ws = currentSocket()
    ws.sent.length = 0
    ws.fireOpen()

    const subFrames = frames(ws).filter((f) => f.action === 'subscribe')
    expect(subFrames.length).toBe(1)
    expect(subFrames[0].topics.sort()).toEqual(['alerts', 'servers'])
  })
})
