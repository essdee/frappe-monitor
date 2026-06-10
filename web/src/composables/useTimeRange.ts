import { computed, type ComputedRef } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import type { TimeRange } from '../api'

/**
 * Quick presets in seconds. The UI's preset buttons map directly to
 * these names; selecting one rewrites the URL with from = now - preset
 * and to = now.
 */
export const PRESETS: Record<string, number> = {
  '15m': 15 * 60,
  '1h': 60 * 60,
  '6h': 6 * 60 * 60,
  '24h': 24 * 60 * 60,
  '7d': 7 * 24 * 60 * 60,
  '30d': 30 * 24 * 60 * 60,
}

/** A reasonable step in seconds given the visible window's span. */
function defaultStepFor(spanSec: number): number {
  if (spanSec <= 60 * 60) return 15 // 1h or less → 15s
  if (spanSec <= 6 * 60 * 60) return 60 // 6h → 1m
  if (spanSec <= 24 * 60 * 60) return 5 * 60 // 24h → 5m
  return 30 * 60 // 7d+ → 30m
}

/** Default refresh interval in seconds (0 = off). */
const DEFAULT_REFRESH = 30

/**
 * useTimeRange exposes the current TimeRange as a ComputedRef derived
 * from URL query params (`?from=&to=&step=&refresh=`), plus mutators
 * that rewrite those params via the router.
 *
 * Defaults: last 1 hour ending "now", step from defaultStepFor,
 * refresh 30s.
 */
export function useTimeRange(): {
  range: ComputedRef<TimeRange>
  refreshSec: ComputedRef<number>
  selectPreset: (label: keyof typeof PRESETS) => void
  setRefresh: (seconds: number) => void
} {
  const route = useRoute()
  const router = useRouter()

  const range = computed<TimeRange>(() => {
    const now = Math.floor(Date.now() / 1000)
    const fromQ = Number(route.query.from)
    const toQ = Number(route.query.to)
    const from = Number.isFinite(fromQ) && fromQ > 0 ? fromQ : now - PRESETS['1h']
    const to = Number.isFinite(toQ) && toQ > 0 ? toQ : now
    const span = Math.max(1, to - from)
    const stepQ = Number(route.query.step)
    const step = Number.isFinite(stepQ) && stepQ > 0 ? stepQ : defaultStepFor(span)
    return { from, to, step }
  })

  const refreshSec = computed<number>(() => {
    const r = Number(route.query.refresh)
    if (route.query.refresh === undefined) return DEFAULT_REFRESH
    if (!Number.isFinite(r) || r < 0) return DEFAULT_REFRESH
    if (r === 0) return 0 // explicit "off"
    // Floor at 5s so a crafted/bookmarked ?refresh=0.001 can't turn the
    // poller into a sub-millisecond request storm (one tab × N charts).
    return Math.max(5, r)
  })

  function selectPreset(label: keyof typeof PRESETS) {
    const span = PRESETS[label]
    const now = Math.floor(Date.now() / 1000)
    router.replace({
      query: { ...route.query, from: String(now - span), to: String(now) },
    })
  }

  function setRefresh(seconds: number) {
    router.replace({ query: { ...route.query, refresh: String(seconds) } })
  }

  return { range, refreshSec, selectPreset, setRefresh }
}
