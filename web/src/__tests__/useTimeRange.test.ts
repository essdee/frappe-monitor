import { describe, it, expect, vi, beforeEach } from 'vitest'
import { reactive } from 'vue'

// useTimeRange derives a TimeRange + refresh interval from the URL query and
// exposes mutators that rewrite the query via vue-router. We mock useRoute /
// useRouter with a reactive fake so we can poke query params directly and
// observe the computed clamping/fallback behavior — no real router needed.

// Shared reactive route mock the composable reads from.
const route = reactive<{ query: Record<string, any> }>({ query: {} })
const replaceMock = vi.fn((arg: { query: Record<string, any> }) => {
  route.query = { ...arg.query }
})

vi.mock('vue-router', () => ({
  useRoute: () => route,
  useRouter: () => ({ replace: replaceMock, push: vi.fn() }),
}))

// Import AFTER the mock is registered.
import { useTimeRange, PRESETS } from '../composables/useTimeRange'

beforeEach(() => {
  route.query = {}
  replaceMock.mockClear()
})

describe('useTimeRange.range defaults & parsing', () => {
  it('defaults to a 1h window ending ~now with no query params', () => {
    const now = Math.floor(Date.now() / 1000)
    const { range } = useTimeRange()
    const r = range.value
    expect(r.to).toBeGreaterThanOrEqual(now - 2)
    expect(r.to).toBeLessThanOrEqual(now + 2)
    expect(r.to - r.from).toBe(PRESETS['1h'])
  })

  it('honors valid from/to query params', () => {
    route.query = { from: '1000', to: '4600' }
    const { range } = useTimeRange()
    expect(range.value.from).toBe(1000)
    expect(range.value.to).toBe(4600)
  })

  it('falls back to defaults for non-positive / non-finite from/to', () => {
    const now = Math.floor(Date.now() / 1000)
    route.query = { from: '-5', to: 'notanumber' }
    const { range } = useTimeRange()
    const r = range.value
    // from invalid (≤0) → default now-1h; to invalid (NaN) → default now.
    expect(r.from).toBe(r.to - PRESETS['1h'])
    expect(r.to).toBeGreaterThanOrEqual(now - 2)
    expect(r.to).toBeLessThanOrEqual(now + 2)
  })

  it('derives a sensible step from the span when step is absent', () => {
    // 24h window → 5m step per defaultStepFor.
    const now = Math.floor(Date.now() / 1000)
    route.query = { from: String(now - 24 * 3600), to: String(now) }
    const { range } = useTimeRange()
    expect(range.value.step).toBe(5 * 60)
  })

  it('honors a valid explicit step and ignores a non-positive one', () => {
    route.query = { from: '1000', to: '2000', step: '7' }
    expect(useTimeRange().range.value.step).toBe(7)

    route.query = { from: '1000', to: '2000', step: '0' }
    // step ≤ 0 → fall back to defaultStepFor(span=1000) which is 15.
    expect(useTimeRange().range.value.step).toBe(15)
  })
})

describe('useTimeRange.refreshSec clamping & fallbacks', () => {
  it('returns the 30s default when refresh is absent', () => {
    const { refreshSec } = useTimeRange()
    expect(refreshSec.value).toBe(30)
  })

  it('treats an explicit 0 as "off"', () => {
    route.query = { refresh: '0' }
    expect(useTimeRange().refreshSec.value).toBe(0)
  })

  it('floors small positive values at 5s (anti request-storm)', () => {
    route.query = { refresh: '0.001' }
    expect(useTimeRange().refreshSec.value).toBe(5)

    route.query = { refresh: '3' }
    expect(useTimeRange().refreshSec.value).toBe(5)
  })

  it('passes through values at/above the 5s floor', () => {
    route.query = { refresh: '5' }
    expect(useTimeRange().refreshSec.value).toBe(5)
    route.query = { refresh: '45' }
    expect(useTimeRange().refreshSec.value).toBe(45)
  })

  it('falls back to the default for negative or non-finite refresh', () => {
    route.query = { refresh: '-10' }
    expect(useTimeRange().refreshSec.value).toBe(30)
    route.query = { refresh: 'abc' }
    expect(useTimeRange().refreshSec.value).toBe(30)
  })
})

describe('useTimeRange mutators', () => {
  it('selectPreset rewrites from/to to a now-anchored window of the preset span', () => {
    const now = Math.floor(Date.now() / 1000)
    const { selectPreset } = useTimeRange()
    selectPreset('6h')
    expect(replaceMock).toHaveBeenCalledTimes(1)
    const q = replaceMock.mock.calls[0][0].query
    expect(Number(q.to)).toBeGreaterThanOrEqual(now - 2)
    expect(Number(q.to) - Number(q.from)).toBe(PRESETS['6h'])
  })

  it('setRefresh writes the refresh query param while preserving others', () => {
    route.query = { from: '1', to: '2' }
    const { setRefresh } = useTimeRange()
    setRefresh(60)
    const q = replaceMock.mock.calls[0][0].query
    expect(q.refresh).toBe('60')
    expect(q.from).toBe('1')
    expect(q.to).toBe('2')
  })
})
