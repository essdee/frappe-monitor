<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { Activity, ExternalLink, FileWarning, Globe2, RefreshCw, Server as ServerIcon, Timer, Workflow } from 'lucide-vue-next'
import { fetchSite, queryLabelMatchers, queryLogs, queryStringLiteral, type SiteDetail } from '../api'
import { useMetricsRange } from '../composables/useMetricsRange'
import { usePolling } from '../composables/usePolling'
import { useTimeRange } from '../composables/useTimeRange'
import MetricChart from '../components/MetricChart.vue'
import { formatMonitorDuration, parseNativeMonitorEvent } from '../nativeMonitor'
import { formatSlowQueryDuration, parseSlowQueryEvent } from '../slowQuery'
import { formatDate, formatDateTime } from '../timeDisplay'

const props = defineProps<{ server: string; bench: string; site: string }>()
const detail = ref<SiteDetail | null>(null)
const detailError = ref<string | null>(null)
const detailLoading = ref(false)
const logs = ref<{ ts: number; line: string; stream: Record<string, string> }[]>([])
const logsError = ref<string | null>(null)
const logsLoading = ref(false)
let detailRequest = 0
let logsRequest = 0
const { range, refreshSec } = useTimeRange()

async function loadDetail() {
  const request = ++detailRequest
  detailLoading.value = true; detailError.value = null
  try { const value = await fetchSite(props.server, props.bench, props.site); if (request === detailRequest) detail.value = value }
  catch (e) { if (request === detailRequest) detailError.value = e instanceof Error ? e.message : String(e) }
  finally { if (request === detailRequest) detailLoading.value = false }
}

async function loadLogs() {
  const request = ++logsRequest
  logsLoading.value = true; logsError.value = null
  try {
		const siteSelector = queryLabelMatchers({ server: props.server, bench: props.bench, site: props.site })
		const benchSelector = queryLabelMatchers({ server: props.server, bench: props.bench })
		const monitorSelector = queryLabelMatchers({ server: props.server, bench: props.bench, log_type: 'monitor' })
		const queries = [
			queryLogs(`{${siteSelector}} |~ "(?i)(error|exception|traceback|failed|fatal|critical)"`, range.value, 100),
			queryLogs(`{${benchSelector}} |= ${queryStringLiteral(props.site)} |~ "(?i)(error|exception|traceback|failed|fatal|critical)"`, range.value, 100),
			queryLogs(`{${monitorSelector}} | json | site=${queryStringLiteral(props.site)} | exception!=""`, range.value, 100),
		]
		const identityHashes = (detail.value?.database_identities ?? []).map((identity) => identity.hash).filter((hash) => /^[0-9a-f]{24}$/.test(hash))
		if (identityHashes.length) {
			const slowSelector = queryLabelMatchers({ server: props.server, log_type: 'slow-query-event' })
			queries.push(queryLogs(`{${slowSelector},identity_hash=~"${identityHashes.join('|')}"}`, range.value, 100))
		}
		const responses = await Promise.all(queries)
		const streams = responses.flatMap((response) => response.data?.result ?? [])
    const flattened = streams.flatMap((stream) =>
      (stream.values ?? []).map(([ns, line]) => ({ ts: Number(BigInt(ns) / 1_000_000n), line, stream: stream.stream })),
		).sort((a, b) => b.ts - a.ts)
		const deduplicated = flattened.filter((entry, index, all) =>
			index === all.findIndex((candidate) => candidate.ts === entry.ts && candidate.line === entry.line),
		).slice(0, 100)
		if (request === logsRequest) logs.value = deduplicated
  } catch (e) { if (request === logsRequest) logsError.value = e instanceof Error ? e.message : String(e) }
  finally { if (request === logsRequest) logsLoading.value = false }
}

watch(() => [props.server, props.bench, props.site], loadDetail)
watch([range, () => props.server, () => props.bench, () => props.site], loadLogs)
const { refresh: refreshDetail } = usePolling(loadDetail, refreshSec)
const { refresh: refreshLogs } = usePolling(loadLogs, refreshSec)

const labels = computed(() => queryLabelMatchers({ server: props.server, bench: props.bench, site: props.site }))
const databaseIdentityKey = computed(() => (detail.value?.database_identities ?? []).map((identity) => `${identity.kind}:${identity.hash}`).join(','))
watch(databaseIdentityKey, loadLogs)
const response = useMetricsRange(computed(() => `frappe_site_public_http_response_ms{${labels.value}} or frappe_site_http_response_ms{${labels.value}}`), range, refreshSec)
const status = useMetricsRange(computed(() => `frappe_site_public_http_status_code{${labels.value}} or frappe_site_http_status_code{${labels.value}}`), range, refreshSec)
const health = useMetricsRange(computed(() => `frappe_site_public_is_healthy{${labels.value}} or frappe_site_is_healthy{${labels.value}}`), range, refreshSec)
const nativeLabels = computed(() => detail.value?.native_monitor_enabled === 1 ? labels.value : '')
const requestRate = useMetricsRange(computed(() => nativeLabels.value
  ? `count_over_time(frappe_site_request_duration_seconds{${nativeLabels.value}}[5m]) / 5`
  : ''), range, refreshSec)
const requestP95 = useMetricsRange(computed(() => nativeLabels.value
  ? `1000 * quantile_over_time(0.95, frappe_site_request_duration_seconds{${nativeLabels.value}}[5m])`
  : ''), range, refreshSec)
const requestErrorRate = useMetricsRange(computed(() => nativeLabels.value
  ? `100 * sum(count_over_time(frappe_site_request_status{${nativeLabels.value},status_class=~"4xx|5xx"}[5m])) / clamp_min(count_over_time(frappe_site_request_duration_seconds{${nativeLabels.value}}[5m]), 1)`
  : ''), range, refreshSec)
const jobP95 = useMetricsRange(computed(() => nativeLabels.value
  ? `1000 * quantile_over_time(0.95, frappe_site_job_duration_seconds{${nativeLabels.value}}[5m])`
  : ''), range, refreshSec)
const busiestRoutes = useMetricsRange(computed(() => nativeLabels.value
  ? `topk(8, sum by (method, route) (count_over_time(frappe_site_request_route_duration_seconds{${nativeLabels.value}}[5m])) / 5)`
  : ''), range, refreshSec)
const routeP95 = useMetricsRange(computed(() => nativeLabels.value
  ? `topk(20, 1000 * quantile_over_time(0.95, frappe_site_request_route_duration_seconds{${nativeLabels.value}}[5m]))`
  : ''), range, refreshSec)
const databaseServerLabels = computed(() => queryLabelMatchers({ server: props.server }))
const slowQueryDuration = useMetricsRange(computed(() =>
	`1000 * (frappe_database_slow_query_duration_seconds{${databaseServerLabels.value}} * on(server, identity_hash, identity_kind) group_left(bench, site) frappe_site_database_identity{${labels.value}})`), range, refreshSec)
const slowQueryRows = useMetricsRange(computed(() =>
	`frappe_database_slow_query_rows_examined{${databaseServerLabels.value}} * on(server, identity_hash, identity_kind) group_left(bench, site) frappe_site_database_identity{${labels.value}}`), range, refreshSec)
const seriesLabel = (metric: Record<string, string>) => metric.__name__?.replace('frappe_site_', '') ?? props.site
const performanceLabel = () => props.site
const databaseSeriesLabel = (metric: Record<string, string>) => `${metric.engine || 'database'} ${metric.operation || 'query'}`
const topRoutes = computed(() => (busiestRoutes.result.value?.data?.result ?? []).map((series) => {
  const last = series.values[series.values.length - 1]
  const method = series.metric.method || '—'
  const route = series.metric.route || '/_web'
  const latencySeries = (routeP95.result.value?.data?.result ?? []).find((candidate) =>
		candidate.metric.method === method && candidate.metric.route === route)
  const latency = latencySeries?.values[latencySeries.values.length - 1]
  return { method, route, rpm: last ? Number(last[1]) : 0, p95: latency ? Number(latency[1]) : null }
}).filter((item) => Number.isFinite(item.rpm)).sort((a, b) => b.rpm - a.rpm).slice(0, 8))
const healthState = computed(() => !detail.value ? 'unknown' : detail.value.is_healthy === 1 ? 'healthy' : detail.value.is_healthy < 0 ? 'unknown' : 'unhealthy')
const externalURL = computed(() => /^https?:\/\//i.test(props.site) ? props.site : `https://${props.site}`)
const logExplorerLink = computed(() => `/logs?server=${encodeURIComponent(props.server)}&bench=${encodeURIComponent(props.bench)}&site=${encodeURIComponent(props.site)}`)
const slowQueryLogLink = computed(() => {
	const identities = (detail.value?.database_identities ?? []).map((identity) => identity.hash).filter((hash) => /^[0-9a-f]{24}$/.test(hash))
	const query = new URLSearchParams({ server: props.server, type: 'slow-query-event' })
	if (identities.length) query.set('identity', identities.join('|'))
	return `/logs?${query.toString()}`
})
const internalFailureReasons: Record<number, string> = { 0: 'No failure', 1: 'No response', 2: 'HTTP 4xx', 3: 'HTTP 5xx', 4: 'Probe skipped', 5: 'No web port', 6: 'Unexpected response' }
const publicFailureReasons: Record<number, string> = { 0: 'No failure', 1: 'DNS failed', 2: 'Private target blocked', 3: 'Connection failed', 4: 'TLS failed', 5: 'HTTP 4xx', 6: 'HTTP 5xx', 7: 'Unexpected status', 8: 'Invalid Frappe response', 9: 'Invalid target URL' }
const failureReason = computed(() => {
  if (!detail.value) return 'Unknown'
  const reasons = detail.value.public_probe_available ? publicFailureReasons : internalFailureReasons
  return reasons[detail.value.failure_reason_code] ?? 'Unknown'
})
const publicReason = computed(() => publicFailureReasons[detail.value?.public_failure_reason_code ?? -1] ?? 'Not observed')
const internalReason = computed(() => internalFailureReasons[detail.value?.internal_failure_reason_code ?? -1] ?? 'Unknown')
const tlsExpiry = computed(() => detail.value?.public_tls_cert_expiry_timestamp_seconds
  ? formatDate(detail.value.public_tls_cert_expiry_timestamp_seconds * 1000)
  : 'Not available')
function state(value: number) { return value === 1 ? 'healthy' : value < 0 ? 'unknown' : 'unhealthy' }
function fmtTime(ms: number) { return formatDateTime(ms) }
function displayLog(entry: { line: string; stream: Record<string, string> }) {
	if (entry.stream.log_type === 'monitor') {
		const event = parseNativeMonitorEvent(entry.line)
		if (event) return `${event.transactionType === 'request' ? `${event.method} ${event.path} · HTTP ${event.statusCode || '—'}` : `JOB ${event.method}`} · ${formatMonitorDuration(event.durationMs)}${event.exception ? ` · ${event.exception}` : ''}`
	}
	if (entry.stream.log_type === 'slow-query-event') {
		const event = parseSlowQueryEvent(entry.line)
		if (event) return `${event.engine.toUpperCase()} ${event.operation.toUpperCase()} · ${formatSlowQueryDuration(event.durationSeconds)} · ${event.rowsExamined.toLocaleString()} rows examined${event.traceID ? ` · trace ${event.traceID}` : ''}`
	}
	return entry.line
}
</script>

<template>
  <section class="page">
    <nav class="breadcrumbs"><RouterLink to="/sites">Sites</RouterLink><span>/</span><RouterLink :to="`/benches/${encodeURIComponent(props.server)}/${encodeURIComponent(props.bench)}`">{{ props.bench }}</RouterLink><span>/</span><span>{{ props.site }}</span></nav>
    <header class="page-heading">
      <div class="site-title"><span class="hero-icon" :class="healthState"><Globe2 :size="22" /></span><div><span class="eyebrow">Frappe site</span><h1>{{ props.site }}</h1><p><ServerIcon :size="13" /> {{ props.server }} <span>·</span><Workflow :size="13" /> {{ props.bench }}</p></div></div>
      <div class="page-actions"><span class="status-pill" :class="healthState">{{ healthState }}</span><a class="button" :href="externalURL" target="_blank" rel="noopener noreferrer"><ExternalLink :size="14" /> Open site</a><button class="button icon-button" aria-label="Refresh site" :disabled="detailLoading" @click="refreshDetail"><RefreshCw :size="15" :class="{ spin: detailLoading }" /></button></div>
    </header>
    <div v-if="detailError" class="notice error">Failed to load site: {{ detailError }}</div>

    <div v-if="detail" class="health-hero panel" :class="healthState">
      <div><span class="hero-label">{{ detail.public_probe_available ? 'Public edge probe' : 'Internal SSH probe' }}</span><strong>{{ healthState === 'healthy' ? 'Site is responding normally' : healthState === 'unknown' ? 'Health could not be determined' : 'Site health check failed' }}</strong><small>{{ failureReason }}</small></div>
      <div class="probe-code"><span>{{ detail.http_status_code > 0 ? `HTTP ${detail.http_status_code}` : 'No HTTP response' }}</span><strong>{{ detail.http_response_ms.toFixed(1) }} ms</strong></div>
    </div>

    <div v-if="detail" class="probe-grid">
      <article class="panel probe-panel" :class="state(detail.public_is_healthy)"><div class="probe-heading"><div><span class="hero-label">Public edge</span><strong>{{ detail.public_probe_available ? state(detail.public_is_healthy) : 'not configured' }}</strong></div><Globe2 :size="18" /></div><dl><div><dt>Status</dt><dd>{{ detail.public_probe_available && detail.public_http_status_code ? `HTTP ${detail.public_http_status_code}` : '—' }}</dd></div><div><dt>Latency</dt><dd>{{ detail.public_probe_available ? `${detail.public_http_response_ms.toFixed(1)} ms` : '—' }}</dd></div><div><dt>TLS expires</dt><dd>{{ tlsExpiry }}</dd></div><div><dt>Reason</dt><dd>{{ publicReason }}</dd></div></dl></article>
      <article class="panel probe-panel" :class="state(detail.internal_is_healthy)"><div class="probe-heading"><div><span class="hero-label">Inside the host</span><strong>{{ state(detail.internal_is_healthy) }}</strong></div><ServerIcon :size="18" /></div><dl><div><dt>Status</dt><dd>{{ detail.internal_http_status_code ? `HTTP ${detail.internal_http_status_code}` : '—' }}</dd></div><div><dt>Latency</dt><dd>{{ detail.internal_http_response_ms.toFixed(1) }} ms</dd></div><div><dt>Route</dt><dd>localhost + Host header</dd></div><div><dt>Reason</dt><dd>{{ internalReason }}</dd></div></dl></article>
    </div>

    <div v-if="detail" class="summary-grid site-metrics">
      <div class="panel summary-card"><Timer :size="18" /><div class="summary-label">Response time</div><div class="summary-value">{{ detail.http_response_ms.toFixed(1) }}<small> ms</small></div><div class="summary-note">Latest completed probe</div></div>
      <div class="panel summary-card"><Activity :size="18" /><div class="summary-label">One-hour average</div><div class="summary-value">{{ detail.avg_response_ms_1h.toFixed(1) }}<small> ms</small></div><div class="summary-note">Rolling response latency</div></div>
      <div class="panel summary-card"><Globe2 :size="18" /><div class="summary-label">HTTP status</div><div class="summary-value">{{ detail.http_status_code || '—' }}</div><div class="summary-note">{{ failureReason }}</div></div>
      <div class="panel summary-card"><FileWarning :size="18" /><div class="summary-label">Probe reason</div><div class="summary-value text-value">{{ failureReason }}</div><div class="summary-note">Code {{ detail.failure_reason_code }}</div></div>
    </div>

    <div v-if="detail" class="section-heading"><div><h2>Availability history</h2><p>Latency, HTTP status, and health across the selected window.</p></div></div>
    <div v-if="detail" class="charts">
      <MetricChart title="HTTP response time" :result="response.result.value" :loading="response.loading.value" :error="response.error.value" :series-label="seriesLabel" y-unit=" ms" />
      <MetricChart title="HTTP status code" :result="status.result.value" :loading="status.loading.value" :error="status.error.value" :series-label="seriesLabel" />
      <MetricChart title="Health state (1 healthy, 0 failed)" :result="health.result.value" :loading="health.loading.value" :error="health.error.value" :series-label="seriesLabel" />
    </div>

    <div v-if="detail" class="section-heading performance-heading"><div><h2>Application performance</h2><p>Low-overhead request and background-job telemetry reported by Frappe itself.</p></div><span class="status-pill" :class="detail.native_monitor_enabled === 1 ? 'healthy' : 'unknown'">{{ detail.native_monitor_enabled === 1 ? 'telemetry enabled' : 'setup required' }}</span></div>
    <div v-if="detail && detail.native_monitor_enabled !== 1" class="notice warning telemetry-notice"><FileWarning :size="18" /><div><strong>Native request telemetry is disabled for this site.</strong><p>Frappe Monitor remains read-only and will not change the target. Enable Frappe's <code>monitor</code> setting on the bench or site to collect API latency, request volume, jobs, and route-level failures.</p></div></div>
    <template v-else-if="detail">
      <div class="charts performance-charts">
        <MetricChart title="Requests per minute (5m rolling)" :result="requestRate.result.value" :loading="requestRate.loading.value" :error="requestRate.error.value" :series-label="performanceLabel" />
        <MetricChart title="Request p95 latency (5m rolling)" :result="requestP95.result.value" :loading="requestP95.loading.value" :error="requestP95.error.value" :series-label="performanceLabel" y-unit=" ms" />
        <MetricChart title="HTTP error rate (5m rolling)" :result="requestErrorRate.result.value" :loading="requestErrorRate.loading.value" :error="requestErrorRate.error.value" :series-label="performanceLabel" y-unit="%" />
        <MetricChart title="Background-job p95 duration" :result="jobP95.result.value" :loading="jobP95.loading.value" :error="jobP95.error.value" :series-label="performanceLabel" y-unit=" ms" />
      </div>
      <section class="panel route-panel">
        <div class="route-heading"><div><h3>Busiest endpoints</h3><p>Latest rolling request rate, capped at eight routes.</p></div><span>requests/min</span></div>
        <div v-if="busiestRoutes.error.value" class="empty-state compact"><FileWarning :size="20" /><p>{{ busiestRoutes.error.value }}</p></div>
        <div v-else-if="topRoutes.length" class="route-table-wrap"><table><thead><tr><th>Method</th><th>Route</th><th>p95</th><th>Rate</th></tr></thead><tbody><tr v-for="route in topRoutes" :key="`${route.method}:${route.route}`"><td><span class="method-badge">{{ route.method }}</span></td><td><code>{{ route.route }}</code></td><td>{{ route.p95 === null ? '—' : `${route.p95.toFixed(0)} ms` }}</td><td>{{ route.rpm.toFixed(2) }}</td></tr></tbody></table></div>
        <div v-else-if="!busiestRoutes.loading.value" class="empty-state compact"><Activity :size="20" /><p>No request events in this time window.</p></div>
      </section>
    </template>

    <div v-if="detail" class="section-heading database-heading"><div><h2>Database performance</h2><p>Slow-query events matched to this site using locally hashed database identities. SQL remains only in the searchable host log.</p></div><RouterLink v-if="detail.database_identities?.length" class="button" :to="slowQueryLogLink"><ExternalLink :size="14" /> Inspect slow queries</RouterLink></div>
    <div v-if="detail" class="charts">
      <MetricChart title="Slow query duration" :result="slowQueryDuration.result.value" :loading="slowQueryDuration.loading.value" :error="slowQueryDuration.error.value" :series-label="databaseSeriesLabel" y-unit=" ms" />
      <MetricChart title="Rows examined by slow queries" :result="slowQueryRows.result.value" :loading="slowQueryRows.loading.value" :error="slowQueryRows.error.value" :series-label="databaseSeriesLabel" />
    </div>

    <div class="section-heading logs-heading"><div><h2>Recent error signals</h2><p>Site-labelled log errors and native Frappe request/job exceptions for this site only.</p></div><div class="log-actions"><RouterLink class="button" :to="logExplorerLink"><ExternalLink :size="14" /> Search this site</RouterLink><button class="button" :disabled="logsLoading" @click="refreshLogs"><RefreshCw :size="14" :class="{ spin: logsLoading }" /> Reload</button></div></div>
    <section class="panel logs-panel">
      <div v-if="logsError" class="logs-unavailable"><FileWarning :size="24" /><div><strong>Log backend unavailable</strong><p>Metrics are still working. Loki returned: {{ logsError }}</p></div></div>
      <div v-else-if="logs.length" class="log-list"><div v-for="(entry, index) in logs" :key="`${entry.ts}-${index}`" class="log-line"><time>{{ fmtTime(entry.ts) }}</time><code>{{ displayLog(entry) }}</code></div></div>
      <div v-else-if="!logsLoading" class="empty-state"><FileWarning :size="25" /><h3>No recent error lines</h3><p>No site-scoped error or exception logs were returned for this time window.</p></div>
    </section>
  </section>
</template>

<style scoped>
.site-title { display: flex; align-items: center; gap: .8rem; }.site-title p { display: flex; align-items: center; gap: .3rem; flex-wrap: wrap; }.hero-icon { width: 48px; height: 48px; display: grid; place-items: center; border-radius: 13px; color: var(--status-unknown); background: color-mix(in srgb,var(--status-unknown) 10%,transparent); }.hero-icon.healthy{color:var(--status-reachable);background:color-mix(in srgb,var(--status-reachable) 10%,transparent)}.hero-icon.unhealthy{color:var(--status-unreachable);background:color-mix(in srgb,var(--status-unreachable) 10%,transparent)}
.health-hero { display: flex; justify-content: space-between; align-items: center; gap: 1rem; padding: 1rem 1.1rem; margin-bottom: .8rem; border-left: 3px solid var(--status-unknown); }.health-hero.healthy{border-left-color:var(--status-reachable)}.health-hero.unhealthy{border-left-color:var(--status-unreachable)}.health-hero>div:first-child{display:flex;flex-direction:column}.hero-label{color:var(--muted-2);font-size:.66rem;text-transform:uppercase;letter-spacing:.1em}.health-hero strong{margin-top:.25rem;color:var(--fg-strong)}.health-hero small{margin-top:.18rem;color:var(--muted)}.probe-code{display:flex;flex-direction:column;align-items:flex-end}.probe-code span{color:var(--muted);font:.72rem "SFMono-Regular",Consolas,monospace}.probe-code strong{font-size:1.3rem}
.probe-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:.8rem;margin-bottom:.8rem}.probe-panel{padding:1rem;border-top:2px solid var(--status-unknown)}.probe-panel.healthy{border-top-color:var(--status-reachable)}.probe-panel.unhealthy{border-top-color:var(--status-unreachable)}.probe-heading{display:flex;justify-content:space-between;gap:1rem}.probe-heading>div{display:flex;flex-direction:column}.probe-heading strong{text-transform:capitalize;color:var(--fg-strong);font-size:.92rem}.probe-heading svg{color:var(--muted-2)}.probe-panel dl{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:.65rem;margin:.8rem 0 0}.probe-panel dl div{min-width:0}.probe-panel dt{color:var(--muted-2);font-size:.65rem;text-transform:uppercase;letter-spacing:.05em}.probe-panel dd{margin:.15rem 0 0;color:var(--fg);font-size:.75rem;overflow-wrap:anywhere}.site-metrics{margin-bottom:.8rem}.site-metrics svg{float:right;color:var(--muted-2)}.summary-value small{color:var(--muted-2);font-size:.78rem}.summary-value.text-value{font-size:1.02rem;margin-top:.75rem}.charts{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:.8rem}.charts>*:last-child{grid-column:1/-1}.logs-heading{margin-top:1.8rem}.log-actions{display:flex;gap:.45rem}.logs-panel{overflow:hidden}.log-list{max-height:390px;overflow:auto}.log-line{display:grid;grid-template-columns:165px minmax(0,1fr);gap:.75rem;padding:.55rem .8rem;border-bottom:1px solid var(--card-border);font-size:.72rem}.log-line:last-child{border:0}.log-line time{color:var(--muted);white-space:nowrap}.log-line code{padding:0;background:none;color:var(--fg);white-space:pre-wrap}.logs-unavailable{display:flex;gap:.75rem;padding:1rem;color:var(--status-warning)}.logs-unavailable strong{color:var(--fg)}.logs-unavailable p{margin:.25rem 0 0;color:var(--muted);font-size:.75rem;overflow-wrap:anywhere}
.performance-heading,.database-heading{margin-top:1.8rem}.performance-charts>*:last-child{grid-column:auto}.telemetry-notice{display:flex;align-items:flex-start;gap:.65rem}.telemetry-notice p{margin:.2rem 0 0}.route-panel{margin-top:.8rem;overflow:hidden}.route-heading{display:flex;align-items:flex-start;justify-content:space-between;gap:1rem;padding:.9rem 1rem;border-bottom:1px solid var(--card-border)}.route-heading h3{margin:0;color:var(--fg-strong);font-size:.88rem}.route-heading p{margin:.2rem 0 0;color:var(--muted);font-size:.72rem}.route-heading>span{color:var(--muted-2);font-size:.65rem;text-transform:uppercase;letter-spacing:.06em}.route-table-wrap{overflow-x:auto}.route-panel table{width:100%;border-collapse:collapse}.route-panel th,.route-panel td{padding:.62rem 1rem;border-bottom:1px solid var(--card-border);text-align:left;font-size:.72rem}.route-panel th{color:var(--muted-2);font-size:.64rem;text-transform:uppercase;letter-spacing:.05em}.route-panel tr:last-child td{border:0}.route-panel th:last-child,.route-panel td:last-child{text-align:right;font-variant-numeric:tabular-nums}.route-panel code{background:none;padding:0;color:var(--fg);white-space:nowrap}.method-badge{display:inline-flex;padding:.15rem .35rem;border-radius:4px;background:var(--bg-hover);color:var(--muted);font:.64rem "SFMono-Regular",Consolas,monospace}.empty-state.compact{padding:1.2rem}.empty-state.compact p{margin:.3rem 0 0}
@media(max-width:900px){.charts,.probe-grid{grid-template-columns:1fr}.charts>*:last-child{grid-column:auto}}@media(max-width:720px){.site-title{align-items:flex-start}.hero-icon{width:42px;height:42px}.health-hero{align-items:flex-start;flex-direction:column}.probe-code{align-items:flex-start}.probe-panel dl{grid-template-columns:1fr}.log-line{grid-template-columns:1fr;gap:.2rem}.logs-heading,.performance-heading{align-items:stretch;flex-direction:column}.logs-heading .button{width:100%}.route-panel th,.route-panel td{padding:.55rem .7rem}.route-panel code{white-space:normal;overflow-wrap:anywhere}}
</style>
