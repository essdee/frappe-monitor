<script setup lang="ts">
import { computed, onMounted, onScopeDispose, ref, watch } from 'vue'
import {
  Bell,
  BellOff,
  RefreshCw,
  AlertTriangle,
  CheckCircle2,
  Info,
} from 'lucide-vue-next'
import { fetchAlerts, type AlertsResponse } from '../api'
import { useTimeRange } from '../composables/useTimeRange'

const { refreshSec } = useTimeRange()
const data = ref<AlertsResponse | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

async function refresh() {
  loading.value = true
  error.value = null
  try {
    data.value = await fetchAlerts()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

let timer: ReturnType<typeof setInterval> | null = null
function arm(secs: number) {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
  if (secs > 0) timer = setInterval(refresh, secs * 1000)
}
watch(refreshSec, (s) => arm(s), { immediate: true })
onScopeDispose(() => {
  if (timer) clearInterval(timer)
})
onMounted(refresh)

const enabled = computed(() => data.value?.enabled ?? false)
const rules = computed(() => data.value?.rules ?? [])
const firing = computed(() => data.value?.firing ?? [])

function severityColor(sev: string): string {
  switch (sev) {
    case 'critical':
      return 'var(--status-unreachable)'
    case 'warning':
      return 'var(--status-warning)'
    case 'info':
      return 'var(--accent)'
    default:
      return 'var(--muted)'
  }
}

function relativeTime(iso: string): string {
  const ts = new Date(iso).getTime()
  if (Number.isNaN(ts)) return ''
  const ageSec = Math.floor((Date.now() - ts) / 1000)
  if (ageSec < 60) return `${ageSec}s ago`
  if (ageSec < 3600) return `${Math.floor(ageSec / 60)}m ago`
  if (ageSec < 86400) return `${Math.floor(ageSec / 3600)}h ago`
  return `${Math.floor(ageSec / 86400)}d ago`
}

function formatLabels(labels: Record<string, string>): string {
  return Object.entries(labels)
    .filter(([k]) => k !== '__name__')
    .map(([k, v]) => `${k}=${v}`)
    .join(' · ')
}
</script>

<template>
  <section>
    <header class="page-header">
      <div class="title">
        <Bell :size="22" :stroke-width="2.25" class="title-icon" />
        <h2>Alerts</h2>
        <span class="count">{{ firing.length }}</span>
      </div>
      <button class="refresh-btn" :disabled="loading" @click="refresh">
        <RefreshCw :size="14" :stroke-width="2" :class="{ spin: loading }" />
        {{ loading ? 'Refreshing' : 'Refresh' }}
      </button>
    </header>

    <p v-if="error" class="error">Failed to load alerts: {{ error }}</p>

    <!-- Enabled banner -->
    <div v-if="data" class="banner" :class="enabled ? 'on' : 'off'">
      <component :is="enabled ? Bell : BellOff" :size="18" :stroke-width="2" />
      <div class="banner-text">
        <strong>{{ enabled ? 'Alerts active' : 'Alerts disabled' }}</strong>
        <span v-if="enabled">— Telegram notifications fire on every rule match.</span>
        <span v-else>
          —
          enable in <code>/etc/frappe-monitor/monitor.yaml</code>:
          set <code>alerts.enabled: true</code> with a bot token + chat IDs,
          then <code>sudo systemctl restart frappe-monitor</code>.
        </span>
      </div>
    </div>

    <!-- Firing right now -->
    <h3 class="section-title">
      <AlertTriangle :size="16" :stroke-width="2.25" />
      Currently firing
    </h3>
    <p v-if="firing.length === 0" class="empty">
      <CheckCircle2 :size="16" :stroke-width="2" />
      Nothing firing — system is healthy.
    </p>
    <div v-else class="firing-list">
      <div v-for="a in firing" :key="a.id" class="firing-card">
        <div class="firing-head">
          <span
            class="rule-name"
            :style="{ color: severityColor(rules.find((r) => r.name === a.rule_name)?.severity ?? '') }"
          >
            {{ a.rule_name }}
          </span>
          <span class="fp" :title="`Fingerprint: ${a.fingerprint}`">
            {{ formatLabels(a.labels) || a.fingerprint }}
          </span>
        </div>
        <div class="firing-meta">
          <span>value: <code>{{ a.value }}</code></span>
          <span>first fired: {{ relativeTime(a.first_fired_at) }}</span>
          <span>last paged: {{ relativeTime(a.last_notified_at) }}</span>
        </div>
      </div>
    </div>

    <!-- Configured rules -->
    <h3 class="section-title rule-title">
      <Info :size="16" :stroke-width="2.25" />
      Configured rules
    </h3>
    <p v-if="rules.length === 0" class="empty muted">
      No rules configured. Edit <code>alerts.rules</code> in
      <code>/etc/frappe-monitor/monitor.yaml</code> or rely on the bundled defaults
      (<code>alerts.disable_defaults: false</code>).
    </p>
    <table v-else class="rules-table">
      <thead>
        <tr>
          <th>Severity</th>
          <th>Name</th>
          <th>Expression</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="r in rules" :key="r.name">
          <td data-label="Severity">
            <span class="badge" :style="{ color: severityColor(r.severity), borderColor: severityColor(r.severity) }">
              {{ r.severity || 'info' }}
            </span>
          </td>
          <td class="rule-name-cell" data-label="Name">{{ r.name }}</td>
          <td class="expr" data-label="Expression"><code>{{ r.expr }}</code></td>
        </tr>
      </tbody>
    </table>
  </section>
</template>

<style scoped>
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 1.25rem;
}
.title {
  display: flex;
  align-items: center;
  gap: 0.6rem;
}
.title-icon { color: var(--accent); }
.title h2 {
  margin: 0;
  font-size: 1.35rem;
  font-weight: 600;
  letter-spacing: -0.02em;
}
.count {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 1.6rem;
  height: 1.4rem;
  padding: 0 0.4rem;
  background: var(--bg-hover);
  color: var(--muted);
  border-radius: 999px;
  font-size: 0.78rem;
  font-weight: 600;
}
.refresh-btn {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  background: var(--card-bg);
  color: var(--fg);
  border: 1px solid var(--card-border);
  padding: 0.45rem 0.9rem;
  border-radius: 6px;
  cursor: pointer;
  font: inherit;
  font-size: 0.88rem;
  font-weight: 500;
}
.refresh-btn:hover:not(:disabled) {
  border-color: var(--accent);
  background: var(--bg-hover);
}
.refresh-btn:disabled { opacity: 0.6; cursor: not-allowed; }
.spin { animation: spin 0.9s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

.banner {
  display: flex;
  gap: 0.75rem;
  padding: 0.75rem 1rem;
  border: 1px solid var(--card-border);
  border-radius: 8px;
  margin-bottom: 1.5rem;
  align-items: flex-start;
}
.banner.on {
  border-color: color-mix(in srgb, var(--status-reachable) 35%, var(--card-border));
  background: color-mix(in srgb, var(--status-reachable) 8%, transparent);
}
.banner.on > svg { color: var(--status-reachable); }
.banner.off {
  border-color: color-mix(in srgb, var(--status-warning) 35%, var(--card-border));
  background: color-mix(in srgb, var(--status-warning) 8%, transparent);
}
.banner.off > svg { color: var(--status-warning); }
.banner-text {
  font-size: 0.9rem;
  line-height: 1.45;
}

.section-title {
  display: flex;
  align-items: center;
  gap: 0.45rem;
  font-size: 0.95rem;
  margin: 1.5rem 0 0.6rem;
  color: var(--muted);
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.06em;
}
.rule-title {
  margin-top: 2rem;
}

.empty {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  color: var(--muted);
  font-size: 0.9rem;
  padding: 0.65rem 0.85rem;
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  border-radius: 6px;
}

.firing-list {
  display: flex;
  flex-direction: column;
  gap: 0.6rem;
}
.firing-card {
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  border-radius: 8px;
  padding: 0.85rem 1rem;
  box-shadow: var(--card-shadow);
}
.firing-head {
  display: flex;
  align-items: baseline;
  gap: 0.75rem;
  flex-wrap: wrap;
}
.rule-name {
  font-weight: 600;
  font-size: 0.95rem;
}
.fp {
  font-family: "JetBrains Mono", ui-monospace, "SF Mono", Menlo, monospace;
  font-size: 0.78rem;
  color: var(--muted);
}
.firing-meta {
  display: flex;
  gap: 1.25rem;
  margin-top: 0.4rem;
  font-size: 0.78rem;
  color: var(--muted);
  flex-wrap: wrap;
}

.rules-table {
  width: 100%;
  border-collapse: separate;
  border-spacing: 0;
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  border-radius: 8px;
  overflow: hidden;
  box-shadow: var(--card-shadow);
}
.rules-table th, .rules-table td {
  padding: 0.65rem 0.9rem;
  text-align: left;
  border-bottom: 1px solid var(--card-border);
  font-size: 0.88rem;
  vertical-align: top;
}
.rules-table tbody tr:last-child td { border-bottom: none; }
.rules-table th {
  font-size: 0.72rem;
  text-transform: uppercase;
  color: var(--muted);
  letter-spacing: 0.08em;
  background: var(--bg);
  font-weight: 600;
}
.rule-name-cell {
  font-weight: 600;
}
.expr code {
  font-size: 0.8rem;
  background: var(--bg-hover);
}
.badge {
  display: inline-flex;
  align-items: center;
  padding: 0.15rem 0.55rem;
  border-radius: 999px;
  border: 1px solid;
  font-size: 0.72rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.error {
  padding: 0.6rem 0.85rem;
  background: color-mix(in srgb, var(--status-unreachable) 12%, transparent);
  border: 1px solid color-mix(in srgb, var(--status-unreachable) 35%, transparent);
  color: var(--status-unreachable);
  border-radius: 6px;
}

/* ----- Mobile (≤720px) — cards instead of cramped tables ----- */
@media (max-width: 720px) {
  .page-header { flex-wrap: wrap; gap: 0.5rem; }
  .banner {
    flex-direction: column;
    align-items: flex-start;
    gap: 0.5rem;
    padding: 0.75rem;
  }
  .banner-text {
    font-size: 0.85rem;
    word-break: break-word;
  }
  .banner-text code {
    word-break: break-all;
  }
  .firing-card { padding: 0.7rem 0.85rem; }
  .firing-head {
    flex-direction: column;
    align-items: flex-start;
    gap: 0.2rem;
  }
  .fp {
    word-break: break-word;
    white-space: normal;
  }
  .firing-meta {
    flex-direction: column;
    gap: 0.3rem;
  }

  /* Rules table → vertical card list. Each <tr> becomes a card,
     each <td> a labeled row. The cramped 3-column grid gives way
     to readable full-width fields. */
  .rules-table { display: block; border: none; background: transparent; box-shadow: none; }
  .rules-table thead { display: none; }
  .rules-table tbody { display: block; }
  .rules-table tr {
    display: block;
    background: var(--card-bg);
    border: 1px solid var(--card-border);
    border-radius: 8px;
    padding: 0.6rem 0.8rem;
    margin-bottom: 0.6rem;
    box-shadow: var(--card-shadow);
  }
  .rules-table td {
    display: block;
    padding: 0.2rem 0;
    border: none;
    font-size: 0.85rem;
  }
  /* Synthetic labels via attr(data-label). We add data-label in the
     template. */
  .rules-table td::before {
    content: attr(data-label);
    display: block;
    font-size: 0.65rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--muted);
    font-weight: 600;
    margin-bottom: 0.15rem;
  }
  .rules-table .rule-name-cell { font-size: 0.95rem; }
  .expr code {
    display: block;
    font-size: 0.78rem;
    line-height: 1.45;
    word-break: break-word;
    white-space: pre-wrap;
    padding: 0.45rem 0.55rem;
  }
  .badge {
    font-size: 0.68rem;
    padding: 0.1rem 0.45rem;
  }
}
</style>
