import { computed, customRef, onScopeDispose, watch, type ComputedRef, type Ref } from 'vue'
import { useRoute, useRouter, type LocationQuery } from 'vue-router'
import type { TimeRange } from '../api'

export const PRESETS: Record<string, number> = {
  '15m': 15 * 60,
  '1h': 60 * 60,
  '6h': 6 * 60 * 60,
  '24h': 24 * 60 * 60,
  '7d': 7 * 24 * 60 * 60,
  '30d': 30 * 24 * 60 * 60,
}

export function defaultStepFor(spanSec: number): number {
  if (spanSec <= 60 * 60) return 15
  if (spanSec <= 6 * 60 * 60) return 60
  if (spanSec <= 24 * 60 * 60) return 5 * 60
  return 30 * 60
}

const DEFAULT_REFRESH = 30
const DEFAULT_PRESET = '1h'

function positiveNumber(value: unknown): number | null {
  const parsed = Number(value)
  return Number.isFinite(parsed) && parsed > 0 ? parsed : null
}

/** Resolve URL state at call time. Live presets deliberately use `nowSec`. */
export function resolveTimeRange(query: LocationQuery, nowSec: number): TimeRange {
  const preset = typeof query.preset === 'string' ? query.preset : ''
  const presetSpan = PRESETS[preset]
  const explicitFrom = positiveNumber(query.from)
  const explicitTo = positiveNumber(query.to)

  let from: number
  let to: number
  if (presetSpan) {
    from = nowSec - presetSpan
    to = nowSec
  } else if (explicitFrom !== null && explicitTo !== null && explicitTo > explicitFrom) {
    from = explicitFrom
    to = explicitTo
  } else {
    from = nowSec - PRESETS[DEFAULT_PRESET]
    to = nowSec
  }

  const span = Math.max(1, to - from)
  const step = positiveNumber(query.step) ?? defaultStepFor(span)
  return { from, to, step }
}

export function useTimeRange(): {
  range: Ref<TimeRange>
  refreshSec: ComputedRef<number>
  activePreset: ComputedRef<string>
  isLive: ComputedRef<boolean>
  selectPreset: (label: keyof typeof PRESETS) => void
  setRefresh: (seconds: number) => void
} {
  const route = useRoute()
  const router = useRouter()

  // A normal computed caches Date.now(), freezing the window. This custom
  // ref resolves wall-clock time on every access while still notifying
  // watchers when the URL state changes. Polling callers therefore receive
  // a fresh window without creating a global clock or duplicate requests.
  let notifyRangeChanged: () => void = () => undefined
  const range = customRef<TimeRange>((track, trigger) => {
    notifyRangeChanged = trigger
    return {
      get() {
        track()
        return resolveTimeRange(route.query, Math.floor(Date.now() / 1000))
      },
      set() {
        // URL query parameters are the only writable source of truth.
      },
    }
  })
  const stopRangeWatch = watch(
    () => [route.query.preset, route.query.from, route.query.to, route.query.step],
    notifyRangeChanged,
  )
  onScopeDispose(stopRangeWatch)

  const refreshSec = computed<number>(() => {
    const value = Number(route.query.refresh)
    if (route.query.refresh === undefined) return DEFAULT_REFRESH
    return Number.isFinite(value) && value >= 0 ? value : DEFAULT_REFRESH
  })

  const activePreset = computed(() => {
    if (typeof route.query.preset === 'string' && PRESETS[route.query.preset]) {
      return route.query.preset
    }
    if (route.query.from === undefined && route.query.to === undefined) {
      return DEFAULT_PRESET
    }
    return ''
  })
  const isLive = computed(() => activePreset.value !== '')

  function selectPreset(label: keyof typeof PRESETS) {
    const query: LocationQuery = { ...route.query, preset: String(label) }
    delete query.from
    delete query.to
    delete query.step
    void router.replace({ query })
  }

  function setRefresh(seconds: number) {
    const safe = Number.isFinite(seconds) && seconds >= 0 ? seconds : DEFAULT_REFRESH
    void router.replace({ query: { ...route.query, refresh: String(safe) } })
  }

  return { range, refreshSec, activePreset, isLive, selectPreset, setRefresh }
}
