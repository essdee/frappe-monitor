<script setup lang="ts">
import { computed, ref } from 'vue'
import { useTimeRange } from '../composables/useTimeRange'
import { useServers } from '../composables/useServers'
import ServerCard from '../components/ServerCard.vue'
import AddServerForm from '../components/AddServerForm.vue'

const { refreshSec } = useTimeRange()
const { servers, loading, error, refresh } = useServers(refreshSec)

const showAdd = ref(false)
function onCreated() {
  showAdd.value = false
  refresh()
}

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
      <div class="header-actions">
        <button v-if="!showAdd" class="primary" @click="showAdd = true">+ Add server</button>
        <button class="refresh-btn" :disabled="loading" @click="refresh">
          {{ loading ? 'Refreshing…' : 'Refresh' }}
        </button>
      </div>
    </header>

    <AddServerForm
      v-if="showAdd"
      @created="onCreated"
      @cancelled="showAdd = false"
    />

    <p v-if="error" class="error">Failed to load servers: {{ error }}</p>

    <p v-if="!loading && servers.length === 0 && !showAdd" class="muted">
      No servers yet. Click <strong>+ Add server</strong> above to register your first bench host.
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
.header-actions {
  display: flex;
  gap: 0.5rem;
}
.refresh-btn,
.primary {
  background: var(--card-bg);
  color: var(--fg);
  border: 1px solid var(--card-border);
  padding: 0.35rem 0.85rem;
  border-radius: 4px;
  cursor: pointer;
  font: inherit;
}
.primary {
  background: var(--accent);
  color: white;
  border-color: var(--accent);
}
.primary:hover { opacity: 0.9; }
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
