<script setup lang="ts">
import { computed, onMounted, onScopeDispose, ref, watch } from 'vue'
import {
  fetchSite,
  queryLogs,
  type SiteDetail,
} from '../api'
import { useTimeRange } from '../composables/useTimeRange'
import { useMetricsRange } from '../composables/useMetricsRange'
import MetricChart from '../components/MetricChart.vue'

const props = defineProps<{ server: string; bench: string; site: string }>()

const detail = ref<SiteDetail | null>(null)
const detailError = ref<string | null>(null)
const detailLoading = ref(false)
const { range, refreshSec } = useTimeRange()

async function loadDetail() {
  detailLoading.value = true
  detailError.value = null
  try {
    detail.value = await fetchSite(props.server, props.bench, props.site)
  } catch (e) {
    detailError.value = e instanceof Error ? e.message : String(e)
  } finally {
    detailLoading.value = false
  }
}
onMounted(loadDetail)
watch(() => [props.server, props.bench, props.site], loadDetail)

// Charts: response time and HTTP status code over the selected range.
const labels = computed(
  () => `server="${props.server}",bench="${props.bench}",site="${props.site}"`,
)
const responseQuery = computed(
  () => `frappe_site_http_response_ms{${labels.value}}`,
)
const statusQuery = computed(
  () => `frappe_site_http_status_code{${labels.value}}`,
)
const healthQuery = computed(
  () => `frappe_site_is_healthy{${labels.value}}`,
)

const response = useMetricsRange(responseQuery, range, refreshSec)
const status = useMetricsRange(statusQuery, range, refreshSec)
const health = useMetricsRange(healthQuery, range, refreshSec)

const oneSeriesLabel = (m: Record<string, string>) =>
  m.__name__ ?? m.site ?? 'series'

// Bench-scoped logs (Loki has no site label today; the closest thing is
// the bench's error log). Phase 7 will add site-scoped tailing.
const logEntries = ref<{ ts: number; line: string; stream: Record<string, string> }[]>([])
const logsError = ref<string | null>(null)
const logsLoading = ref(false)

async function loadLogs() {
  logsLoading.value = true
  logsError.value = null
  try {
    const q = `{server="${props.server}",bench="${props.bench}",log_type="error"}`
    const resp = await queryLogs(q, range.value, 100)
    const flat: typeof logEntries.value = []
    for (const stream of resp.data?.result ?? []) {
      for (const [ns, line] of stream.values ?? []) {
        flat.push({ ts: Number(ns) / 1_000_000, line, stream: stream.stream })
      }
    }
    flat.sort((a, b) => b.ts - a.ts) // newest first
    logEntries.value = flat
  } catch (e) {
    logsError.value = e instanceof Error ? e.message : String(e)
  } finally {
    logsLoading.value = false
  }
}

let logTimer: ReturnType<typeof setInterval> | null = null
function armLogs(secs: number) {
  if (logTimer) {
    clearInterval(logTimer)
    logTimer = null
  }
  if (secs > 0) logTimer = setInterval(loadLogs, secs * 1000)
}
watch(refreshSec, (s) => armLogs(s), { immediate: true })
watch([range, () => props.server, () => props.bench], loadLogs)
onMounted(loadLogs)
onScopeDispose(() => {
  if (logTimer) clearInterval(logTimer)
})

function fmtTime(ms: number): string {
  return new Date(ms).toLocaleString()
}

function statusColor(d: SiteDetail): string {
  if (d.is_healthy === 1) return 'var(--status-reachable)'
  if (d.http_status_code === 0) return 'var(--status-unknown)'
  return 'var(--status-unreachable)'
}
</script>

<template>
  <section>
    <header class="page-header">
      <div>
        <h2>{{ props.site }}</h2>
        <p class="hostname">
          on <code>{{ props.server }}</code> · bench <code>{{ props.bench }}</code>
        </p>
      </div>
      <div v-if="detail" class="status-block" :style="{ borderColor: statusColor(detail) }">
        <span class="status-text">
          {{ detail.is_healthy === 1 ? 'healthy' : 'unhealthy' }}
        </span>
        <span class="status-code">HTTP {{ detail.http_status_code }}</span>
      </div>
    </header>

    <p v-if="detailError" class="error">Failed to load site: {{ detailError }}</p>

    <div v-if="detail" class="summary">
      <div class="summary-item">
        <div class="label">Response (ms)</div>
        <div class="value">{{ detail.http_response_ms.toFixed(1) }}</div>
      </div>
      <div class="summary-item">
        <div class="label">Avg 1h (ms)</div>
        <div class="value">{{ detail.avg_response_ms_1h.toFixed(1) }}</div>
      </div>
      <div class="summary-item">
        <div class="label">HTTP</div>
        <div class="value">{{ detail.http_status_code }}</div>
      </div>
      <div class="summary-item">
        <div class="label">Healthy</div>
        <div class="value">{{ detail.is_healthy === 1 ? 'yes' : 'no' }}</div>
      </div>
    </div>

    <div class="charts" v-if="detail">
      <MetricChart
        title="HTTP response time (ms)"
        :result="response.result.value"
        :loading="response.loading.value"
        :error="response.error.value"
        :series-label="oneSeriesLabel"
        y-unit=" ms"
      />
      <MetricChart
        title="HTTP status code"
        :result="status.result.value"
        :loading="status.loading.value"
        :error="status.error.value"
        :series-label="oneSeriesLabel"
      />
      <MetricChart
        title="Healthy (1 = healthy, 0 = not)"
        :result="health.result.value"
        :loading="health.loading.value"
        :error="health.error.value"
        :series-label="oneSeriesLabel"
      />
    </div>

    <section class="logs">
      <header class="logs-header">
        <h3>Recent error log lines</h3>
        <span class="logs-sub">
          (bench-scoped — Loki has no per-site label yet)
          <button class="reload" :disabled="logsLoading" @click="loadLogs">
            {{ logsLoading ? '…' : 'reload' }}
          </button>
        </span>
      </header>
      <p v-if="logsError" class="error">Failed to load logs: {{ logsError }}</p>
      <p v-else-if="!logsLoading && logEntries.length === 0" class="muted">
        No error log lines in this window.
      </p>
      <ol v-else class="log-list">
        <li v-for="(e, i) in logEntries" :key="i" class="log-line">
          <time>{{ fmtTime(e.ts) }}</time>
          <span class="log-msg">{{ e.line }}</span>
        </li>
      </ol>
    </section>
  </section>
</template>

<style scoped>
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 1rem;
  gap: 1rem;
}
.hostname {
  color: var(--muted);
  font-size: 0.9rem;
  margin: 0;
}
.hostname code {
  font-family: ui-monospace, "SF Mono", Menlo, monospace;
}
.status-block {
  border: 1px solid var(--card-border);
  background: var(--card-bg);
  padding: 0.4rem 0.75rem;
  border-radius: 4px;
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 0.15rem;
}
.status-text {
  text-transform: capitalize;
  font-weight: 600;
  font-size: 0.85rem;
}
.status-code {
  font-family: ui-monospace, "SF Mono", Menlo, monospace;
  font-size: 0.75rem;
  color: var(--muted);
}
.summary {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 0.75rem;
  margin-bottom: 1rem;
}
.summary-item {
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  border-radius: 6px;
  padding: 0.75rem 1rem;
}
.label {
  font-size: 0.75rem;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.value {
  font-size: 1.1rem;
  font-weight: 600;
  margin-top: 0.25rem;
}
.charts {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 1rem;
  margin-bottom: 1.5rem;
}
@media (max-width: 900px) {
  .charts {
    grid-template-columns: 1fr;
  }
}
.logs-header {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  margin: 0.5rem 0 0.5rem 0;
}
.logs-header h3 {
  font-size: 0.95rem;
  margin: 0;
}
.logs-sub {
  color: var(--muted);
  font-size: 0.8rem;
}
.reload {
  margin-left: 0.5rem;
  background: transparent;
  border: 1px solid var(--card-border);
  color: var(--muted);
  border-radius: 3px;
  padding: 0.1rem 0.4rem;
  font-size: 0.75rem;
  cursor: pointer;
}
.reload:hover:not(:disabled) {
  border-color: var(--accent);
  color: var(--fg);
}
.log-list {
  list-style: none;
  padding: 0;
  margin: 0;
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  border-radius: 6px;
  max-height: 360px;
  overflow-y: auto;
}
.log-line {
  display: grid;
  grid-template-columns: 160px 1fr;
  gap: 0.75rem;
  padding: 0.4rem 0.75rem;
  border-bottom: 1px solid var(--card-border);
  font-family: ui-monospace, "SF Mono", Menlo, monospace;
  font-size: 0.75rem;
  word-break: break-word;
}
.log-line:last-child {
  border-bottom: none;
}
.log-line time {
  color: var(--muted);
  white-space: nowrap;
}
.log-msg {
  white-space: pre-wrap;
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

@media (max-width: 720px) {
  .page-header {
    flex-direction: column;
    align-items: flex-start;
    gap: 0.5rem;
  }
  .page-header h2 {
    font-size: 1.15rem;
    word-break: break-word;
  }
  .hostname { font-size: 0.82rem; word-break: break-word; }
  .hostname code { word-break: break-all; }
  .status-block {
    align-self: flex-start;
    flex-direction: row;
    align-items: baseline;
    gap: 0.5rem;
  }
  .summary {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 0.5rem;
  }
  .summary-item { padding: 0.6rem 0.75rem; }
  .value { font-size: 1rem; }
  .logs-header {
    flex-direction: column;
    align-items: flex-start;
    gap: 0.25rem;
  }
  /* Time + line stack vertically — no more 160px-fixed first column
     squishing the message into a 30%-wide ribbon on phones. */
  .log-line {
    grid-template-columns: 1fr;
    gap: 0.15rem;
  }
  .log-line time { font-size: 0.7rem; }
  .log-msg { font-size: 0.78rem; }
  .log-list { max-height: 280px; }
}
</style>
