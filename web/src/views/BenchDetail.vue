<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { Boxes, Cpu, Globe2, Layers3, RefreshCw, Server as ServerIcon, Workflow } from 'lucide-vue-next'
import { fetchBench, fetchSites, queryLabelMatchers, type BenchDetail, type SitePair } from '../api'
import { useMetricsRange } from '../composables/useMetricsRange'
import { usePolling } from '../composables/usePolling'
import { useTimeRange } from '../composables/useTimeRange'
import MetricChart from '../components/MetricChart.vue'

const props = defineProps<{ server: string; bench: string }>()
const detail = ref<BenchDetail | null>(null)
const sites = ref<SitePair[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
let requestID = 0
const { range, refreshSec } = useTimeRange()

async function refresh() {
  const request = ++requestID
  loading.value = true; error.value = null
  try {
    const [bench, allSites] = await Promise.all([fetchBench(props.server, props.bench), fetchSites()])
    if (request !== requestID) return
    detail.value = bench
    sites.value = allSites.filter((site) => site.server === props.server && site.bench === props.bench)
  } catch (e) { if (request === requestID) error.value = e instanceof Error ? e.message : String(e) }
  finally { if (request === requestID) loading.value = false }
}
watch(() => [props.server, props.bench], refresh)
const { refresh: refreshNow } = usePolling(refresh, refreshSec)

const labels = computed(() => queryLabelMatchers({ server: props.server, bench: props.bench }))
const supervisorQuery = computed(() => `frappe_bench_supervisor_running{${labels.value}} or frappe_bench_supervisor_total{${labels.value}}`)
const queueQuery = computed(() => `frappe_bench_redis_queue_depth{${labels.value}}`)
const appsQuery = computed(() => `frappe_bench_apps_count{${labels.value}}`)
const workerCapacityQuery = computed(() => `{__name__=~"frappe_bench_workers_(registered|busy|idle)",${labels.value}}`)
const webCapacityQuery = computed(() => `{__name__=~"frappe_bench_web_workers_(configured|running)",${labels.value}}`)
const serviceCPUQuery = computed(() => `topk(8, frappe_bench_service_cpu_percent{${labels.value}})`)
const serviceRSSQuery = computed(() => `topk(8, frappe_bench_service_rss_bytes{${labels.value}} / 1048576)`)
const requestRateQuery = computed(() => `sum by(server, bench) (count_over_time(frappe_site_request_duration_seconds{${labels.value}}[5m])) / 5`)
const requestErrorQuery = computed(() => `100 * sum by(server, bench) (count_over_time(frappe_site_request_status{${labels.value},status_class=~"4xx|5xx"}[5m])) / clamp_min(sum by(server, bench) (count_over_time(frappe_site_request_duration_seconds{${labels.value}}[5m])), 1)`)
const supervisor = useMetricsRange(supervisorQuery, range, refreshSec)
const queues = useMetricsRange(queueQuery, range, refreshSec)
const apps = useMetricsRange(appsQuery, range, refreshSec)
const workerCapacity = useMetricsRange(workerCapacityQuery, range, refreshSec)
const webCapacity = useMetricsRange(webCapacityQuery, range, refreshSec)
const serviceCPU = useMetricsRange(serviceCPUQuery, range, refreshSec)
const serviceRSS = useMetricsRange(serviceRSSQuery, range, refreshSec)
const requestRate = useMetricsRange(requestRateQuery, range, refreshSec)
const requestError = useMetricsRange(requestErrorQuery, range, refreshSec)
const queueRows = computed(() => Object.entries(detail.value?.redis_queues ?? {}).map(([name, depth]) => ({ name, depth })).sort((a, b) => b.depth - a.depth))
const queueBacklog = computed(() => queueRows.value.reduce((sum, queue) => sum + queue.depth, 0))
const healthySites = computed(() => sites.value.filter((site) => site.is_healthy === 1).length)
const issueSites = computed(() => sites.value.filter((site) => site.is_healthy !== 1))
const supervisorHealth = computed(() => {
  if (!detail.value || detail.value.supervisor_total === 0) return { label: 'Not detected', state: 'unknown' }
  const okay = detail.value.supervisor_running >= detail.value.supervisor_total
  return { label: `${detail.value.supervisor_running}/${detail.value.supervisor_total}`, state: okay ? 'healthy' : 'unhealthy' }
})
const workerUtilization = computed(() => detail.value?.workers_registered ? (detail.value.workers_busy / detail.value.workers_registered) * 100 : 0)
const webProcesses = computed(() => {
  const services = detail.value?.services ?? []
  const workers = services.filter((service) => service.kind === 'web-worker' && service.state === 1)
  if (workers.length) return workers.length
  return services.filter((service) => service.kind === 'web' && service.state === 1).length
})
const gunicornDetected = computed(() => (detail.value?.services ?? []).some((service) => service.kind === 'web-master' || service.kind === 'web-worker'))
const webCapacityState = computed(() => {
  const configured = detail.value?.web_workers_configured ?? 0
  if (!configured || !gunicornDetected.value) return 'unknown'
  return webProcesses.value >= configured ? 'healthy' : 'warning'
})
function serviceState(state: number) { return state === 1 ? 'running' : state === 2 ? 'starting' : state === 3 ? 'failed' : 'unknown' }
function serviceKind(kind: string) { return kind.split('-').map((part) => part[0]?.toUpperCase() + part.slice(1)).join(' ') }
function bytes(value: number) { if (!value) return '0 B'; const units=['B','KB','MB','GB']; let index=0; while(value>=1024&&index<units.length-1){value/=1024;index++} return `${value.toFixed(index ? 1 : 0)} ${units[index]}` }
function uptime(seconds: number) { if (!seconds) return '—'; const days=Math.floor(seconds/86400); const hours=Math.floor((seconds%86400)/3600); const mins=Math.floor((seconds%3600)/60); return days ? `${days}d ${hours}h` : hours ? `${hours}h ${mins}m` : `${mins}m` }
function capacityLabel(metric: Record<string, string>) { return metric.__name__?.replace('frappe_bench_', '').replace(/_/g, ' ') ?? 'capacity' }
function serviceLabel(metric: Record<string, string>) { return `${metric.service || 'process'}${metric.kind ? ` · ${serviceKind(metric.kind)}` : ''}` }
</script>

<template>
  <section class="page">
    <nav class="breadcrumbs"><RouterLink to="/benches">Benches</RouterLink><span>/</span><span>{{ props.bench }}</span></nav>
    <header class="page-heading">
      <div class="bench-title"><span class="hero-icon"><Boxes :size="22" /></span><div><span class="eyebrow">Frappe bench</span><h1>{{ props.bench }}</h1><p><ServerIcon :size="13" /> Hosted on <code>{{ props.server }}</code></p></div></div>
      <div class="page-actions"><button class="button" :disabled="loading" @click="refreshNow"><RefreshCw :size="15" :class="{ spin: loading }" />{{ loading ? 'Refreshing' : 'Refresh' }}</button></div>
    </header>
    <div v-if="error" class="notice error">Failed to load bench: {{ error }}</div>

    <div v-if="detail" class="summary-grid">
      <div class="panel summary-card"><Layers3 :size="18" /><div class="summary-label">Frappe version</div><div class="summary-value text-value">{{ detail.frappe_version || 'Unknown' }}</div><div class="summary-note">Runtime framework</div></div>
      <div class="panel summary-card"><Cpu :size="18" /><div class="summary-label">Web processes running</div><div class="summary-value">{{ webProcesses }}<small>/{{ gunicornDetected ? (detail.web_workers_configured || '?') : 'dev' }}</small></div><div class="summary-note"><span class="status-pill compact" :class="webCapacityState">{{ webCapacityState === 'healthy' ? 'Meets Gunicorn configuration' : webCapacityState === 'warning' ? 'Below Gunicorn configuration' : gunicornDetected ? 'Gunicorn configuration unknown' : 'Development server detected' }}</span></div></div>
			<div class="panel summary-card worker-card"><Workflow :size="18" /><div class="summary-label">RQ workers available now</div><div class="summary-value" :class="{ danger: detail.workers_registered > 0 && detail.workers_idle === 0 }">{{ detail.workers_idle }}<small>/{{ detail.workers_registered }}</small></div><div class="summary-note">{{ detail.workers_busy }} busy · {{ workerUtilization.toFixed(0) }}% utilized · {{ detail.background_workers_configured || '?' }} configured</div><div class="capacity-bar"><span :class="{ hot: workerUtilization >= 85 }" :style="{ width: `${workerUtilization}%` }" /></div></div>
      <RouterLink :to="`/sites?server=${encodeURIComponent(props.server)}&bench=${encodeURIComponent(props.bench)}`" class="panel summary-card"><Globe2 :size="18" /><div class="summary-label">Healthy sites</div><div class="summary-value">{{ healthySites }}<small>/{{ sites.length }}</small></div><div class="summary-note">Open site inventory →</div></RouterLink>
    </div>

    <div v-if="detail" class="details-grid">
      <section class="panel queues-panel"><div class="panel-header"><h2 class="panel-title"><Workflow :size="16" /> Queue status</h2><span class="status-pill" :class="queueBacklog > 100 ? 'warning' : 'healthy'">{{ queueBacklog > 100 ? 'Backlog' : 'Normal' }}</span></div><div class="panel-body"><div v-if="queueRows.length" class="queue-list"><div v-for="queue in queueRows" :key="queue.name"><span>{{ queue.name }}</span><strong :class="{ danger: queue.depth > 100 }">{{ queue.depth }}</strong></div></div><p v-else class="muted">No Redis queues reported.</p></div></section>
      <section class="panel sites-panel"><div class="panel-header"><h2 class="panel-title"><Globe2 :size="16" /> Site health</h2><span class="status-pill" :class="issueSites.length ? 'unhealthy' : 'healthy'">{{ issueSites.length ? `${issueSites.length} issues` : 'All healthy' }}</span></div><div class="panel-body"><div class="site-list"><RouterLink v-for="site in sites.slice(0, 6)" :key="site.site" :to="`/sites/${encodeURIComponent(site.server)}/${encodeURIComponent(site.bench)}/${encodeURIComponent(site.site)}`"><span class="status-dot" :class="site.is_healthy === 1 ? 'healthy' : site.is_healthy < 0 ? 'unknown' : 'unhealthy'" />{{ site.site }}<span>{{ site.is_healthy === 1 ? 'Healthy' : site.is_healthy < 0 ? 'Unknown' : 'Unhealthy' }}</span></RouterLink></div><p v-if="!sites.length" class="muted">No sites discovered on this bench.</p></div></section>
    </div>

		<section v-if="detail?.services?.length" class="panel services-panel">
			<div class="panel-header"><div><h2 class="panel-title"><Cpu :size="16" /> Services and processes</h2><p>Supervisor services or inferred bench processes. Monitoring is read-only.</p></div><span class="count-badge">{{ detail.services.length }}</span></div>
			<div class="data-table-wrap"><table class="data-table"><thead><tr><th>Service</th><th>Type</th><th>State</th><th>PID</th><th>CPU</th><th>Memory</th><th>RSS</th><th>Uptime</th></tr></thead><tbody>
				<tr v-for="service in detail.services" :key="service.name"><td><strong>{{ service.name }}</strong><small v-if="service.kind === 'worker'">queue: {{ service.queue || 'all' }}</small></td><td>{{ serviceKind(service.kind) }}</td><td><span class="status-pill" :class="serviceState(service.state) === 'running' ? 'healthy' : serviceState(service.state) === 'failed' ? 'unhealthy' : 'unknown'">{{ serviceState(service.state) }}</span></td><td class="mono">{{ service.pid || '—' }}</td><td>{{ service.cpu_percent.toFixed(1) }}%</td><td>{{ service.memory_percent.toFixed(1) }}%</td><td>{{ bytes(service.rss_bytes) }}</td><td>{{ uptime(service.uptime_seconds) }}</td></tr>
			</tbody></table></div>
		</section>

    <div v-if="detail" class="section-heading"><div><h2>Bench telemetry</h2><p>Historical service capacity, queue pressure, and per-process resource usage.</p></div><span class="status-pill" :class="supervisorHealth.state">Supervisor {{ supervisorHealth.label }}</span></div>
    <div v-if="detail" class="charts capacity-charts">
      <MetricChart title="RQ worker capacity" :result="workerCapacity.result.value" :loading="workerCapacity.loading.value" :error="workerCapacity.error.value" :series-label="capacityLabel" />
      <MetricChart title="Web worker capacity" :result="webCapacity.result.value" :loading="webCapacity.loading.value" :error="webCapacity.error.value" :series-label="capacityLabel" />
      <MetricChart title="Supervisor processes" :result="supervisor.result.value" :loading="supervisor.loading.value" :error="supervisor.error.value" :series-label="(metric) => metric.__name__ ?? 'Supervisor'" />
      <MetricChart title="Redis queue depth" :result="queues.result.value" :loading="queues.loading.value" :error="queues.error.value" :series-label="(metric) => metric.queue ?? 'Queue'" />
      <MetricChart title="Top process CPU" :result="serviceCPU.result.value" :loading="serviceCPU.loading.value" :error="serviceCPU.error.value" :series-label="serviceLabel" y-unit="%" />
      <MetricChart title="Top process RSS" :result="serviceRSS.result.value" :loading="serviceRSS.loading.value" :error="serviceRSS.error.value" :series-label="serviceLabel" y-unit=" MiB" />
      <MetricChart title="Bench requests per minute" :result="requestRate.result.value" :loading="requestRate.loading.value" :error="requestRate.error.value" :series-label="() => 'All sites'" />
      <MetricChart title="Bench HTTP error rate" :result="requestError.result.value" :loading="requestError.loading.value" :error="requestError.error.value" :series-label="() => 'HTTP 4xx + 5xx'" y-unit="%" />
      <MetricChart title="Installed apps" :result="apps.result.value" :loading="apps.loading.value" :error="apps.error.value" :series-label="() => 'Apps'" />
    </div>
  </section>
</template>

<style scoped>
.bench-title { display: flex; align-items: center; gap: .8rem; }.bench-title p { display: flex; align-items: center; gap: .3rem; }.hero-icon { width: 48px; height: 48px; display: grid; place-items: center; color: var(--accent-strong); background: var(--accent-soft); border-radius: 13px; }.summary-card { color: inherit; }.summary-card svg { float: right; color: var(--muted-2); }.summary-value.text-value { font-size: 1.15rem; margin-top: .7rem; }.summary-value small { color: var(--muted-2); font-size: .8rem; }.danger { color: var(--status-unreachable)!important; }.services-panel .data-table{min-width:760px}.services-panel th,.services-panel td{white-space:nowrap}
.capacity-bar{height:4px;margin-top:.55rem;border-radius:999px;overflow:hidden;background:var(--bg-hover)}.capacity-bar span{display:block;height:100%;background:var(--accent);transition:width .2s}.capacity-bar span.hot{background:var(--status-unreachable)}.status-pill.compact{margin-top:.08rem;font-size:.63rem;padding:.17rem .42rem}.details-grid { display: grid; grid-template-columns: minmax(250px,.75fr) minmax(320px,1.25fr); gap: .8rem; margin-top: .8rem; }.queue-list { display: grid; gap: .35rem; }.queue-list div { display: flex; justify-content: space-between; padding: .48rem .55rem; border-radius: 7px; background: var(--bg); font-size: .78rem; }.queue-list span { color: var(--muted); }.site-list { display: grid; gap: .15rem; }.site-list a { display: grid; grid-template-columns: auto 1fr auto; align-items: center; gap: .5rem; padding: .48rem; border-radius: 7px; font-size: .78rem; }.site-list a:hover { background: var(--bg-hover); }.site-list a>span:last-child { color: var(--muted); font-size: .7rem; }.status-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--status-unknown); }.status-dot.healthy { background: var(--status-reachable); }.status-dot.unhealthy { background: var(--status-unreachable); }.services-panel{margin-top:.8rem}.services-panel .panel-header p{margin:.2rem 0 0;color:var(--muted);font-size:.72rem}.services-panel td strong{display:block;color:var(--fg)}.services-panel td small{display:block;margin-top:.15rem;color:var(--muted-2);font-size:.65rem}
.charts { display: grid; grid-template-columns: repeat(2,minmax(0,1fr)); gap: .8rem; }.charts>*:last-child { grid-column: 1/-1; }.capacity-charts>*:last-child{grid-column:auto}
@media(max-width:900px){.details-grid,.charts{grid-template-columns:1fr}.charts>*:last-child{grid-column:auto}}@media(max-width:720px){.bench-title{align-items:flex-start}.hero-icon{width:42px;height:42px}}
</style>
