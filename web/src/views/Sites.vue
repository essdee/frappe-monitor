<script setup lang="ts">
import { computed, onMounted, onScopeDispose, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { fetchSites, type SitePair } from '../api'
import { useTimeRange } from '../composables/useTimeRange'

const sites = ref<SitePair[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const filter = ref('')
const { refreshSec } = useTimeRange()

async function refresh() {
  loading.value = true
  error.value = null
  try {
    sites.value = await fetchSites()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

let timer: ReturnType<typeof setInterval> | null = null
function arm(secs: number) {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
  if (secs > 0) timer = setInterval(refresh, secs * 1000)
}
watch(refreshSec, (s) => arm(s), { immediate: true })
onScopeDispose(() => {
  if (timer) clearInterval(timer)
})
onMounted(refresh)

// Sites can be many — give the operator a substring filter that matches
// across server / bench / site fields.
const filtered = computed<SitePair[]>(() => {
  const q = filter.value.trim().toLowerCase()
  const list = q
    ? sites.value.filter(
        (s) =>
          s.server.toLowerCase().includes(q) ||
          s.bench.toLowerCase().includes(q) ||
          s.site.toLowerCase().includes(q),
      )
    : sites.value
  return [...list].sort(
    (a, b) =>
      a.server.localeCompare(b.server) ||
      a.bench.localeCompare(b.bench) ||
      a.site.localeCompare(b.site),
  )
})
</script>

<template>
  <section>
    <header class="page-header">
      <h2>Sites</h2>
      <div class="header-controls">
        <input
          v-model="filter"
          class="filter"
          type="search"
          placeholder="filter sites…"
        />
        <button class="refresh-btn" :disabled="loading" @click="refresh">
          {{ loading ? 'Refreshing…' : 'Refresh' }}
        </button>
      </div>
    </header>

    <p v-if="error" class="error">Failed to load sites: {{ error }}</p>

    <p v-if="!loading && sites.length === 0" class="muted">
      No sites reporting yet. The site collector populates this list once
      <code>frappe_site_is_healthy</code> is being pushed to VictoriaMetrics.
    </p>

    <p v-else-if="filtered.length === 0" class="muted">
      No sites match <code>{{ filter }}</code>.
    </p>

    <table v-else class="sites-table">
      <thead>
        <tr>
          <th>Site</th>
          <th>Server</th>
          <th>Bench</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="s in filtered" :key="`${s.server}|${s.bench}|${s.site}`">
          <td>
            <RouterLink
              :to="`/sites/${encodeURIComponent(s.server)}/${encodeURIComponent(s.bench)}/${encodeURIComponent(s.site)}`"
            >
              {{ s.site }}
            </RouterLink>
          </td>
          <td class="mono">{{ s.server }}</td>
          <td class="mono">{{ s.bench }}</td>
        </tr>
      </tbody>
    </table>
  </section>
</template>

<style scoped>
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  margin-bottom: 1rem;
  gap: 1rem;
}
.header-controls {
  display: flex;
  gap: 0.5rem;
  align-items: center;
}
.filter {
  background: var(--card-bg);
  color: var(--fg);
  border: 1px solid var(--card-border);
  border-radius: 4px;
  padding: 0.35rem 0.6rem;
  font: inherit;
  width: 220px;
}
.filter:focus {
  outline: none;
  border-color: var(--accent);
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
.sites-table {
  width: 100%;
  border-collapse: collapse;
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  border-radius: 6px;
  overflow: hidden;
}
.sites-table th,
.sites-table td {
  padding: 0.5rem 0.75rem;
  text-align: left;
  border-bottom: 1px solid var(--card-border);
  font-size: 0.9rem;
}
.sites-table tr:last-child td {
  border-bottom: none;
}
.sites-table th {
  font-size: 0.75rem;
  text-transform: uppercase;
  color: var(--muted);
  letter-spacing: 0.05em;
}
.sites-table a {
  color: var(--accent);
  text-decoration: none;
  font-weight: 600;
}
.sites-table a:hover {
  text-decoration: underline;
}
.mono {
  font-family: ui-monospace, "SF Mono", Menlo, monospace;
  color: var(--muted);
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
