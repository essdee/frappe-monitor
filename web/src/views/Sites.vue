<script setup lang="ts">
import { computed, onMounted, onScopeDispose, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import {
  Globe,
  RefreshCw,
  Search,
  Filter,
  X,
} from 'lucide-vue-next'
import { fetchSites, type SitePair } from '../api'
import { useTimeRange } from '../composables/useTimeRange'

const sites = ref<SitePair[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const filter = ref('')
const serverFilter = ref<string>('')
const benchFilter = ref<string>('')
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

// Distinct values for the dropdowns. Re-derive every time `sites`
// changes; cheap (O(n) over a typically small list).
const allServers = computed(() => {
  return Array.from(new Set(sites.value.map((s) => s.server))).sort()
})
const allBenches = computed(() => {
  // If a server is selected, scope bench options to that server.
  const candidates = serverFilter.value
    ? sites.value.filter((s) => s.server === serverFilter.value)
    : sites.value
  return Array.from(new Set(candidates.map((s) => s.bench))).sort()
})

const filtered = computed<SitePair[]>(() => {
  const q = filter.value.trim().toLowerCase()
  let list = sites.value
  if (serverFilter.value) list = list.filter((s) => s.server === serverFilter.value)
  if (benchFilter.value) list = list.filter((s) => s.bench === benchFilter.value)
  if (q) {
    list = list.filter(
      (s) =>
        s.server.toLowerCase().includes(q) ||
        s.bench.toLowerCase().includes(q) ||
        s.site.toLowerCase().includes(q),
    )
  }
  return [...list].sort(
    (a, b) =>
      a.server.localeCompare(b.server) ||
      a.bench.localeCompare(b.bench) ||
      a.site.localeCompare(b.site),
  )
})

function clearAll() {
  filter.value = ''
  serverFilter.value = ''
  benchFilter.value = ''
}

const hasFilters = computed(
  () => filter.value !== '' || serverFilter.value !== '' || benchFilter.value !== '',
)
</script>

<template>
  <section>
    <header class="page-header">
      <div class="title">
        <Globe :size="22" :stroke-width="2.25" class="title-icon" />
        <h2>Sites</h2>
        <span class="count">{{ filtered.length }}<span v-if="filtered.length !== sites.length"> / {{ sites.length }}</span></span>
      </div>
      <button class="refresh-btn" :disabled="loading" @click="refresh">
        <RefreshCw :size="14" :stroke-width="2" :class="{ spin: loading }" />
        {{ loading ? 'Refreshing' : 'Refresh' }}
      </button>
    </header>

    <div class="filters">
      <div class="filter-bar">
        <div class="search-input">
          <Search :size="16" :stroke-width="2" class="search-icon" />
          <input
            v-model="filter"
            type="search"
            placeholder="Search sites…"
          />
        </div>
        <div class="select-input">
          <Filter :size="14" :stroke-width="2" class="select-icon" />
          <select v-model="serverFilter">
            <option value="">All servers</option>
            <option v-for="srv in allServers" :key="srv" :value="srv">
              {{ srv }}
            </option>
          </select>
        </div>
        <div class="select-input">
          <Filter :size="14" :stroke-width="2" class="select-icon" />
          <select v-model="benchFilter">
            <option value="">All benches</option>
            <option v-for="b in allBenches" :key="b" :value="b">
              {{ b }}
            </option>
          </select>
        </div>
        <button v-if="hasFilters" class="clear-btn" @click="clearAll">
          <X :size="14" :stroke-width="2.5" /> Clear
        </button>
      </div>
    </div>

    <p v-if="error" class="error">Failed to load sites: {{ error }}</p>

    <p v-if="!loading && sites.length === 0" class="muted">
      No sites reporting yet. Once <code>frappe_site_is_healthy</code> is being
      pushed to VictoriaMetrics, sites will appear here.
    </p>

    <p v-else-if="filtered.length === 0" class="muted">
      No sites match the current filters.
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
          <td data-label="Site">
            <RouterLink
              :to="`/sites/${encodeURIComponent(s.server)}/${encodeURIComponent(s.bench)}/${encodeURIComponent(s.site)}`"
            >
              <Globe :size="14" :stroke-width="2" class="row-icon" />
              {{ s.site }}
            </RouterLink>
          </td>
          <td class="mono" data-label="Server">{{ s.server }}</td>
          <td class="mono" data-label="Bench">{{ s.bench }}</td>
        </tr>
      </tbody>
    </table>
  </section>
</template>

<style scoped>
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 1.25rem;
}
.title {
  display: flex;
  align-items: center;
  gap: 0.6rem;
}
.title-icon { color: var(--accent); }
.title h2 {
  margin: 0;
  font-size: 1.35rem;
  font-weight: 600;
  letter-spacing: -0.02em;
}
.count {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 0 0.5rem;
  height: 1.4rem;
  background: var(--bg-hover);
  color: var(--muted);
  border-radius: 999px;
  font-size: 0.78rem;
  font-weight: 600;
}
.refresh-btn {
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
}
.refresh-btn:hover:not(:disabled) {
  border-color: var(--accent);
  background: var(--bg-hover);
}
.refresh-btn:disabled { opacity: 0.6; cursor: not-allowed; }
.spin { animation: spin 0.9s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
.filters {
  margin-bottom: 1rem;
}
.filter-bar {
  display: flex;
  gap: 0.5rem;
  flex-wrap: wrap;
  align-items: center;
}
.search-input,
.select-input {
  position: relative;
  display: inline-flex;
  align-items: center;
}
.search-input input,
.select-input select {
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  border-radius: 6px;
  color: var(--fg);
  font: inherit;
  font-size: 0.88rem;
  padding: 0.45rem 0.75rem 0.45rem 2.1rem;
  transition: border-color 120ms;
}
.search-input { width: 280px; }
.search-input input { width: 100%; }
.select-input select {
  appearance: none;
  cursor: pointer;
  padding-right: 2rem;
  min-width: 160px;
}
.search-input input:focus,
.select-input select:focus {
  outline: none;
  border-color: var(--accent);
}
.search-icon,
.select-icon {
  position: absolute;
  left: 0.65rem;
  color: var(--muted);
  pointer-events: none;
}
.clear-btn {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  background: transparent;
  border: 1px solid var(--card-border);
  color: var(--muted);
  border-radius: 6px;
  padding: 0.45rem 0.7rem;
  font: inherit;
  font-size: 0.85rem;
  cursor: pointer;
}
.clear-btn:hover {
  color: var(--fg);
  border-color: var(--accent);
}
.sites-table {
  width: 100%;
  border-collapse: separate;
  border-spacing: 0;
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  border-radius: 8px;
  overflow: hidden;
  box-shadow: var(--card-shadow);
}
.sites-table th,
.sites-table td {
  padding: 0.65rem 0.9rem;
  text-align: left;
  border-bottom: 1px solid var(--card-border);
  font-size: 0.88rem;
}
.sites-table tbody tr:last-child td { border-bottom: none; }
.sites-table tbody tr:hover { background: var(--bg-hover); }
.sites-table th {
  font-size: 0.72rem;
  text-transform: uppercase;
  color: var(--muted);
  letter-spacing: 0.08em;
  background: var(--bg);
  font-weight: 600;
}
.sites-table a {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  color: var(--accent);
  text-decoration: none;
  font-weight: 500;
}
.sites-table a:hover { color: var(--accent-strong); }
.row-icon { color: var(--muted-2); }
.sites-table a:hover .row-icon { color: var(--accent); }
.mono {
  font-family: "JetBrains Mono", ui-monospace, "SF Mono", Menlo, monospace;
  color: var(--muted);
  font-size: 0.82rem;
}
.error {
  padding: 0.6rem 0.85rem;
  background: color-mix(in srgb, var(--status-unreachable) 12%, transparent);
  border: 1px solid color-mix(in srgb, var(--status-unreachable) 35%, transparent);
  color: var(--status-unreachable);
  border-radius: 6px;
}
.muted {
  color: var(--muted);
}

@media (max-width: 720px) {
  .page-header { flex-wrap: wrap; }
  .filter-bar {
    flex-direction: column;
    align-items: stretch;
  }
  .search-input,
  .select-input {
    width: 100%;
  }
  .search-input input,
  .select-input select {
    width: 100%;
  }
  .clear-btn {
    align-self: flex-end;
  }

  /* Sites table → card list. The 3-column layout on a 360px viewport
     forces every cell to scroll; vertical cards are more legible.
     Same data-label trick the Alerts page uses. */
  .sites-table {
    display: block;
    border: none;
    background: transparent;
    box-shadow: none;
    overflow: visible;
    white-space: normal;
  }
  .sites-table thead { display: none; }
  .sites-table tbody { display: block; }
  .sites-table tr {
    display: block;
    background: var(--card-bg);
    border: 1px solid var(--card-border);
    border-radius: 8px;
    padding: 0.55rem 0.8rem;
    margin-bottom: 0.5rem;
    box-shadow: var(--card-shadow);
  }
  .sites-table td {
    display: block;
    padding: 0.2rem 0;
    border: none;
    font-size: 0.85rem;
    word-break: break-word;
  }
  .sites-table td::before {
    content: attr(data-label);
    display: block;
    font-size: 0.65rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--muted);
    font-weight: 600;
    margin-bottom: 0.1rem;
  }
  .sites-table td:first-child::before {
    /* Site row reads as the card title; suppress redundant "Site"
       label and bump font size. */
    display: none;
  }
  .sites-table td:first-child {
    font-size: 0.95rem;
    margin-bottom: 0.25rem;
  }
}
</style>
