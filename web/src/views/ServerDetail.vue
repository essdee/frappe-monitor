<script setup lang="ts">
import { ref, computed, watch, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { Pencil } from 'lucide-vue-next'
import {
  fetchServer,
  testServerConnection,
  deployCollector,
  deleteServer,
  type Server,
  type TestConnectionResult,
} from '../api'
import { useTimeRange } from '../composables/useTimeRange'
import { useMetricsRange } from '../composables/useMetricsRange'
import { useRealtimeTopic } from '../composables/useRealtime'
import { topics } from '../realtime'
import MetricChart from '../components/MetricChart.vue'
import SystemDetailsCard from '../components/SystemDetailsCard.vue'
import EditServerForm from '../components/EditServerForm.vue'

const props = defineProps<{ id: number }>()
const router = useRouter()

const server = ref<Server | null>(null)
const serverError = ref<string | null>(null)

const probeResult = ref<TestConnectionResult | null>(null)
const probeRunning = ref(false)
const deployMsg = ref<string | null>(null)
const deployRunning = ref(false)
const deleteRunning = ref(false)
const showEdit = ref(false)

function onEdited(updated: Server) {
  server.value = updated
  showEdit.value = false
  // Clear stale probe banners — the SSH target may have changed,
  // and showing "SSH reachable" against the old IP would mislead.
  probeResult.value = null
  deployMsg.value = null
}

const { range } = useTimeRange()

// Numeric id for the realtime metrics topic.
const serverId = computed<number | null>(() => (Number.isFinite(props.id) ? props.id : null))

// Live status: patch the header badge the instant a pull updates this
// server's reachability, instead of waiting for a manual reload.
useRealtimeTopic(topics.server(props.id), {
  'server.status': (ev) => {
    if (server.value && ev.data?.id === props.id) {
      server.value = {
        ...server.value,
        status: ev.data.status,
        last_error: ev.data.last_error,
        last_pinged_at: ev.data.last_pinged_at,
      }
    }
  },
})

async function loadServer() {
  serverError.value = null
  try {
    server.value = await fetchServer(props.id)
  } catch (e) {
    serverError.value = e instanceof Error ? e.message : String(e)
  }
}

async function runProbe() {
  probeRunning.value = true
  probeResult.value = null
  try {
    probeResult.value = await testServerConnection(props.id)
  } catch (e) {
    probeResult.value = {
      reachable: false,
      latency_ms: 0,
      error: e instanceof Error ? e.message : String(e),
      error_kind: 'unknown',
    }
  } finally {
    probeRunning.value = false
  }
  // Re-fetch the server row so the page-header status badge reflects
  // the probe outcome — otherwise the UI shows "unreachable" right
  // next to a green "SSH reachable" probe-result banner, which is the
  // exact contradiction operators flag as "the dashboard is wrong".
  // The probe handler already wrote the new status to the DB; we just
  // need to surface it.
  await loadServer()
}

async function runDeploy() {
  deployRunning.value = true
  deployMsg.value = null
  try {
    const r = await deployCollector(props.id)
    deployMsg.value = `Collector v${r.version} deployed.`
  } catch (e) {
    deployMsg.value = `Deploy failed: ${e instanceof Error ? e.message : String(e)}`
  } finally {
    deployRunning.value = false
  }
}

async function runDelete() {
  if (!server.value) return
  if (!window.confirm(
    `Delete server "${server.value.name}" (${server.value.hostname})?\n\n` +
    `This stops collecting metrics + logs from it. Existing data in VictoriaMetrics ` +
    `and Loki will age out on the configured retention.`
  )) return
  deleteRunning.value = true
  try {
    await deleteServer(props.id)
    router.push('/servers')
  } catch (e) {
    serverError.value = e instanceof Error ? e.message : String(e)
  } finally {
    deleteRunning.value = false
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

const cpu = useMetricsRange(cpuQuery, range, serverId)
const mem = useMetricsRange(memQuery, range, serverId)
const disk = useMetricsRange(diskQuery, range, serverId)
const load = useMetricsRange(loadQuery, range, serverId)

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

    <div v-if="server && !showEdit" class="actions">
      <button :disabled="probeRunning" @click="runProbe">
        {{ probeRunning ? 'Testing…' : 'Test SSH' }}
      </button>
      <button :disabled="deployRunning" @click="runDeploy">
        {{ deployRunning ? 'Deploying…' : 'Deploy collector' }}
      </button>
      <button class="edit" @click="showEdit = true">
        <Pencil :size="14" :stroke-width="2" /> Edit
      </button>
      <button class="danger" :disabled="deleteRunning" @click="runDelete">
        {{ deleteRunning ? 'Deleting…' : 'Delete' }}
      </button>
    </div>

    <EditServerForm
      v-if="server && showEdit"
      :server="server"
      @saved="onEdited"
      @cancelled="showEdit = false"
    />

    <p
      v-if="probeResult"
      class="probe-result"
      :class="probeResult.reachable ? 'ok' : 'fail'"
    >
      <template v-if="probeResult.reachable">
        SSH reachable (latency {{ probeResult.latency_ms }} ms).
      </template>
      <template v-else>
        SSH failed ({{ probeResult.error_kind ?? 'unknown' }}): {{ probeResult.error }}
      </template>
    </p>
    <p v-if="deployMsg" class="probe-result ok">{{ deployMsg }}</p>

    <p v-if="serverError" class="error">Failed to load server: {{ serverError }}</p>

    <SystemDetailsCard v-if="server" :server-id="props.id" />

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
.actions {
  display: flex;
  gap: 0.5rem;
  margin-bottom: 0.75rem;
}
.actions button {
  background: var(--card-bg);
  color: var(--fg);
  border: 1px solid var(--card-border);
  padding: 0.35rem 0.85rem;
  border-radius: 4px;
  cursor: pointer;
  font: inherit;
}
.actions button:hover:not(:disabled) { border-color: var(--accent); }
.actions button:disabled { opacity: 0.6; cursor: not-allowed; }
.actions button.edit {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
  border-color: var(--accent);
  color: var(--accent);
}
.actions button.edit:hover:not(:disabled) {
  background: var(--accent-soft);
}
.actions button.danger {
  border-color: var(--status-unreachable);
  color: var(--status-unreachable);
}
.actions button.danger:hover:not(:disabled) {
  background: color-mix(in srgb, var(--status-unreachable) 12%, transparent);
}
.probe-result {
  padding: 0.5rem 0.75rem;
  border-radius: 4px;
  font-size: 0.9rem;
  margin-bottom: 0.75rem;
}
.probe-result.ok {
  background: color-mix(in srgb, var(--status-reachable) 12%, transparent);
  color: var(--status-reachable);
}
.probe-result.fail {
  background: color-mix(in srgb, var(--status-unreachable) 12%, transparent);
  color: var(--status-unreachable);
}

@media (max-width: 720px) {
  .page-header {
    flex-direction: column;
    align-items: stretch;
    gap: 0.5rem;
  }
  .page-header h2 {
    font-size: 1.15rem;
    word-break: break-word;
  }
  .hostname {
    font-size: 0.82rem;
    word-break: break-all;
  }
  .hostname code {
    word-break: break-all;
    font-size: 0.78rem;
  }
  /* Status badge moves under the hostname; flex-start so it doesn't
     hug the right edge once the column is narrow. */
  .status-block { align-self: flex-start; }

  .actions {
    display: grid;
    /* Two rows: [Test SSH | Deploy collector] / [Edit | Delete].
       Grid gives equal widths, keeps Delete next to Edit (so the
       red button isn't visually stranded), and avoids wrap-fight
       between flex children of different label widths. */
    grid-template-columns: 1fr 1fr;
    gap: 0.4rem;
  }
  .actions button {
    width: 100%;
    text-align: center;
    justify-content: center;
    padding: 0.55rem 0.7rem;
  }
  .probe-result {
    word-break: break-word;
    font-size: 0.85rem;
  }
}
</style>
