<script setup lang="ts">
import { Clock3, Radio } from 'lucide-vue-next'
import { PRESETS, useTimeRange } from '../composables/useTimeRange'

const { refreshSec, activePreset, isLive, selectPreset, setRefresh } = useTimeRange()
const REFRESH_OPTIONS = [
  { label: 'Manual', value: 0 },
  { label: '30 sec', value: 30 },
  { label: '1 min', value: 60 },
  { label: '5 min', value: 300 },
]
</script>

<template>
  <div class="timeline-filter" aria-label="Dashboard time controls">
    <div class="live-state" :class="{ live: isLive }">
      <Radio :size="13" />
      {{ isLive ? 'Live' : 'Fixed' }}
    </div>
    <div class="presets" aria-label="Time window">
      <button
        v-for="label in Object.keys(PRESETS)"
        :key="label"
        class="preset"
        :class="{ active: activePreset === label }"
        type="button"
        @click="selectPreset(label)"
      >{{ label }}</button>
    </div>
    <label class="refresh">
      <Clock3 :size="14" />
      <span class="sr-only">Refresh interval</span>
      <select :value="refreshSec" @change="setRefresh(Number(($event.target as HTMLSelectElement).value))">
        <option v-for="option in REFRESH_OPTIONS" :key="option.value" :value="option.value">
          {{ option.label }}
        </option>
      </select>
    </label>
  </div>
</template>

<style scoped>
.timeline-filter { display: flex; align-items: center; justify-content: flex-end; gap: .55rem; min-width: 0; }
.live-state { min-height: 32px; display: inline-flex; align-items: center; gap: .3rem; padding: 0 .55rem; color: var(--muted); font-size: .72rem; font-weight: 700; border-right: 1px solid var(--card-border); }
.live-state.live { color: var(--status-reachable); }
.presets { display: flex; padding: .2rem; gap: .15rem; background: color-mix(in srgb, var(--card-bg) 90%, transparent); border: 1px solid var(--card-border); border-radius: 9px; }
.preset { min-height: 28px; padding: 0 .52rem; border: 0; border-radius: 6px; color: var(--muted); background: transparent; cursor: pointer; font-size: .72rem; font-weight: 650; }
.preset:hover { color: var(--fg); background: var(--bg-hover); }
.preset.active { color: white; background: var(--accent); box-shadow: 0 3px 10px rgba(81, 102, 223, .22); }
.refresh { min-height: 34px; display: inline-flex; align-items: center; gap: .25rem; padding-left: .45rem; border: 1px solid var(--card-border); border-radius: 9px; color: var(--muted); background: var(--card-bg); }
.refresh select { min-height: 32px; border: 0; padding: 0 .5rem 0 .2rem; color: var(--fg); background: transparent; cursor: pointer; font-size: .74rem; }
.refresh select:focus { outline: 0; }
.sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0,0,0,0); }
@media (max-width: 720px) {
  .timeline-filter { width: 100%; justify-content: flex-start; overflow-x: auto; scrollbar-width: none; }
  .timeline-filter::-webkit-scrollbar { display: none; }
  .live-state { flex: 0 0 auto; }
  .presets { flex: 0 0 auto; }
  .refresh { flex: 0 0 auto; }
}
</style>
