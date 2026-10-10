<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { Globe2, RefreshCw, Search, SlidersHorizontal, X } from 'lucide-vue-next'
import { fetchSites, type SitePair } from '../api'
import { useTimeRange } from '../composables/useTimeRange'
import { usePolling } from '../composables/usePolling'

const route = useRoute()
const router = useRouter()
const sites = ref<SitePair[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const search = ref(typeof route.query.q === 'string' ? route.query.q : '')
const serverFilter = ref(typeof route.query.server === 'string' ? route.query.server : '')
const benchFilter = ref(typeof route.query.bench === 'string' ? route.query.bench : '')
const healthFilter = ref(typeof route.query.health === 'string' ? route.query.health : 'all')
const { refreshSec } = useTimeRange()

async function refresh() {
  loading.value = true; error.value = null
  try { sites.value = await fetchSites() }
  catch (e) { error.value = e instanceof Error ? e.message : String(e) }
  finally { loading.value = false }
}
const { refresh: refreshNow } = usePolling(refresh, refreshSec)

const servers = computed(() => [...new Set(sites.value.map((site) => site.server))].sort())
const benches = computed(() => [...new Set(sites.value.filter((site) => !serverFilter.value || site.server === serverFilter.value).map((site) => site.bench))].sort())
watch(serverFilter, () => { if (benchFilter.value && !benches.value.includes(benchFilter.value)) benchFilter.value = '' })
watch([search, serverFilter, benchFilter, healthFilter], () => {
  const query = { ...route.query }
  const pairs: Array<[string, string, string]> = [['q', search.value, ''], ['server', serverFilter.value, ''], ['bench', benchFilter.value, ''], ['health', healthFilter.value, 'all']]
  for (const [key, value, fallback] of pairs) { if (value && value !== fallback) query[key] = value; else delete query[key] }
  void router.replace({ query })
})

function state(site: SitePair) { return site.is_healthy === 1 ? 'healthy' : site.is_healthy < 0 ? 'unknown' : 'unhealthy' }
const filtered = computed(() => {
  const needle = search.value.trim().toLowerCase()
  const priority: Record<string, number> = { unhealthy: 0, unknown: 1, healthy: 2 }
  return sites.value.filter((site) =>
    (!serverFilter.value || site.server === serverFilter.value) &&
    (!benchFilter.value || site.bench === benchFilter.value) &&
    (healthFilter.value === 'all' || state(site) === healthFilter.value) &&
    (!needle || [site.site, site.server, site.bench].some((item) => item.toLowerCase().includes(needle))),
  ).sort((a, b) => priority[state(a)] - priority[state(b)] || a.site.localeCompare(b.site))
})
const healthy = computed(() => sites.value.filter((site) => site.is_healthy === 1).length)
const unhealthy = computed(() => sites.value.filter((site) => site.is_healthy === 0).length)
const unknown = computed(() => sites.value.filter((site) => site.is_healthy < 0).length)
function clearFilters() { search.value = ''; serverFilter.value = ''; benchFilter.value = ''; healthFilter.value = 'all' }
const hasFilters = computed(() => !!search.value || !!serverFilter.value || !!benchFilter.value || healthFilter.value !== 'all')
</script>

<template>
  <section class="page">
    <header class="page-heading">
      <div><span class="eyebrow">Availability</span><h1>Sites</h1><p>HTTP probe health across every Frappe site, sorted with failures first.</p></div>
      <div class="page-actions"><button class="button" :disabled="loading" @click="refreshNow"><RefreshCw :size="15" :class="{ spin: loading }" />{{ loading ? 'Refreshing' : 'Refresh' }}</button></div>
    </header>

    <div class="summary-grid site-summary">
      <button class="panel summary-card" :class="{ selected: healthFilter === 'all' }" @click="healthFilter = 'all'"><div class="summary-label">All sites</div><div class="summary-value">{{ sites.length }}</div><div class="summary-note">Across {{ servers.length }} servers</div></button>
      <button class="panel summary-card" :class="{ selected: healthFilter === 'healthy' }" @click="healthFilter = 'healthy'"><div class="summary-label">Healthy</div><div class="summary-value healthy-text">{{ healthy }}</div><div class="summary-note">Passing probes</div></button>
      <button class="panel summary-card" :class="{ selected: healthFilter === 'unhealthy' }" @click="healthFilter = 'unhealthy'"><div class="summary-label">Unhealthy</div><div class="summary-value unhealthy-text">{{ unhealthy }}</div><div class="summary-note">Requires attention</div></button>
      <button class="panel summary-card" :class="{ selected: healthFilter === 'unknown' }" @click="healthFilter = 'unknown'"><div class="summary-label">Unknown</div><div class="summary-value unknown-text">{{ unknown }}</div><div class="summary-note">Probe incomplete</div></button>
    </div>

    <div class="panel filters">
      <label class="search"><Search :size="16" /><input v-model="search" type="search" placeholder="Search sites, benches, or servers…" /></label>
      <label class="select"><SlidersHorizontal :size="14" /><select v-model="serverFilter"><option value="">All servers</option><option v-for="server in servers" :key="server">{{ server }}</option></select></label>
      <label class="select"><SlidersHorizontal :size="14" /><select v-model="benchFilter"><option value="">All benches</option><option v-for="bench in benches" :key="bench">{{ bench }}</option></select></label>
      <button v-if="hasFilters" class="button ghost" @click="clearFilters"><X :size="14" /> Clear</button>
      <span class="result-count">{{ filtered.length }} shown</span>
    </div>

    <div v-if="error" class="notice error">Failed to load sites: {{ error }}</div>
    <div v-if="filtered.length" class="panel data-table-wrap">
      <table class="data-table sites-table"><thead><tr><th>Site</th><th>Health</th><th>Server</th><th>Bench</th><th></th></tr></thead>
        <tbody><tr v-for="site in filtered" :key="`${site.server}/${site.bench}/${site.site}`">
          <td><RouterLink class="table-link" :to="`/sites/${encodeURIComponent(site.server)}/${encodeURIComponent(site.bench)}/${encodeURIComponent(site.site)}`"><span class="site-icon"><Globe2 :size="15" /></span>{{ site.site }}</RouterLink></td>
          <td><span class="status-pill" :class="state(site)">{{ state(site) }}</span></td><td class="mono muted">{{ site.server }}</td><td class="mono muted">{{ site.bench }}</td>
          <td class="open-cell"><RouterLink :to="`/sites/${encodeURIComponent(site.server)}/${encodeURIComponent(site.bench)}/${encodeURIComponent(site.site)}`">View →</RouterLink></td>
        </tr></tbody>
      </table>
    </div>
    <div v-else-if="!loading" class="panel empty-state"><Globe2 :size="28" /><h3>{{ sites.length ? 'No sites match these filters' : 'No sites reporting' }}</h3><p>{{ sites.length ? 'Clear one or more filters to see sites.' : 'Sites appear after the first agentless SSH collection.' }}</p></div>
  </section>
</template>

<style scoped>
.site-summary { margin-bottom: 1rem; }.site-summary button { text-align: left; cursor: pointer; color: inherit; }.site-summary button:hover,.site-summary button.selected { border-color: var(--accent); }.healthy-text { color: var(--status-reachable); }.unhealthy-text { color: var(--status-unreachable); }.unknown-text { color: var(--status-unknown); }
.filters { display: flex; align-items: center; gap: .55rem; padding: .7rem; margin-bottom: 1rem; }.search,.select { min-height: 39px; display: flex; align-items: center; gap: .4rem; padding: 0 .65rem; color: var(--muted); background: var(--bg); border: 1px solid var(--card-border); border-radius: 8px; }.search { flex: 1; }.search input,.select select { border: 0; outline: 0; background: transparent; }.search input { width: 100%; }.select select { min-width: 130px; cursor: pointer; }.result-count { margin-left: auto; color: var(--muted-2); font-size: .7rem; white-space: nowrap; }
.site-icon { width: 28px; height: 28px; display: grid; place-items: center; border-radius: 8px; color: var(--accent-strong); background: var(--accent-soft); }.open-cell { text-align: right; font-size: .75rem; color: var(--accent-strong); }
@media(max-width:850px){.filters{align-items:stretch;flex-wrap:wrap}.search{flex:1 1 100%}.select{flex:1}.select select{width:100%}.result-count{align-self:center}}
@media(max-width:720px){.sites-table th:nth-child(3),.sites-table td:nth-child(3),.sites-table th:nth-child(4),.sites-table td:nth-child(4){display:none}.filters .button{width:auto}}
</style>
