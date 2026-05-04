<script setup lang="ts">
import { useTimeRange, PRESETS } from '../composables/useTimeRange'

const { range, refreshSec, selectPreset, setRefresh } = useTimeRange()

const REFRESH_OPTIONS = [
  { label: 'Off', value: 0 },
  { label: '30s', value: 30 },
  { label: '1m', value: 60 },
  { label: '5m', value: 300 },
]

function formatTs(ts: number): string {
  const d = new Date(ts * 1000)
  return d.toISOString().replace('T', ' ').slice(0, 19) + ' UTC'
}
</script>

<template>
  <div class="timeline-filter">
    <div class="presets">
      <button
        v-for="label in Object.keys(PRESETS)"
        :key="label"
        class="preset"
        @click="selectPreset(label)"
      >
        {{ label }}
      </button>
    </div>
    <div class="refresh">
      <label>Refresh:</label>
      <select :value="refreshSec" @change="setRefresh(Number(($event.target as HTMLSelectElement).value))">
        <option v-for="o in REFRESH_OPTIONS" :key="o.value" :value="o.value">
          {{ o.label }}
        </option>
      </select>
    </div>
    <div class="window">
      {{ formatTs(range.from) }} &rarr; {{ formatTs(range.to) }}
    </div>
  </div>
</template>

<style scoped>
.timeline-filter {
  display: flex;
  gap: 1rem;
  align-items: center;
  flex-wrap: wrap;
  padding: 0.5rem 0;
  font-size: 0.85rem;
}
.presets {
  display: flex;
  gap: 0.25rem;
  flex-wrap: wrap;
}
.preset {
  background: var(--card-bg);
  color: var(--fg);
  border: 1px solid var(--card-border);
  padding: 0.25rem 0.65rem;
  border-radius: 4px;
  cursor: pointer;
  font: inherit;
}
.preset:hover {
  border-color: var(--accent);
}
.refresh {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  white-space: nowrap;
}
.refresh select {
  background: var(--card-bg);
  color: var(--fg);
  border: 1px solid var(--card-border);
  padding: 0.2rem 0.4rem;
  border-radius: 4px;
  font: inherit;
}
.window {
  color: var(--muted);
  font-variant-numeric: tabular-nums;
}

/* Mobile: presets become a horizontally-scrollable strip so 1h/3h/6h/
   24h/3d don't wrap into 3 lines, the refresh selector sits to the
   right, and the absolute window timestamps drop out (they're not
   actionable on a phone — the preset gives you the same context). */
@media (max-width: 720px) {
  .timeline-filter {
    gap: 0.5rem;
    padding: 0.25rem 0;
    flex-wrap: wrap;
  }
  .presets {
    flex: 1 1 100%;
    flex-wrap: nowrap;
    overflow-x: auto;
    -webkit-overflow-scrolling: touch;
    /* hide scrollbar; iOS already has overscroll feedback */
    scrollbar-width: none;
  }
  .presets::-webkit-scrollbar { display: none; }
  .preset {
    flex-shrink: 0;
    padding: 0.35rem 0.7rem;
  }
  .refresh {
    margin-left: auto;
  }
  .window {
    flex: 1 1 100%;
    font-size: 0.72rem;
    color: var(--muted-2);
    word-break: break-all;
  }
}
</style>
