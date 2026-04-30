<script setup lang="ts">
import { computed } from 'vue'
import { useTimeRange } from '../composables/useTimeRange'
import { useServers } from '../composables/useServers'
import ServerCard from '../components/ServerCard.vue'

const { refreshSec } = useTimeRange()
const { servers, loading, error, refresh } = useServers(refreshSec)

// Worst-first: unreachable → unknown → reachable, then by name.
const sorted = computed(() => {
  const order: Record<string, number> = { unreachable: 0, unknown: 1, reachable: 2 }
  return [...servers.value].sort((a, b) => {
    const ord = (order[a.status] ?? 3) - (order[b.status] ?? 3)
    if (ord !== 0) return ord
    return a.name.localeCompare(b.name)
  })
})
</script>

<template>
  <section>
    <header class="page-header">
      <h2>Servers</h2>
      <button class="refresh-btn" :disabled="loading" @click="refresh">
        {{ loading ? 'Refreshing…' : 'Refresh' }}
      </button>
    </header>

    <p v-if="error" class="error">Failed to load servers: {{ error }}</p>

    <p v-if="!loading && servers.length === 0" class="muted">
      No servers yet. POST one via <code>POST /api/v1/servers</code> to get started.
    </p>

    <div v-else class="grid">
      <ServerCard v-for="srv in sorted" :key="srv.id" :server="srv" />
    </div>
  </section>
</template>

<style scoped>
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  margin-bottom: 1rem;
}
.refresh-btn {
  background: var(--card-bg);
  color: var(--fg);
  border: 1px solid var(--card-border);
  padding: 0.35rem 0.85rem;
  border-radius: 4px;
  cursor: pointer;
  font: inherit;
}
.refresh-btn:hover:not(:disabled) {
  border-color: var(--accent);
}
.refresh-btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 1rem;
}
.error {
  padding: 0.5rem 0.75rem;
  background: color-mix(in srgb, var(--status-unreachable) 12%, transparent);
  color: var(--status-unreachable);
  border-radius: 4px;
}
.muted {
  color: var(--muted);
}
</style>
