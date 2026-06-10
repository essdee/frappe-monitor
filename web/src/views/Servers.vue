<script setup lang="ts">
import { computed, ref } from 'vue'
import { Plus, RefreshCw, Server as ServerIcon } from 'lucide-vue-next'
import { useServers } from '../composables/useServers'
import ServerCard from '../components/ServerCard.vue'
import AddServerForm from '../components/AddServerForm.vue'

const { servers, loading, error, refresh } = useServers()

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
      <div class="title">
        <ServerIcon :size="22" :stroke-width="2.25" class="title-icon" />
        <h2>Servers</h2>
        <span class="count">{{ servers.length }}</span>
      </div>
      <div class="header-actions">
        <button v-if="!showAdd" class="primary" @click="showAdd = true">
          <Plus :size="16" :stroke-width="2.5" /> Add server
        </button>
        <button class="refresh-btn" :disabled="loading" @click="refresh">
          <RefreshCw :size="14" :stroke-width="2" :class="{ spin: loading }" />
          {{ loading ? 'Refreshing' : 'Refresh' }}
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
  align-items: center;
  margin-bottom: 1.5rem;
  gap: 1rem;
}
.title {
  display: flex;
  align-items: center;
  gap: 0.6rem;
}
.title-icon {
  color: var(--accent);
}
.title h2 {
  margin: 0;
  font-size: 1.35rem;
  letter-spacing: -0.02em;
  font-weight: 600;
}
.count {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 1.6rem;
  height: 1.4rem;
  padding: 0 0.4rem;
  background: var(--bg-hover);
  color: var(--muted);
  border-radius: 999px;
  font-size: 0.78rem;
  font-weight: 600;
}
.header-actions {
  display: flex;
  gap: 0.5rem;
}
.refresh-btn,
.primary {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  background: var(--card-bg);
  color: var(--fg);
  border: 1px solid var(--card-border);
  padding: 0.45rem 0.9rem;
  border-radius: 6px;
  cursor: pointer;
  font: inherit;
  font-size: 0.88rem;
  font-weight: 500;
  transition: border-color 120ms ease, background 120ms ease;
}
.refresh-btn:hover:not(:disabled),
.primary:hover:not(:disabled) {
  border-color: var(--accent);
  background: var(--bg-hover);
}
.refresh-btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
.primary {
  background: var(--accent);
  color: var(--fg-strong);
  border-color: var(--accent);
}
.primary:hover:not(:disabled) {
  background: var(--accent-strong);
  border-color: var(--accent-strong);
}
.spin {
  animation: spin 0.9s linear infinite;
}
@keyframes spin {
  to { transform: rotate(360deg); }
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

@media (max-width: 720px) {
  .page-header {
    align-items: stretch;
  }
  .header-actions {
    width: 100%;
    justify-content: flex-end;
  }
  .header-actions .primary,
  .header-actions .refresh-btn {
    flex: 1;
    justify-content: center;
  }
  .grid {
    grid-template-columns: 1fr;
    gap: 0.75rem;
  }
}
</style>
