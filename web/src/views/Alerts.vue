<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { AlertTriangle, Bell, BellOff, CheckCircle2, Clock3, ExternalLink, History, RefreshCw, ShieldAlert } from 'lucide-vue-next'
import { fetchAlerts, type AlertIncident, type AlertsResponse } from '../api'
import { usePolling } from '../composables/usePolling'
import { useTimeRange } from '../composables/useTimeRange'

const { refreshSec } = useTimeRange()
const data = ref<AlertsResponse | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)
const statusFilter = ref<'all' | 'firing' | 'resolved'>('all')

async function refresh() {
  loading.value = true
  error.value = null
  try { data.value = await fetchAlerts() }
  catch (e) { error.value = e instanceof Error ? e.message : String(e) }
  finally { loading.value = false }
}
const { refresh: refreshNow } = usePolling(refresh, refreshSec)

const enabled = computed(() => data.value?.enabled ?? false)
const firing = computed(() => data.value?.firing ?? [])
const rules = computed(() => data.value?.rules ?? [])
const incidents = computed(() => data.value?.incidents ?? [])
const filteredIncidents = computed(() => statusFilter.value === 'all' ? incidents.value : incidents.value.filter((item) => item.status === statusFilter.value))
const critical = computed(() => firing.value.filter((item) => severityFor(item.rule_name) === 'critical').length)
const resolvedToday = computed(() => incidents.value.filter((item) => item.resolved_at && Date.now() - new Date(item.resolved_at).getTime() < 86_400_000).length)

function severityFor(ruleName: string) { return rules.value.find((rule) => rule.name === ruleName)?.severity ?? 'warning' }
function relativeTime(iso?: string) {
  if (!iso) return '—'
  const age = Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 1000))
  if (age < 60) return `${age}s ago`
  if (age < 3600) return `${Math.floor(age / 60)}m ago`
  if (age < 86400) return `${Math.floor(age / 3600)}h ago`
  return `${Math.floor(age / 86400)}d ago`
}
function duration(item: AlertIncident) {
  const end = item.resolved_at ? new Date(item.resolved_at).getTime() : Date.now()
  const seconds = Math.max(0, Math.floor((end - new Date(item.started_at).getTime()) / 1000))
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`
  return `${Math.floor(seconds / 86400)}d ${Math.floor((seconds % 86400) / 3600)}h`
}
function target(labels: Record<string, string>) { return [labels.server, labels.bench, labels.site || labels.service].filter(Boolean).join(' / ') || 'Fleet' }
function humanRule(name: string) { return name.replace(/_/g, ' ') }
function logLink(labels: Record<string, string>) {
  const query = new URLSearchParams()
  if (labels.server) query.set('server', labels.server)
  if (labels.bench) query.set('bench', labels.bench)
  if (labels.site) query.set('q', labels.site)
	if (labels.identity_hash && /^[0-9a-f]{24}$/.test(labels.identity_hash)) {
		query.delete('q')
		query.set('type', 'slow-query-event')
		query.set('identity', labels.identity_hash)
	}
  return `/logs?${query.toString()}`
}
function targetLink(labels: Record<string, string>) {
	if (labels.server && labels.bench && labels.site) return `/sites/${encodeURIComponent(labels.server)}/${encodeURIComponent(labels.bench)}/${encodeURIComponent(labels.site)}`
	if (labels.server && labels.bench) return `/benches/${encodeURIComponent(labels.server)}/${encodeURIComponent(labels.bench)}`
	return '/servers'
}
</script>

<template>
  <section class="page incidents-page">
    <header class="page-heading">
      <div><span class="eyebrow">Detection & history</span><h1>Incidents</h1><p>Current failures and retained incident history from server, service, worker, queue, and site-health rules.</p></div>
      <div class="page-actions"><button class="button" :disabled="loading" @click="refreshNow"><RefreshCw :size="15" :class="{ spin: loading }" />{{ loading ? 'Refreshing' : 'Refresh' }}</button></div>
    </header>

    <div v-if="error" class="notice error"><AlertTriangle :size="18" />Failed to load incidents: {{ error }}</div>
    <div v-if="data && !enabled" class="notice warning"><BellOff :size="18" /><span><strong>Alert evaluation is disabled.</strong> Metrics remain visible, but Telegram notifications and new incident records require <code>alerts.enabled</code> in the monitor configuration.</span></div>

    <div class="summary-grid">
      <div class="panel summary-card"><ShieldAlert :size="18" /><div class="summary-label">Open incidents</div><div class="summary-value" :class="{ danger: firing.length }">{{ firing.length }}</div><div class="summary-note">Failing now</div></div>
      <div class="panel summary-card"><AlertTriangle :size="18" /><div class="summary-label">Critical</div><div class="summary-value" :class="{ danger: critical }">{{ critical }}</div><div class="summary-note">Immediate attention</div></div>
      <div class="panel summary-card"><CheckCircle2 :size="18" /><div class="summary-label">Resolved in 24h</div><div class="summary-value">{{ resolvedToday }}</div><div class="summary-note">Recovery recorded</div></div>
      <div class="panel summary-card"><Bell :size="18" /><div class="summary-label">Detection rules</div><div class="summary-value">{{ rules.length }}</div><div class="summary-note">{{ enabled ? 'Evaluation active' : 'Evaluation disabled' }}</div></div>
    </div>

    <div class="section-heading"><div><h2>Needs attention</h2><p>Live conditions that are currently violating a rule.</p></div></div>
    <div v-if="firing.length" class="firing-grid">
      <article v-for="item in firing" :key="item.id" class="panel incident-card" :class="severityFor(item.rule_name)">
        <div class="incident-head"><span class="status-pill" :class="severityFor(item.rule_name)">{{ severityFor(item.rule_name) }}</span><span class="age"><Clock3 :size="12" />{{ relativeTime(item.first_fired_at) }}</span></div>
        <h3>{{ humanRule(item.rule_name) }}</h3><p>{{ target(item.labels) }}</p>
        <div class="incident-meta"><span>Observed value <strong>{{ item.value }}</strong></span><span>Last Telegram attempt {{ relativeTime(item.last_notified_at) }}</span></div>
        <div class="incident-actions"><RouterLink :to="targetLink(item.labels)" class="diagnose-link">Open affected target <ExternalLink :size="13" /></RouterLink><RouterLink :to="logLink(item.labels)" class="diagnose-link">Investigate related logs <ExternalLink :size="13" /></RouterLink></div>
      </article>
    </div>
    <div v-else class="panel empty-state"><CheckCircle2 :size="29" /><h3>No active incidents</h3><p>{{ enabled ? 'Every evaluated rule is currently healthy.' : 'Enable alert evaluation to continuously detect and record failures.' }}</p></div>

    <div class="section-heading history-heading"><div><h2>Incident history</h2><p>Resolved episodes are retained instead of disappearing after recovery.</p></div><div class="segmented"><button :class="{ active: statusFilter === 'all' }" @click="statusFilter = 'all'">All</button><button :class="{ active: statusFilter === 'firing' }" @click="statusFilter = 'firing'">Open</button><button :class="{ active: statusFilter === 'resolved' }" @click="statusFilter = 'resolved'">Resolved</button></div></div>
    <section class="panel history-panel">
      <div v-if="filteredIncidents.length" class="data-table-wrap"><table class="data-table"><thead><tr><th>Status</th><th>Incident</th><th>Target</th><th>Started</th><th>Duration</th><th></th></tr></thead><tbody>
        <tr v-for="item in filteredIncidents" :key="item.id"><td><span class="status-pill" :class="item.status === 'firing' ? item.severity : 'healthy'">{{ item.status }}</span></td><td><strong class="rule-title">{{ humanRule(item.rule_name) }}</strong><small>{{ item.severity }}</small></td><td>{{ target(item.labels) }}</td><td>{{ relativeTime(item.started_at) }}</td><td>{{ duration(item) }}</td><td><RouterLink :to="logLink(item.labels)" class="table-link">Logs <ExternalLink :size="12" /></RouterLink></td></tr>
      </tbody></table></div>
      <div v-else class="empty-state"><History :size="26" /><h3>No recorded incidents</h3><p>New alert episodes will appear here and remain available after they recover.</p></div>
    </section>

    <details class="panel rules-panel"><summary><span><Bell :size="15" />Detection rules</span><span>{{ rules.length }} configured</span></summary><div class="data-table-wrap"><table class="data-table"><thead><tr><th>Severity</th><th>Name</th><th>PromQL expression</th></tr></thead><tbody><tr v-for="rule in rules" :key="rule.name"><td><span class="status-pill" :class="rule.severity">{{ rule.severity }}</span></td><td>{{ rule.name }}</td><td><code>{{ rule.expr }}</code></td></tr></tbody></table></div></details>
  </section>
</template>

<style scoped>
.summary-card svg{float:right;color:var(--muted-2)}.danger{color:var(--status-unreachable)!important}.firing-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:.8rem}.incident-card{padding:1rem;border-left:3px solid var(--status-warning)}.incident-card.critical{border-left-color:var(--status-unreachable)}.incident-head{display:flex;align-items:center;justify-content:space-between}.age{display:flex;align-items:center;gap:.3rem;color:var(--muted);font-size:.7rem}.incident-card h3{margin:.85rem 0 .25rem;color:var(--fg-strong);font-size:1rem;text-transform:capitalize}.incident-card>p{margin:0;color:var(--muted);font-size:.78rem}.incident-meta{display:flex;gap:1rem;flex-wrap:wrap;margin:.8rem 0;color:var(--muted-2);font-size:.69rem}.incident-meta strong{color:var(--fg)}.incident-actions{display:flex;gap:.8rem;flex-wrap:wrap}.diagnose-link{display:inline-flex;align-items:center;gap:.35rem;color:var(--accent-strong);font-size:.74rem;font-weight:650}.history-heading{margin-top:2rem}.segmented{display:flex;padding:3px;border:1px solid var(--card-border);border-radius:8px;background:var(--card-bg)}.segmented button{padding:.32rem .62rem;border:0;border-radius:5px;background:transparent;color:var(--muted);cursor:pointer;font-size:.7rem}.segmented button.active{background:var(--bg-hover);color:var(--fg)}.history-panel{overflow:hidden}.rule-title{display:block;color:var(--fg);text-transform:capitalize}.history-panel td small{display:block;margin-top:.16rem;color:var(--muted-2);font-size:.64rem}.rules-panel{margin-top:1rem;overflow:hidden}.rules-panel summary{display:flex;align-items:center;justify-content:space-between;padding:.9rem 1rem;cursor:pointer;color:var(--muted);font-size:.78rem}.rules-panel summary span:first-child{display:flex;align-items:center;gap:.45rem;color:var(--fg);font-weight:650}.rules-panel[open] summary{border-bottom:1px solid var(--card-border)}
@media(max-width:850px){.firing-grid{grid-template-columns:1fr}}@media(max-width:720px){.history-heading{align-items:flex-start;flex-direction:column}.segmented{width:100%}.segmented button{flex:1}.firing-grid{gap:.55rem}}
</style>
