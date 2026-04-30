<script setup lang="ts">
import { ref, computed, watch, onMounted } from 'vue'
import { fetchServer, type Server } from '../api'
import { useTimeRange } from '../composables/useTimeRange'
import { useMetricsRange } from '../composables/useMetricsRange'
import MetricChart from '../components/MetricChart.vue'

const props = defineProps<{ id: number }>()

const server = ref<Server | null>(null)
const serverError = ref<string | null>(null)

const { range, refreshSec } = useTimeRange()

async function loadServer() {
  serverError.value = null
  try {
    server.value = await fetchServer(props.id)
  } catch (e) {
    serverError.value = e instanceof Error ? e.message : String(e)
  }
}

onMounted(loadServer)
watch(() => props.id, loadServer)

// CPU usage % derived from raw jiffies. (1 - idle_rate / total_rate) * 100.
const cpuQuery = computed(() => {
  const name = server.value?.name ?? ''
  if (!name) return ''
  return `100 * (1 - rate(frappe_server_cpu_idle{server="${name}"}[5m]) / clamp_min(
    rate(frappe_server_cpu_user{server="${name}"}[5m]) +
    rate(frappe_server_cpu_nice{server="${name}"}[5m]) +
    rate(frappe_server_cpu_system{server="${name}"}[5m]) +
    rate(frappe_server_cpu_idle{server="${name}"}[5m]) +
    rate(frappe_server_cpu_iowait{server="${name}"}[5m]) +
    rate(frappe_server_cpu_irq{server="${name}"}[5m]) +
    rate(frappe_server_cpu_softirq{server="${name}"}[5m]) +
    rate(frappe_server_cpu_steal{server="${name}"}[5m]),
    1
  ))`
})

const memQuery = computed(() => {
  const name = server.value?.name ?? ''
  if (!name) return ''
  return `100 * (1 - frappe_server_mem_available_bytes{server="${name}"} / frappe_server_mem_total_bytes{server="${name}"})`
})

const diskQuery = computed(() => {
  const name = server.value?.name ?? ''
  if (!name) return ''
  return `100 * frappe_server_disk_used_bytes{server="${name}"} / frappe_server_disk_total_bytes{server="${name}"}`
})

const loadQuery = computed(() => {
  const name = server.value?.name ?? ''
  if (!name) return ''
  return `frappe_server_load_1m{server="${name}"} or frappe_server_load_5m{server="${name}"} or frappe_server_load_15m{server="${name}"}`
})

const cpu = useMetricsRange(cpuQuery, range, refreshSec)
const mem = useMetricsRange(memQuery, range, refreshSec)
const disk = useMetricsRange(diskQuery, range, refreshSec)
const load = useMetricsRange(loadQuery, range, refreshSec)

const cpuLabel = (_m: Record<string, string>) => 'cpu used'
const memLabel = (_m: Record<string, string>) => 'mem used'
const diskLabel = (m: Record<string, string>) => `mount ${m.mount ?? '?'}`
const loadLabel = (m: Record<string, string>) => {
  // PromQL `or` doesn't preserve which metric a series came from in
  // labels; show the metric name from __name__ if present, otherwise
  // pick the first label that disambiguates. ECharts' legend handles
  // duplicates by suffixing.
  return m.__name__ ?? 'load'
}
</script>

<template>
  <section>
    <header v-if="server" class="page-header">
      <div>
        <h2>{{ server.name }}</h2>
        <p class="hostname">
          {{ server.hostname }} · ssh
          <code>{{ server.ssh_user }}@{{ server.hostname }}:{{ server.ssh_port }}</code>
        </p>
      </div>
      <div class="status-block">
        <span class="status-text">{{ server.status }}</span>
      </div>
    </header>

    <p v-if="serverError" class="error">Failed to load server: {{ serverError }}</p>

    <div class="charts" v-if="server">
      <MetricChart
        title="CPU usage (%)"
        :result="cpu.result.value"
        :loading="cpu.loading.value"
        :error="cpu.error.value"
        :series-label="cpuLabel"
        y-unit="%"
      />
      <MetricChart
        title="Memory usage (%)"
        :result="mem.result.value"
        :loading="mem.loading.value"
        :error="mem.error.value"
        :series-label="memLabel"
        y-unit="%"
      />
      <MetricChart
        title="Disk usage (% per mount)"
        :result="disk.result.value"
        :loading="disk.loading.value"
        :error="disk.error.value"
        :series-label="diskLabel"
        y-unit="%"
      />
      <MetricChart
        title="Load average (1/5/15)"
        :result="load.result.value"
        :loading="load.loading.value"
        :error="load.error.value"
        :series-label="loadLabel"
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
.status-text {
  text-transform: capitalize;
  font-weight: 600;
  font-size: 0.85rem;
  padding: 0.25rem 0.6rem;
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  border-radius: 4px;
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
