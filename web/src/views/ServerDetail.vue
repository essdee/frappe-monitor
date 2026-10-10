<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import {
  Boxes, Gauge, Globe2, Pencil, RefreshCw, Server as ServerIcon,
} from 'lucide-vue-next'
import {
  fetchBenches,
  fetchServer,
  fetchSites,
  queryLabelMatchers,
  type BenchPair,
  type Server,
  type SitePair,
} from '../api'
import { useTimeRange } from '../composables/useTimeRange'
import { useMetricsRange } from '../composables/useMetricsRange'
import { usePolling } from '../composables/usePolling'
import MetricChart from '../components/MetricChart.vue'
import SystemDetailsCard from '../components/SystemDetailsCard.vue'

const props = defineProps<{ id: number }>()
const { range, refreshSec } = useTimeRange()
const server = ref<Server | null>(null)
const benches = ref<BenchPair[]>([])
const sites = ref<SitePair[]>([])
const serverError = ref<string | null>(null)
let serverRequest = 0

async function loadServer() {
  const request = ++serverRequest
  serverError.value = null
  try {
    const current = await fetchServer(props.id)
    if (request !== serverRequest) return
    server.value = current
    const [benchResult, siteResult] = await Promise.allSettled([fetchBenches(), fetchSites()])
    if (request !== serverRequest) return
    if (benchResult.status === 'fulfilled') benches.value = benchResult.value.filter((item) => item.server === current.name)
    if (siteResult.status === 'fulfilled') sites.value = siteResult.value.filter((item) => item.server === current.name)
  } catch (e) {
    if (request === serverRequest) serverError.value = e instanceof Error ? e.message : String(e)
  }
}

watch(() => props.id, loadServer)
usePolling(loadServer, refreshSec)

const healthySiteCount = computed(() => sites.value.filter((site) => site.is_healthy === 1).length)
const queueLabels = computed(() => server.value ? queryLabelMatchers({ server: server.value.name }) : '')
const cpuQuery = computed(() => !queueLabels.value ? '' : `100 * (1 - rate(frappe_server_cpu_idle{${queueLabels.value}}[5m]) / clamp_min(rate(frappe_server_cpu_user{${queueLabels.value}}[5m]) + rate(frappe_server_cpu_nice{${queueLabels.value}}[5m]) + rate(frappe_server_cpu_system{${queueLabels.value}}[5m]) + rate(frappe_server_cpu_idle{${queueLabels.value}}[5m]) + rate(frappe_server_cpu_iowait{${queueLabels.value}}[5m]) + rate(frappe_server_cpu_irq{${queueLabels.value}}[5m]) + rate(frappe_server_cpu_softirq{${queueLabels.value}}[5m]) + rate(frappe_server_cpu_steal{${queueLabels.value}}[5m]), 1))`)
const memQuery = computed(() => !queueLabels.value ? '' : `100 * (1 - frappe_server_mem_available_bytes{${queueLabels.value}} / frappe_server_mem_total_bytes{${queueLabels.value}})`)
const diskQuery = computed(() => !queueLabels.value ? '' : `100 * frappe_server_disk_used_bytes{${queueLabels.value}} / frappe_server_disk_total_bytes{${queueLabels.value}}`)
const loadQuery = computed(() => !queueLabels.value ? '' : `frappe_server_load_1m{${queueLabels.value}} or frappe_server_load_5m{${queueLabels.value}} or frappe_server_load_15m{${queueLabels.value}}`)
const cpu = useMetricsRange(cpuQuery, range, refreshSec)
const mem = useMetricsRange(memQuery, range, refreshSec)
const disk = useMetricsRange(diskQuery, range, refreshSec)
const load = useMetricsRange(loadQuery, range, refreshSec)
const cpuLabel = () => 'CPU used'
const memLabel = () => 'Memory used'
const diskLabel = (metric: Record<string, string>) => `Mount ${metric.mount ?? '?'}`
const loadLabel = (metric: Record<string, string>) => metric.__name__?.replace('frappe_server_load_', '') ?? 'Load'
</script>

<template>
  <section class="page">
    <nav class="breadcrumbs"><RouterLink to="/servers">Servers</RouterLink><span>/</span><span>{{ server?.name ?? 'Loading' }}</span></nav>
    <header v-if="server" class="page-heading server-heading">
      <div class="server-title">
        <span class="hero-icon"><ServerIcon :size="22" /></span>
        <div>
          <span class="eyebrow">Infrastructure target</span>
          <h1>{{ server.name }}</h1>
          <p><code>{{ server.ssh_user }}@{{ server.hostname }}:{{ server.ssh_port }}</code></p>
        </div>
      </div>
      <div class="heading-status">
        <span class="status-pill" :class="server.status">{{ server.status }}</span>
        <span v-for="(value, key) in server.labels" :key="key" class="label-chip">{{ key }}: {{ value }}</span>
        <RouterLink class="button configure" :to="`/servers/${server.id}/edit`"><Pencil :size="14" />Configure</RouterLink>
      </div>
    </header>

    <div v-if="serverError" class="notice error">Failed to load server: {{ serverError }}</div>

    <div v-if="server" class="summary-grid compact-summary">
      <RouterLink :to="`/benches?server=${encodeURIComponent(server.name)}`" class="panel summary-card"><Boxes :size="18" /><div class="summary-label">Benches</div><div class="summary-value">{{ benches.length }}</div><div class="summary-note">Discovered workloads</div></RouterLink>
      <RouterLink :to="`/sites?server=${encodeURIComponent(server.name)}`" class="panel summary-card"><Globe2 :size="18" /><div class="summary-label">Healthy sites</div><div class="summary-value">{{ healthySiteCount }}<small>/{{ sites.length }}</small></div><div class="summary-note">HTTP probes passing</div></RouterLink>
      <div class="panel summary-card"><Gauge :size="18" /><div class="summary-label">Collection</div><div class="summary-value status-value">{{ server.status === 'reachable' ? 'Active' : 'Paused' }}</div><div class="summary-note">Based on latest SSH result</div></div>
      <div class="panel summary-card"><RefreshCw :size="18" /><div class="summary-label">Discovery</div><div class="summary-value status-value">{{ server.bench_paths.length ? 'Pinned' : 'Automatic' }}</div><div class="summary-note">{{ server.bench_paths.length ? `${server.bench_paths.length} configured paths` : 'Searches common bench paths' }}</div></div>
    </div>

    <SystemDetailsCard v-if="server" :server-id="props.id" />

    <div v-if="server" class="section-heading"><div><h2>Performance</h2><p>Metrics for the selected dashboard time window.</p></div></div>
    <div v-if="server" class="charts">
      <MetricChart title="CPU utilization" :result="cpu.result.value" :loading="cpu.loading.value" :error="cpu.error.value" :series-label="cpuLabel" y-unit="%" />
      <MetricChart title="Memory utilization" :result="mem.result.value" :loading="mem.loading.value" :error="mem.error.value" :series-label="memLabel" y-unit="%" />
      <MetricChart title="Disk utilization by mount" :result="disk.result.value" :loading="disk.loading.value" :error="disk.error.value" :series-label="diskLabel" y-unit="%" />
      <MetricChart title="Load average" :result="load.result.value" :loading="load.loading.value" :error="load.error.value" :series-label="loadLabel" />
    </div>
  </section>
</template>

<style scoped>
.server-title { display: flex; align-items: center; gap: .85rem; }.hero-icon { width: 48px; height: 48px; display: grid; place-items: center; color: var(--accent-strong); background: var(--accent-soft); border: 1px solid color-mix(in srgb, var(--accent) 25%, var(--card-border)); border-radius: 13px; }.server-title p { margin-top: .3rem; }
.heading-status { display: flex; justify-content: flex-end; align-items: center; gap: .4rem; flex-wrap: wrap; }.label-chip { padding: .25rem .5rem; border: 1px solid var(--card-border); border-radius: 999px; color: var(--muted); background: var(--card-bg); font-size: .7rem; }
.configure{min-height:34px;padding:.35rem .65rem;font-size:.75rem}
.compact-summary { margin: 1rem 0 1.25rem; }.compact-summary .summary-card { color: inherit; }.compact-summary svg { float: right; color: var(--muted-2); }.compact-summary small { color: var(--muted-2); font-size: .8rem; }.status-value { font-size: 1.14rem; margin-top: .7rem; }
.charts { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: .8rem; min-width: 0; }
@media (max-width: 980px) { .charts { grid-template-columns: 1fr; } }
@media (max-width: 720px) { .server-heading { gap: .7rem; }.heading-status { justify-content: flex-start; }.hero-icon { width: 42px; height: 42px; } }
</style>
