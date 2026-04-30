<script setup lang="ts">
import { computed, onMounted, onScopeDispose, ref, watch } from 'vue'
import { fetchBench, type BenchDetail } from '../api'
import { useTimeRange } from '../composables/useTimeRange'
import { useMetricsRange } from '../composables/useMetricsRange'
import MetricChart from '../components/MetricChart.vue'

const props = defineProps<{ server: string; bench: string }>()

const detail = ref<BenchDetail | null>(null)
const detailError = ref<string | null>(null)
const detailLoading = ref(false)
const { range, refreshSec } = useTimeRange()

async function loadDetail() {
  detailLoading.value = true
  detailError.value = null
  try {
    detail.value = await fetchBench(props.server, props.bench)
  } catch (e) {
    detailError.value = e instanceof Error ? e.message : String(e)
  } finally {
    detailLoading.value = false
  }
}

onMounted(loadDetail)
watch(() => [props.server, props.bench], loadDetail)

let timer: ReturnType<typeof setInterval> | null = null
function arm(secs: number) {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
  if (secs > 0) timer = setInterval(loadDetail, secs * 1000)
}
watch(refreshSec, (s) => arm(s), { immediate: true })
onScopeDispose(() => {
  if (timer) clearInterval(timer)
})

const labels = computed(() => `server="${props.server}",bench="${props.bench}"`)

const supervisorQuery = computed(
  () => `frappe_bench_supervisor_running{${labels.value}} or frappe_bench_supervisor_total{${labels.value}}`,
)
const queueQuery = computed(() => `frappe_bench_redis_queue_depth{${labels.value}}`)
const appsQuery = computed(() => `frappe_bench_apps_count{${labels.value}}`)

const supervisor = useMetricsRange(supervisorQuery, range, refreshSec)
const queues = useMetricsRange(queueQuery, range, refreshSec)
const apps = useMetricsRange(appsQuery, range, refreshSec)

const supervisorLabel = (m: Record<string, string>) => m.__name__ ?? 'supervisor'
const queueLabel = (m: Record<string, string>) => m.queue ?? 'queue'
const appsLabel = (_m: Record<string, string>) => 'apps'

const queueRows = computed<{ name: string; depth: number }[]>(() => {
  const q = detail.value?.redis_queues ?? {}
  return Object.entries(q)
    .map(([name, depth]) => ({ name, depth }))
    .sort((a, b) => a.name.localeCompare(b.name))
})

function supervisorHealth(d: BenchDetail): { label: string; color: string } {
  if (d.supervisor_total === 0) {
    return { label: '—', color: 'var(--muted)' }
  }
  const ok = d.supervisor_running >= d.supervisor_total
  return {
    label: `${d.supervisor_running} / ${d.supervisor_total}`,
    color: ok ? 'var(--status-reachable)' : 'var(--status-unreachable)',
  }
}
</script>

<template>
  <section>
    <header class="page-header">
      <div>
        <h2>{{ props.bench }}</h2>
        <p class="hostname">on <code>{{ props.server }}</code></p>
      </div>
    </header>

    <p v-if="detailError" class="error">Failed to load bench: {{ detailError }}</p>

    <div v-if="detail" class="summary">
      <div class="summary-item">
        <div class="label">Frappe</div>
        <div class="value">{{ detail.frappe_version || '—' }}</div>
      </div>
      <div class="summary-item">
        <div class="label">Apps</div>
        <div class="value">{{ detail.apps_count }}</div>
      </div>
      <div class="summary-item">
        <div class="label">Supervisor</div>
        <div class="value" :style="{ color: supervisorHealth(detail).color }">
          {{ supervisorHealth(detail).label }}
        </div>
      </div>
      <div class="summary-item">
        <div class="label">Queues</div>
        <div class="value">{{ queueRows.length }}</div>
      </div>
    </div>

    <div v-if="queueRows.length" class="queue-table">
      <h3 class="section-title">Redis queue depths (current)</h3>
      <table>
        <thead>
          <tr><th>Queue</th><th>Depth</th></tr>
        </thead>
        <tbody>
          <tr v-for="q in queueRows" :key="q.name">
            <td>{{ q.name }}</td>
            <td :class="{ warn: q.depth > 100 }">{{ q.depth }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="charts" v-if="detail">
      <MetricChart
        title="Supervisor processes"
        :result="supervisor.result.value"
        :loading="supervisor.loading.value"
        :error="supervisor.error.value"
        :series-label="supervisorLabel"
      />
      <MetricChart
        title="Redis queue depth"
        :result="queues.result.value"
        :loading="queues.loading.value"
        :error="queues.error.value"
        :series-label="queueLabel"
      />
      <MetricChart
        title="Apps count"
        :result="apps.result.value"
        :loading="apps.loading.value"
        :error="apps.error.value"
        :series-label="appsLabel"
      />
    </div>
  </section>
</template>

<style scoped>
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 1rem;
}
.hostname {
  color: var(--muted);
  font-size: 0.9rem;
  margin: 0;
}
.hostname code {
  font-family: ui-monospace, "SF Mono", Menlo, monospace;
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
.section-title {
  font-size: 0.95rem;
  margin: 1rem 0 0.5rem 0;
}
.queue-table {
  margin-bottom: 1rem;
}
.queue-table table {
  width: 100%;
  max-width: 480px;
  border-collapse: collapse;
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  border-radius: 6px;
  overflow: hidden;
}
.queue-table th,
.queue-table td {
  text-align: left;
  padding: 0.5rem 0.75rem;
  border-bottom: 1px solid var(--card-border);
  font-size: 0.9rem;
}
.queue-table tr:last-child td {
  border-bottom: none;
}
.queue-table th {
  font-size: 0.75rem;
  text-transform: uppercase;
  color: var(--muted);
  letter-spacing: 0.05em;
}
.warn {
  color: var(--status-unreachable);
  font-weight: 600;
}
.charts {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 1rem;
}
@media (max-width: 900px) {
  .charts {
    grid-template-columns: 1fr;
  }
}
.error {
  padding: 0.5rem 0.75rem;
  background: color-mix(in srgb, var(--status-unreachable) 12%, transparent);
  color: var(--status-unreachable);
  border-radius: 4px;
}
</style>
