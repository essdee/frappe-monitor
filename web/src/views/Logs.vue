<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { AlertTriangle, Download, FileSearch, Pause, Play, RefreshCw, Search, SlidersHorizontal } from 'lucide-vue-next'
import { fetchBenches, fetchServers, queryLabelMatchers, queryLogs, queryStringLiteral, type BenchPair, type Server } from '../api'
import { usePolling } from '../composables/usePolling'
import { useTimeRange } from '../composables/useTimeRange'
import { formatMonitorBytes as formatBytes, formatMonitorDuration as formatDuration, parseNativeMonitorEvent, type NativeMonitorEvent } from '../nativeMonitor'
import { formatSlowQueryDuration, parseSlowQueryEvent, type SlowQueryEvent } from '../slowQuery'
import { formatLogTimestamp } from '../timeDisplay'

interface LogRow { timestampNs: string; timestampMs: number; line: string; labels: Record<string, string>; native: NativeMonitorEvent | null; slowQuery: SlowQueryEvent | null }
const route = useRoute()
const router = useRouter()
const { range, refreshSec } = useTimeRange()
const servers = ref<Server[]>([])
const benches = ref<BenchPair[]>([])
const rows = ref<LogRow[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const search = ref(typeof route.query.q === 'string' ? route.query.q : '')
const server = ref(typeof route.query.server === 'string' ? route.query.server : '')
const bench = ref(typeof route.query.bench === 'string' ? route.query.bench : '')
const site = ref(typeof route.query.site === 'string' ? route.query.site : '')
const logType = ref(typeof route.query.type === 'string' ? route.query.type : '')
const identity = ref(typeof route.query.identity === 'string' ? route.query.identity : '')
const limit = ref(250)
const paused = ref(false)
let requestID = 0

const availableBenches = computed(() => benches.value.filter((item) => !server.value || item.server === server.value))
watch(server, () => { if (bench.value && !availableBenches.value.some((item) => item.bench === bench.value)) bench.value = '' })

const logQL = computed(() => {
  const labels: Record<string, string> = {}
  if (server.value) labels.server = server.value
  if (bench.value) labels.bench = bench.value
  if (site.value) labels.site = site.value
  if (logType.value) labels.log_type = logType.value
  const identityHashes = identity.value.split('|').filter((hash) => /^[0-9a-f]{24}$/.test(hash))
  const baseSelector = Object.keys(labels).length ? queryLabelMatchers(labels) : 'server=~".+"'
  const selector = identityHashes.length ? `${baseSelector},identity_hash=~"${identityHashes.join('|')}"` : baseSelector
  const contains = search.value.trim() ? ` |= ${queryStringLiteral(search.value.trim())}` : ''
  return `{${selector}}${contains}`
})

async function refresh() {
  const request = ++requestID
  loading.value = true; error.value = null
  try {
    const response = await queryLogs(logQL.value, range.value, limit.value)
    const next = (response.data?.result ?? []).flatMap((stream) =>
      (stream.values ?? []).map(([timestampNs, line]) => ({
        timestampNs,
        timestampMs: Number(BigInt(timestampNs) / 1_000_000n),
        line,
        labels: stream.stream,
			native: stream.stream.log_type === 'monitor' ? parseNativeMonitorEvent(line) : null,
			slowQuery: stream.stream.log_type === 'slow-query-event' ? parseSlowQueryEvent(line) : null,
      })),
    ).sort((a, b) => a.timestampNs === b.timestampNs ? 0 : a.timestampNs > b.timestampNs ? -1 : 1)
    if (request === requestID) rows.value = next
  } catch (e) { if (request === requestID) error.value = e instanceof Error ? e.message : String(e) }
  finally { if (request === requestID) loading.value = false }
}

Promise.allSettled([fetchServers(), fetchBenches()]).then(([serverRows, benchRows]) => {
  if (serverRows.status === 'fulfilled') servers.value = serverRows.value
  if (benchRows.status === 'fulfilled') benches.value = benchRows.value
})

const pollingInterval = computed(() => paused.value ? 0 : refreshSec.value)
const { refresh: refreshNow } = usePolling(refresh, pollingInterval)
let filterTimer: ReturnType<typeof setTimeout> | null = null
watch([search, server, bench, site, logType, identity, limit, range], () => {
  const query = { ...route.query }
  for (const [key, value] of [['q', search.value], ['server', server.value], ['bench', bench.value], ['site', site.value], ['type', logType.value], ['identity', identity.value]]) {
    if (value) query[key] = value; else delete query[key]
  }
  void router.replace({ query })
  if (filterTimer) clearTimeout(filterTimer)
  filterTimer = setTimeout(refreshNow, 250)
})

function formatTimestamp(ms: number) {
	return formatLogTimestamp(ms)
}
function inferredLevel(row: LogRow) {
  if (row.native?.exception || (row.native?.statusCode ?? 0) >= 500) return 'error'
	if (row.slowQuery) return 'warning'
	if (/\b(fatal|panic|critical)\b/i.test(row.line)) return 'critical'
	if (/\b(error|exception|traceback|failed)\b/i.test(row.line)) return 'error'
	if (/\bwarn(ing)?\b/i.test(row.line)) return 'warning'
	if (/\bdebug\b/i.test(row.line)) return 'debug'
  return 'info'
}
function inspectTrace(uuid: string) {
	search.value = uuid
	// Database slow-query logs are host-level until their trace/database can
	// be correlated, so a trace investigation must not retain bench/site/type.
	bench.value = ''
	site.value = ''
	logType.value = ''
	identity.value = ''
}
function clearIdentity() { identity.value = '' }
function downloadLogs() {
  const body = rows.value.map((row) => `${new Date(row.timestampMs).toISOString()} ${JSON.stringify(row.labels)} ${row.line}`).join('\n')
  const url = URL.createObjectURL(new Blob([body], { type: 'text/plain' }))
  const anchor = document.createElement('a'); anchor.href = url; anchor.download = `frappe-monitor-logs-${Date.now()}.txt`; anchor.click(); URL.revokeObjectURL(url)
}
</script>

<template>
  <section class="page logs-page">
    <header class="page-heading"><div><span class="eyebrow">Diagnostics</span><h1>Log explorer</h1><p>Search Frappe bench and site logs collected over read-only SSH, without leaving the monitoring console.</p></div><div class="page-actions"><button class="button" @click="paused = !paused"><component :is="paused ? Play : Pause" :size="14" />{{ paused ? 'Resume live' : 'Pause live' }}</button><button class="button" :disabled="loading" @click="refreshNow"><RefreshCw :size="15" :class="{spin:loading}" /> Refresh</button><button class="button" :disabled="!rows.length" @click="downloadLogs"><Download :size="14" /> Export</button></div></header>

    <div class="panel query-panel">
      <label class="search"><Search :size="17" /><input v-model="search" type="search" placeholder="Search log message text…" /></label>
      <div class="filters">
        <label><SlidersHorizontal :size="14" /><select v-model="server"><option value="">All servers</option><option v-for="item in servers" :key="item.id" :value="item.name">{{ item.name }}</option></select></label>
        <label><select v-model="bench"><option value="">All benches</option><option v-for="item in availableBenches" :key="`${item.server}/${item.bench}`" :value="item.bench">{{ item.bench }}</option></select></label>
        <label><input v-model.trim="site" placeholder="Site label" /></label>
        <label><input v-model.trim="logType" list="log-types" placeholder="All log types" /><datalist id="log-types"><option v-for="type in ['monitor','error','access','frappe','worker','scheduler','socketio','database','slow-query','slow-query-event','web','bench','backup']" :key="type" :value="type" /></datalist></label>
        <label><select v-model.number="limit"><option :value="100">100 lines</option><option :value="250">250 lines</option><option :value="500">500 lines</option><option :value="1000">1,000 lines</option></select></label>
      </div>
      <div v-if="identity" class="correlation-filter"><span>Site-correlated database events</span><button type="button" @click="clearIdentity">Clear</button></div>
      <div class="query-meta"><code>{{ logQL }}</code><span>{{ rows.length }} lines{{ paused ? ' · live updates paused' : '' }}</span></div>
    </div>

    <div v-if="error" class="notice error"><AlertTriangle :size="18" /><span>Logs are unavailable: {{ error }}</span></div>
    <section v-if="rows.length" class="panel log-results" aria-live="polite">
      <div v-for="row in rows" :key="`${row.timestampNs}-${row.labels.log_type ?? ''}-${row.line}`" class="log-row">
        <time>{{ formatTimestamp(row.timestampMs) }}</time>
        <span class="level" :class="inferredLevel(row)">{{ inferredLevel(row) }}</span>
        <div class="labels"><span v-if="row.labels.server">{{ row.labels.server }}</span><span v-if="row.labels.bench">{{ row.labels.bench }}</span><span v-if="row.native?.site || row.labels.site">{{ row.native?.site || row.labels.site }}</span><span v-if="row.labels.log_type">{{ row.labels.log_type }}</span></div>
        <div v-if="row.native" class="native-event">
          <div class="native-summary"><span class="method">{{ row.native.transactionType === 'request' ? row.native.method : row.native.scheduled ? 'SCHEDULED JOB' : 'JOB' }}</span><strong>{{ row.native.transactionType === 'request' ? row.native.path : row.native.method }}</strong><span v-if="row.native.transactionType === 'request'" class="http-status" :class="{ failed: row.native.statusCode >= 400 }">HTTP {{ row.native.statusCode || '—' }}</span><span>{{ formatDuration(row.native.durationMs) }}</span></div>
          <div class="native-meta"><span v-if="row.native.transactionType === 'request'">{{ formatBytes(row.native.responseBytes) }} response</span><span v-else>{{ formatDuration(row.native.waitMs) }} queue wait</span><button v-if="row.native.uuid" type="button" class="trace-button" title="Search every log on this server for the Frappe trace ID" @click="inspectTrace(row.native.uuid)">Trace {{ row.native.uuid }}</button></div>
          <p v-if="row.native.exception" class="native-exception">{{ row.native.exception }}</p>
          <details><summary>Raw event</summary><pre>{{ row.native.formatted }}</pre></details>
        </div>
        <div v-else-if="row.slowQuery" class="native-event slow-query-event">
          <div class="native-summary"><span class="method">{{ row.slowQuery.engine.toUpperCase() }}</span><strong>{{ row.slowQuery.operation.toUpperCase() }} query</strong><span>{{ formatSlowQueryDuration(row.slowQuery.durationSeconds) }}</span></div>
          <div class="native-meta"><span>{{ row.slowQuery.rowsExamined.toLocaleString() }} rows examined</span><span>{{ row.slowQuery.rowsSent.toLocaleString() }} rows returned</span><span>{{ formatSlowQueryDuration(row.slowQuery.lockSeconds) }} lock time</span><button v-if="row.slowQuery.traceID" type="button" class="trace-button" title="Search every log on this server for the Frappe trace ID" @click="inspectTrace(row.slowQuery.traceID)">Trace {{ row.slowQuery.traceID }}</button></div>
        </div>
        <pre v-else>{{ row.line }}</pre>
      </div>
    </section>
    <div v-else-if="!loading && !error" class="panel empty-state"><FileSearch :size="28" /><h3>No log lines in this window</h3><p>Broaden the time window or remove filters. If this is a new target, ensure log streaming is enabled and Loki is reachable.</p></div>
  </section>
</template>

<style scoped>
.query-panel{padding:.8rem;margin-bottom:1rem}.search{min-height:44px;display:flex;align-items:center;gap:.55rem;padding:0 .75rem;border:1px solid var(--card-border);border-radius:9px;background:var(--bg);color:var(--muted)}.search:focus-within{border-color:var(--accent)}.search input{width:100%;border:0;outline:0;background:transparent}.filters{display:flex;gap:.45rem;flex-wrap:wrap;margin-top:.6rem}.filters label{min-height:37px;display:flex;align-items:center;gap:.3rem;padding:0 .5rem;border:1px solid var(--card-border);border-radius:8px;background:var(--bg);color:var(--muted)}.filters select,.filters input{min-height:35px;min-width:115px;border:0;outline:0;background:transparent;font-size:.75rem}.correlation-filter{display:flex;align-items:center;gap:.5rem;margin-top:.6rem;color:var(--accent-strong);font-size:.68rem}.correlation-filter button{border:0;background:none;color:var(--muted);font:inherit;cursor:pointer;text-decoration:underline}.query-meta{display:flex;justify-content:space-between;gap:1rem;align-items:center;margin-top:.65rem;color:var(--muted-2);font-size:.68rem}.query-meta code{max-width:75%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.log-results{overflow:hidden}.log-row{display:grid;grid-template-columns:145px 64px minmax(160px,.45fr) minmax(300px,1.6fr);gap:.6rem;align-items:start;padding:.55rem .75rem;border-bottom:1px solid var(--card-border);font-size:.72rem}.log-row:last-child{border:0}.log-row:hover{background:color-mix(in srgb,var(--bg-hover) 60%,transparent)}.log-row time{color:var(--muted);white-space:nowrap;font-variant-numeric:tabular-nums}.level{font-size:.62rem;font-weight:750;text-transform:uppercase}.level.error,.level.critical{color:var(--status-unreachable)}.level.warning{color:var(--status-warning)}.level.info{color:var(--accent-strong)}.level.debug{color:var(--muted)}.labels{display:flex;flex-wrap:wrap;gap:.25rem}.labels span{padding:.12rem .3rem;border-radius:4px;background:var(--bg-hover);color:var(--muted);font-size:.62rem}.log-row pre{margin:0;white-space:pre-wrap;overflow-wrap:anywhere;color:var(--fg);font: .7rem/1.5 "SFMono-Regular",Consolas,monospace}.native-event{min-width:0}.native-summary{display:flex;align-items:center;gap:.5rem;flex-wrap:wrap}.native-summary strong{min-width:140px;flex:1;color:var(--fg-strong);overflow-wrap:anywhere}.native-summary>span:last-child{color:var(--accent-strong);font-variant-numeric:tabular-nums}.method,.http-status{padding:.13rem .32rem;border-radius:4px;background:var(--bg-hover);color:var(--muted);font:.62rem "SFMono-Regular",Consolas,monospace}.http-status.failed{color:var(--status-unreachable)}.native-meta{display:flex;align-items:center;gap:.7rem;flex-wrap:wrap;margin-top:.3rem;color:var(--muted-2);font-size:.65rem}.trace-button{padding:0;border:0;background:none;color:var(--accent-strong);font:inherit;cursor:pointer}.trace-button:hover{text-decoration:underline}.native-exception{margin:.4rem 0 0;color:var(--status-unreachable);overflow-wrap:anywhere}.native-event details{margin-top:.35rem}.native-event summary{cursor:pointer;color:var(--muted-2);font-size:.65rem}.native-event details pre{margin-top:.4rem;padding:.55rem;border-radius:6px;background:var(--bg);max-height:280px;overflow:auto}
@media(max-width:1050px){.log-row{grid-template-columns:135px 60px 1fr}.log-row>pre,.native-event{grid-column:1/-1;padding-left:0}}@media(max-width:720px){.filters{display:grid;grid-template-columns:repeat(2,minmax(0,1fr))}.filters label,.filters select,.filters input{width:100%;min-width:0}.query-meta{align-items:flex-start;flex-direction:column}.query-meta code{max-width:100%}.log-row{grid-template-columns:1fr auto}.log-row time{grid-column:1}.labels{grid-column:1/-1}.log-row>pre,.native-event{grid-column:1/-1}.native-summary strong{min-width:100%;order:4}.native-meta{display:grid;gap:.2rem}.page-actions .button{flex:1}}
</style>
