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
</style>
