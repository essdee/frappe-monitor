<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import {
  Cpu,
  HardDrive,
  MemoryStick,
  Activity,
  RefreshCw,
  Server,
  Box,
} from 'lucide-vue-next'
import {
  fetchSystemSnapshot,
  refreshSystemSnapshot,
  type SystemSnapshotResp,
} from '../api'

const props = defineProps<{ serverId: number }>()

const snapshot = ref<SystemSnapshotResp | null>(null)
const loading = ref(false)
const refreshing = ref(false)
const error = ref<string | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    snapshot.value = await fetchSystemSnapshot(props.serverId)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

async function refresh() {
  refreshing.value = true
  error.value = null
  try {
    snapshot.value = await refreshSystemSnapshot(props.serverId)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    refreshing.value = false
  }
}

onMounted(load)
watch(() => props.serverId, load)

// --- formatting helpers -------------------------------------------------

function bytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  while (n >= 1024 && i < u.length - 1) {
    n /= 1024
    i++
  }
  return `${n.toFixed(n >= 100 || i === 0 ? 0 : 1)} ${u[i]}`
}

function uptime(secs: number): string {
  if (!secs) return ''
  const d = Math.floor(secs / 86400)
  const h = Math.floor((secs % 86400) / 3600)
  const m = Math.floor((secs % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

function relativeTime(iso: string | undefined): string {
  if (!iso) return 'never'
  const ts = new Date(iso).getTime()
  if (Number.isNaN(ts)) return 'never'
  const ageSec = Math.floor((Date.now() - ts) / 1000)
  if (ageSec < 60) return `${ageSec}s ago`
  if (ageSec < 3600) return `${Math.floor(ageSec / 60)}m ago`
  if (ageSec < 86400) return `${Math.floor(ageSec / 3600)}h ago`
  return `${Math.floor(ageSec / 86400)}d ago`
}

const memUsedPct = computed(() => {
  const m = snapshot.value?.payload?.memory
  if (!m || !m.total_bytes) return 0
  return ((m.total_bytes - m.available_bytes) / m.total_bytes) * 100
})
const swapUsedPct = computed(() => {
  const m = snapshot.value?.payload?.memory
  if (!m || !m.swap_total_bytes) return 0
  return ((m.swap_total_bytes - m.swap_free_bytes) / m.swap_total_bytes) * 100
})
</script>

<template>
  <section class="card">
    <header class="head">
      <div class="title">
        <Server :size="18" :stroke-width="2.25" class="title-icon" />
        <h3>System details</h3>
        <span v-if="snapshot" class="captured">
          captured {{ relativeTime(snapshot.captured_at) }}
        </span>
      </div>
      <button class="btn" :disabled="refreshing" @click="refresh">
        <RefreshCw :size="14" :stroke-width="2" :class="{ spin: refreshing }" />
        {{ refreshing ? 'Refreshing' : 'Refresh' }}
      </button>
    </header>

    <p v-if="loading && !snapshot" class="muted">Loading…</p>
    <p v-if="error" class="error">{{ error }}</p>

    <p v-if="!loading && !snapshot && !error" class="empty">
      No snapshot yet. Click <strong>Refresh</strong> to capture system details
      over SSH (OS, CPU, memory, disks, top processes).
    </p>

    <p v-if="snapshot?.last_error" class="error">
      Last capture failed: {{ snapshot.last_error }}
    </p>

    <div v-if="snapshot?.payload" class="grid">
      <!-- System identity -->
      <div class="tile">
        <div class="tile-head">
          <Activity :size="14" :stroke-width="2" />
          <span>System</span>
        </div>
        <dl>
          <dt>OS</dt>     <dd>{{ snapshot.payload.system.os || '—' }}</dd>
          <dt>Kernel</dt> <dd class="mono">{{ snapshot.payload.system.kernel }}</dd>
          <dt>Arch</dt>   <dd class="mono">{{ snapshot.payload.system.arch }}</dd>
          <dt>Host</dt>   <dd class="mono">{{ snapshot.payload.system.hostname }}</dd>
          <dt>Uptime</dt> <dd>{{ uptime(snapshot.payload.system.uptime_seconds) }}</dd>
        </dl>
      </div>

      <!-- CPU -->
      <div class="tile">
        <div class="tile-head">
          <Cpu :size="14" :stroke-width="2" />
          <span>CPU</span>
        </div>
        <dl>
          <dt>Model</dt> <dd>{{ snapshot.payload.cpu.model || '—' }}</dd>
          <dt>Cores</dt> <dd>{{ snapshot.payload.cpu.cores }}</dd>
          <dt>Load</dt>  <dd class="mono">
            {{ snapshot.payload.load['1m'].toFixed(2) }} ·
            {{ snapshot.payload.load['5m'].toFixed(2) }} ·
            {{ snapshot.payload.load['15m'].toFixed(2) }}
          </dd>
        </dl>
      </div>

      <!-- Memory + swap -->
      <div class="tile">
        <div class="tile-head">
          <MemoryStick :size="14" :stroke-width="2" />
          <span>Memory</span>
        </div>
        <dl>
          <dt>Total</dt>     <dd>{{ bytes(snapshot.payload.memory.total_bytes) }}</dd>
          <dt>Available</dt> <dd>{{ bytes(snapshot.payload.memory.available_bytes) }}</dd>
          <dt>Used</dt>      <dd>{{ memUsedPct.toFixed(1) }}%</dd>
        </dl>
        <div class="bar"><span :style="{ width: memUsedPct + '%' }" /></div>

        <dl class="dense">
          <dt>Swap</dt>
          <dd>
            {{ bytes(snapshot.payload.memory.swap_total_bytes - snapshot.payload.memory.swap_free_bytes) }}
            / {{ bytes(snapshot.payload.memory.swap_total_bytes) }}
          </dd>
        </dl>
        <div class="bar swap"><span :style="{ width: swapUsedPct + '%' }" /></div>
      </div>
    </div>

    <!-- Disks -->
    <div v-if="snapshot?.payload" class="block">
      <h4>
        <HardDrive :size="14" :stroke-width="2" />
        Disks
      </h4>
      <table>
        <thead>
          <tr>
            <th>Mount</th>
            <th>Filesystem</th>
            <th class="num">Used</th>
            <th class="num">Total</th>
            <th>Usage</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="d in snapshot.payload.disks" :key="d.mount">
            <td class="mono">{{ d.mount }}</td>
            <td class="mono muted">{{ d.device }}</td>
            <td class="num">{{ bytes(d.used_bytes) }}</td>
            <td class="num">{{ bytes(d.total_bytes) }}</td>
            <td class="bar-cell">
              <div class="bar inline">
                <span :style="{ width: d.use_pct + '%' }" />
              </div>
              <span class="pct">{{ d.use_pct }}%</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Top processes -->
    <div v-if="snapshot?.payload" class="proc-grid">
      <div class="block">
        <h4>
          <Cpu :size="14" :stroke-width="2" />
          Top by CPU
        </h4>
        <table class="procs">
          <thead>
            <tr>
              <th class="num">PID</th>
              <th>User</th>
              <th>Command</th>
              <th class="num">CPU%</th>
              <th class="num">MEM%</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in snapshot.payload.top_cpu" :key="p.pid + ':cpu'">
              <td class="num mono">{{ p.pid }}</td>
              <td class="mono">{{ p.user }}</td>
              <td class="mono cmd">{{ p.command }}</td>
              <td class="num">{{ p.cpu_pct.toFixed(1) }}</td>
              <td class="num muted">{{ p.mem_pct.toFixed(1) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="block">
        <h4>
          <Box :size="14" :stroke-width="2" />
          Top by memory
        </h4>
        <table class="procs">
          <thead>
            <tr>
              <th class="num">PID</th>
              <th>User</th>
              <th>Command</th>
              <th class="num">RSS</th>
              <th class="num">MEM%</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in snapshot.payload.top_mem" :key="p.pid + ':mem'">
              <td class="num mono">{{ p.pid }}</td>
              <td class="mono">{{ p.user }}</td>
              <td class="mono cmd">{{ p.command }}</td>
              <td class="num">{{ bytes(p.rss_kb * 1024) }}</td>
              <td class="num">{{ p.mem_pct.toFixed(1) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </section>
</template>

<style scoped>
.card {
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  border-radius: 8px;
  padding: 1rem 1.25rem 1.25rem;
  box-shadow: var(--card-shadow);
  margin-bottom: 1.5rem;
}
.head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 0.85rem;
}
.title {
  display: flex;
  align-items: center;
  gap: 0.55rem;
}
.title-icon { color: var(--accent); }
.title h3 { margin: 0; font-size: 1rem; font-weight: 600; }
.captured { color: var(--muted); font-size: 0.78rem; margin-left: 0.5rem; }
.btn {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  color: var(--fg);
  padding: 0.4rem 0.85rem;
  border-radius: 6px;
  font: inherit;
  font-size: 0.85rem;
  font-weight: 500;
  cursor: pointer;
}
.btn:hover:not(:disabled) {
  border-color: var(--accent);
  background: var(--bg-hover);
}
.btn:disabled { opacity: 0.6; cursor: not-allowed; }
.spin { animation: spin 0.9s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

.empty, .muted { color: var(--muted); font-size: 0.9rem; }
.error {
  padding: 0.5rem 0.75rem;
  background: color-mix(in srgb, var(--status-unreachable) 12%, transparent);
  border: 1px solid color-mix(in srgb, var(--status-unreachable) 35%, transparent);
  color: var(--status-unreachable);
  border-radius: 6px;
  margin-bottom: 0.75rem;
  font-size: 0.85rem;
}

.grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: 0.75rem;
  margin-bottom: 1rem;
}
.tile {
  background: var(--bg-elevated);
  border: 1px solid var(--card-border);
  border-radius: 6px;
  padding: 0.75rem 0.85rem;
}
.tile-head {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  font-size: 0.75rem;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--muted);
  font-weight: 600;
  margin-bottom: 0.6rem;
}

dl {
  display: grid;
  grid-template-columns: 5rem 1fr;
  gap: 0.25rem 0.5rem;
  margin: 0;
  font-size: 0.85rem;
}
dl.dense {
  margin-top: 0.5rem;
}
dt { color: var(--muted); }
dd {
  margin: 0;
  word-break: break-word;
}
.mono {
  font-family: "JetBrains Mono", ui-monospace, "SF Mono", Menlo, monospace;
  font-size: 0.82rem;
}

.bar {
  margin-top: 0.4rem;
  height: 6px;
  background: var(--bg-hover);
  border-radius: 3px;
  overflow: hidden;
}
.bar > span {
  display: block;
  height: 100%;
  background: var(--accent);
  transition: width 200ms ease;
}
.bar.swap > span { background: var(--status-warning); }
.bar.inline {
  display: inline-block;
  width: 80px;
  vertical-align: middle;
  margin-right: 0.4rem;
  margin-top: 0;
}

.block {
  margin-top: 1rem;
}
.block h4 {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  font-size: 0.85rem;
  margin: 0 0 0.5rem 0;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--muted);
  font-weight: 600;
}

table {
  width: 100%;
  border-collapse: separate;
  border-spacing: 0;
  background: var(--bg-elevated);
  border: 1px solid var(--card-border);
  border-radius: 6px;
  overflow: hidden;
  font-size: 0.84rem;
}
th, td {
  padding: 0.45rem 0.7rem;
  text-align: left;
  border-bottom: 1px solid var(--card-border);
}
tbody tr:last-child td { border-bottom: none; }
tbody tr:hover { background: var(--bg-hover); }
th {
  font-size: 0.7rem;
  text-transform: uppercase;
  color: var(--muted);
  letter-spacing: 0.06em;
  font-weight: 600;
}
.num { text-align: right; font-variant-numeric: tabular-nums; }
.bar-cell { white-space: nowrap; }
.pct {
  font-variant-numeric: tabular-nums;
  font-size: 0.78rem;
  color: var(--muted);
}
.cmd {
  max-width: 28ch;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.proc-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(360px, 1fr));
  gap: 0.85rem;
}

/* ----- mobile ----- */
@media (max-width: 720px) {
  .card {
    padding: 0.75rem 0.85rem 1rem;
  }
  .head { flex-wrap: wrap; gap: 0.5rem; }
  .grid {
    grid-template-columns: 1fr;
    gap: 0.6rem;
  }
  /* DL columns get tighter; values truncate gracefully. */
  dl {
    grid-template-columns: 4.5rem 1fr;
    font-size: 0.82rem;
  }
  dd { word-break: break-word; }
  .cmd { max-width: 16ch; }
  /* Process tables horizontal-scroll on phone (inherits from style.css);
     the inline disk usage bar shrinks. */
  .bar.inline { width: 50px; }
  table { font-size: 0.8rem; }
  th, td { padding: 0.4rem 0.55rem; }
  .proc-grid { grid-template-columns: 1fr; }
}
</style>
