<script setup lang="ts">
import { computed } from 'vue'
import { useTimeRange, PRESETS } from '../composables/useTimeRange'
import { useRealtimeStatus } from '../composables/useRealtime'

const { range, selectPreset } = useTimeRange()

// Live push connection — replaces the old refresh-interval dropdown. The
// dashboard updates on server push now, so there's no interval to pick.
const conn = useRealtimeStatus()
const connLabel = computed(() =>
  conn.value === 'open' ? 'Live' : conn.value === 'connecting' ? 'Connecting…' : 'Offline',
)

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
    <div class="conn" :class="conn" :title="`real-time connection: ${connLabel}`">
      <span class="dot"></span>
      <span class="conn-label">{{ connLabel }}</span>
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
.conn {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  white-space: nowrap;
  color: var(--muted);
}
.conn .dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--muted);
}
.conn.open .dot {
  background: var(--status-reachable);
}
.conn.connecting .dot {
  background: var(--status-warning);
}
.conn.closed .dot {
  background: var(--status-unreachable);
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
  .conn {
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
