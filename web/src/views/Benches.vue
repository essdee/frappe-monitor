<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { ArrowRight, Boxes, RefreshCw, Search, Server as ServerIcon } from 'lucide-vue-next'
import { fetchBenches, fetchSites, type BenchPair, type SitePair } from '../api'
import { useTimeRange } from '../composables/useTimeRange'
import { usePolling } from '../composables/usePolling'

const route = useRoute()
const router = useRouter()
const benches = ref<BenchPair[]>([])
const sites = ref<SitePair[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const search = ref(typeof route.query.q === 'string' ? route.query.q : '')
const serverFilter = ref(typeof route.query.server === 'string' ? route.query.server : '')
const { refreshSec } = useTimeRange()

async function refresh() {
  loading.value = true
  error.value = null
  try {
    const [benchRows, siteRows] = await Promise.all([fetchBenches(), fetchSites()])
    benches.value = benchRows
    sites.value = siteRows
  } catch (e) { error.value = e instanceof Error ? e.message : String(e) }
  finally { loading.value = false }
}
const { refresh: refreshNow } = usePolling(refresh, refreshSec)

watch([search, serverFilter], () => {
  const query = { ...route.query }
  if (search.value) query.q = search.value; else delete query.q
  if (serverFilter.value) query.server = serverFilter.value; else delete query.server
  void router.replace({ query })
})

const servers = computed(() => [...new Set(benches.value.map((item) => item.server))].sort())
const rows = computed(() => {
  const needle = search.value.trim().toLowerCase()
  return benches.value.filter((item) =>
    (!serverFilter.value || item.server === serverFilter.value) &&
    (!needle || item.bench.toLowerCase().includes(needle) || item.server.toLowerCase().includes(needle)),
  ).map((item) => {
    const children = sites.value.filter((site) => site.server === item.server && site.bench === item.bench)
    return { ...item, siteCount: children.length, healthyCount: children.filter((site) => site.is_healthy === 1).length, issueCount: children.filter((site) => site.is_healthy !== 1).length }
  }).sort((a, b) => b.issueCount - a.issueCount || a.server.localeCompare(b.server) || a.bench.localeCompare(b.bench))
})
</script>

<template>
  <section class="page">
    <header class="page-heading">
      <div><span class="eyebrow">Frappe workloads</span><h1>Benches</h1><p>Each discovered bench, its site coverage, and whether any workload needs attention.</p></div>
      <div class="page-actions"><button class="button" :disabled="loading" @click="refreshNow"><RefreshCw :size="15" :class="{ spin: loading }" />{{ loading ? 'Refreshing' : 'Refresh' }}</button></div>
    </header>

    <div v-if="error" class="notice error">Failed to load benches: {{ error }}</div>

    <div v-if="benches.length" class="panel toolbar">
      <label class="search"><Search :size="16" /><input v-model="search" type="search" placeholder="Search benches…" /></label>
      <select v-model="serverFilter" aria-label="Filter by server"><option value="">All servers</option><option v-for="server in servers" :key="server" :value="server">{{ server }}</option></select>
      <span>{{ rows.length }} of {{ benches.length }}</span>
    </div>

    <div v-if="rows.length" class="bench-grid">
      <RouterLink v-for="bench in rows" :key="`${bench.server}/${bench.bench}`" :to="`/benches/${encodeURIComponent(bench.server)}/${encodeURIComponent(bench.bench)}`" class="panel bench-card">
        <div class="bench-head"><span class="bench-icon"><Boxes :size="18" /></span><div><strong>{{ bench.bench }}</strong><small><ServerIcon :size="12" /> {{ bench.server }}</small></div><ArrowRight :size="16" class="arrow" /></div>
        <div class="bench-health"><div><span>Sites</span><strong>{{ bench.siteCount }}</strong></div><div><span>Healthy</span><strong class="healthy">{{ bench.healthyCount }}</strong></div><div><span>Attention</span><strong :class="{ danger: bench.issueCount }">{{ bench.issueCount }}</strong></div></div>
      </RouterLink>
    </div>
    <div v-else-if="!loading" class="panel empty-state"><Boxes :size="28" /><h3>{{ benches.length ? 'No matching benches' : 'No benches reporting' }}</h3><p>{{ benches.length ? 'Adjust the search or server filter.' : 'Wait for the first agentless SSH collection.' }}</p></div>
  </section>
</template>

<style scoped>
.toolbar { display: flex; align-items: center; gap: .65rem; padding: .7rem; margin-bottom: 1rem; }.toolbar > span { color: var(--muted-2); font-size: .72rem; white-space: nowrap; }.search { flex: 1; min-height: 39px; display: flex; align-items: center; gap: .45rem; padding: 0 .65rem; border: 1px solid var(--card-border); border-radius: 8px; background: var(--bg); color: var(--muted); }.search input { flex: 1; min-width: 0; border: 0; outline: 0; background: transparent; }.toolbar select { min-height: 39px; padding: 0 .7rem; border: 1px solid var(--card-border); border-radius: 8px; background: var(--bg); }
.bench-grid { display: grid; grid-template-columns: repeat(auto-fill,minmax(290px,1fr)); gap: .8rem; }.bench-card { color: inherit; padding: 1rem; transition: 150ms ease; }.bench-card:hover { color: inherit; transform: translateY(-2px); border-color: var(--accent); }.bench-head { display: flex; align-items: center; gap: .65rem; }.bench-icon { width: 38px; height: 38px; display: grid; place-items: center; color: var(--accent-strong); border-radius: 10px; background: var(--accent-soft); }.bench-head > div { min-width: 0; display: flex; flex-direction: column; }.bench-head strong { color: var(--fg-strong); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.bench-head small { display: inline-flex; align-items: center; gap: .28rem; margin-top: .15rem; color: var(--muted); font-size: .7rem; }.arrow { margin-left: auto; color: var(--muted-2); }.bench-health { display: grid; grid-template-columns: repeat(3,1fr); gap: .4rem; padding-top: .85rem; margin-top: .85rem; border-top: 1px solid var(--card-border); }.bench-health div { display: flex; flex-direction: column; }.bench-health span { color: var(--muted-2); font-size: .65rem; text-transform: uppercase; }.bench-health strong { margin-top: .18rem; font-size: .9rem; }.healthy { color: var(--status-reachable); }.danger { color: var(--status-unreachable); }
@media(max-width:720px){.toolbar{align-items:stretch;flex-direction:column}.toolbar select{width:100%}.toolbar>span{align-self:flex-end}.bench-grid{grid-template-columns:1fr}}
</style>
