<script setup lang="ts">
import { computed, ref } from 'vue'
import { Plus, RefreshCw, Search, Server as ServerIcon, X } from 'lucide-vue-next'
import { RouterLink } from 'vue-router'
import { useTimeRange } from '../composables/useTimeRange'
import { useFleetData } from '../composables/useFleetData'
import ServerCard from '../components/ServerCard.vue'

const { refreshSec } = useTimeRange()
const fleet = useFleetData(refreshSec)
const search = ref('')
const status = ref('all')
const filtered = computed(() => {
  const priority = { unreachable: 0, unknown: 1, reachable: 2 }
  const needle = search.value.trim().toLowerCase()
  return [...fleet.servers.value]
    .filter((server) => status.value === 'all' || server.status === status.value)
    .filter((server) => !needle || [server.name, server.hostname, server.ssh_user, ...Object.values(server.labels ?? {})].some((value) => value.toLowerCase().includes(needle)))
    .sort((a, b) => priority[a.status] - priority[b.status] || a.name.localeCompare(b.name))
})
function countsFor(server: string) {
  const siteRows = fleet.sites.value.filter((item) => item.server === server)
  return { benches: fleet.benches.value.filter((item) => item.server === server).length, sites: siteRows.length, healthy: siteRows.filter((item) => item.is_healthy === 1).length }
}
</script>

<template>
  <section class="page">
    <header class="page-heading"><div><span class="eyebrow">Infrastructure</span><h1>Servers</h1><p>Read-only SSH health and workload coverage for every configured Frappe host.</p></div><div class="page-actions"><button class="button" :disabled="fleet.loading.value" @click="fleet.refresh"><RefreshCw :size="15" :class="{ spin: fleet.loading.value }" />{{ fleet.loading.value ? 'Refreshing' : 'Refresh view' }}</button><RouterLink class="button primary" to="/servers/new"><Plus :size="16" />Add server</RouterLink></div></header>
    <div v-if="fleet.errors.value.length" class="notice error">Some fleet data is unavailable: {{ fleet.errors.value.join(' · ') }}</div>
    <div v-if="fleet.servers.value.length" class="toolbar panel"><label class="search-box"><Search :size="16" /><input v-model="search" type="search" placeholder="Search name, host, user, or label…" /><button v-if="search" type="button" aria-label="Clear search" @click="search = ''"><X :size="14" /></button></label><div class="status-filters" aria-label="Filter by status"><button v-for="item in ['all','reachable','unreachable','unknown']" :key="item" :class="{active:status===item}" @click="status=item">{{ item }}</button></div><span class="result-count">{{ filtered.length }} of {{ fleet.servers.value.length }}</span></div>
    <div v-if="filtered.length" class="server-grid"><ServerCard v-for="server in filtered" :key="server.id" :server="server" :bench-count="countsFor(server.name).benches" :site-count="countsFor(server.name).sites" :healthy-site-count="countsFor(server.name).healthy" /></div>
    <div v-else-if="!fleet.loading.value" class="panel empty-state"><ServerIcon :size="28" /><h3>{{ fleet.servers.value.length ? 'No matching servers' : 'No configured servers' }}</h3><p>{{ fleet.servers.value.length ? 'Clear the search or choose another health state.' : 'Register your first Frappe server and verify SSH from this console.' }}</p><RouterLink v-if="!fleet.servers.value.length" class="button primary empty-action" to="/servers/new"><Plus :size="16" />Add first server</RouterLink></div>
  </section>
</template>

<style scoped>
.toolbar{display:flex;align-items:center;gap:.75rem;padding:.7rem;margin-bottom:1rem}.search-box{min-width:240px;flex:1;display:flex;align-items:center;gap:.45rem;min-height:38px;padding:0 .65rem;color:var(--muted);background:var(--bg);border:1px solid var(--card-border);border-radius:8px}.search-box:focus-within{border-color:var(--accent)}.search-box input{width:100%;border:0;outline:0;background:transparent;font-size:.82rem}.search-box button{width:28px;height:28px;padding:0;display:grid;place-items:center;border:0;border-radius:6px;color:var(--muted);background:transparent;cursor:pointer}.status-filters{display:flex;gap:.15rem;padding:.18rem;border:1px solid var(--card-border);border-radius:8px}.status-filters button{min-height:30px;padding:0 .55rem;color:var(--muted);border:0;border-radius:6px;background:transparent;cursor:pointer;font-size:.71rem;font-weight:650;text-transform:capitalize}.status-filters button.active{color:var(--fg);background:var(--bg-hover)}.result-count{color:var(--muted-2);font-size:.72rem;white-space:nowrap}.server-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(300px,1fr));gap:.8rem}@media(max-width:800px){.toolbar{align-items:stretch;flex-direction:column}.search-box{width:100%}.status-filters{width:100%;overflow-x:auto}.status-filters button{flex:1}.result-count{align-self:flex-end}}@media(max-width:720px){.server-grid{grid-template-columns:1fr}}
.empty-action{margin-top:1rem}
</style>
