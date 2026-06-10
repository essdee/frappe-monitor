<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { Database, Plus, RefreshCw, Pencil, Trash2, Play } from 'lucide-vue-next'
import {
  fetchServers,
  createDBTarget,
  patchDBTarget,
  deleteDBTarget,
  checkDBTarget,
  type Server,
  type DBTarget,
  type NewDBTargetInput,
} from '../api'
import { useDBTargets } from '../composables/useDBTargets'

const { targets, loading, error, refresh } = useDBTargets()

const servers = ref<Server[]>([])
onMounted(async () => {
  try {
    servers.value = await fetchServers()
  } catch {
    /* form just shows an empty dropdown */
  }
})
// Prefer the server name carried on the live db.status push (always
// current, even for a server added after this page mounted); fall back to
// the once-fetched server list, then the raw id.
const serverLabel = (t: DBTarget) =>
  t.server || servers.value.find((s) => s.id === t.server_id)?.name || `#${t.server_id}`

// --- add / edit form -------------------------------------------------------
const showForm = ref(false)
const editingId = ref<number | null>(null)
const formError = ref<string | null>(null)
const saving = ref(false)
const blank = (): NewDBTargetInput & { enabled: boolean } => ({
  server_id: servers.value[0]?.id ?? 0,
  name: '',
  enabled: true,
  lag_threshold_seconds: 30,
  mysql_command: 'mysql',
  defaults_file: '',
  socket: '',
  heartbeat_enabled: false,
  heartbeat_query: '',
})
const form = reactive(blank())

function openAdd() {
  Object.assign(form, blank())
  editingId.value = null
  formError.value = null
  showForm.value = true
}
function openEdit(t: DBTarget) {
  Object.assign(form, {
    server_id: t.server_id,
    name: t.name,
    enabled: t.enabled,
    lag_threshold_seconds: t.lag_threshold_seconds,
    mysql_command: t.mysql_command,
    defaults_file: t.defaults_file ?? '',
    socket: t.socket ?? '',
    heartbeat_enabled: t.heartbeat_enabled,
    heartbeat_query: t.heartbeat_query ?? '',
  })
  editingId.value = t.id
  formError.value = null
  showForm.value = true
}
function closeForm() {
  showForm.value = false
}

async function save() {
  if (!form.name || !form.server_id) {
    formError.value = 'name and server are required'
    return
  }
  saving.value = true
  formError.value = null
  try {
    if (editingId.value != null) {
      await patchDBTarget(editingId.value, { ...form })
    } else {
      await createDBTarget({ ...form })
    }
    showForm.value = false
    // The WS db.created/updated event also lands; refresh covers the
    // case where this tab created it before subscribing.
    await refresh()
  } catch (e) {
    formError.value = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = false
  }
}

// --- per-row actions -------------------------------------------------------
const busy = reactive<Record<number, boolean>>({})

async function checkNow(t: DBTarget) {
  busy[t.id] = true
  try {
    await checkDBTarget(t.id)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy[t.id] = false
  }
}

async function remove(t: DBTarget) {
  if (!window.confirm(`Delete DB target "${t.name}"?`)) return
  busy[t.id] = true
  try {
    await deleteDBTarget(t.id)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    delete busy[t.id] // row is gone (or re-enable on failure); don't leak the key
  }
}

// --- display helpers -------------------------------------------------------
const sorted = computed(() => {
  const order: Record<string, number> = { broken: 0, unreachable: 1, lagging: 2, unknown: 3, healthy: 4 }
  return [...targets.value].sort(
    (a, b) => (order[a.status] ?? 9) - (order[b.status] ?? 9) || a.name.localeCompare(b.name),
  )
})
function statusColor(s: string): string {
  switch (s) {
    case 'healthy': return 'var(--status-reachable)'
    case 'lagging': return 'var(--status-warning)'
    case 'broken':
    case 'unreachable': return 'var(--status-unreachable)'
    default: return 'var(--status-unknown)'
  }
}
function lagText(t: DBTarget): string {
  if (t.heartbeat_lag_seconds != null) return `${t.heartbeat_lag_seconds.toFixed(1)}s (heartbeat)`
  if (t.lag_seconds != null) return `${t.lag_seconds}s`
  return '—'
}
</script>

<template>
  <section>
    <header class="page-header">
      <h2><Database :size="20" /> Databases &mdash; replication</h2>
      <button class="primary" @click="openAdd"><Plus :size="16" /> Add database</button>
    </header>

    <p v-if="error" class="error">{{ error }}</p>

    <form v-if="showForm" class="db-form" @submit.prevent="save">
      <h3>{{ editingId != null ? 'Edit' : 'Add' }} DB target</h3>
      <div class="grid">
        <label>Name
          <input v-model="form.name" placeholder="prod-db-replica" required />
        </label>
        <label>Server (SSH host)
          <select v-model.number="form.server_id" required>
            <option v-for="s in servers" :key="s.id" :value="s.id">{{ s.name }} ({{ s.hostname }})</option>
          </select>
        </label>
        <label>Lag threshold (seconds)
          <input v-model.number="form.lag_threshold_seconds" type="number" min="1" />
        </label>
        <label>mysql command
          <input v-model="form.mysql_command" placeholder="mysql" />
        </label>
        <label>Defaults file (creds)
          <input v-model="form.defaults_file" placeholder="~/.my.cnf" />
        </label>
        <label>Socket (optional)
          <input v-model="form.socket" placeholder="/run/mysqld/mysqld.sock" />
        </label>
        <label class="checkbox">
          <input v-model="form.enabled" type="checkbox" /> Enabled
        </label>
        <label class="checkbox">
          <input v-model="form.heartbeat_enabled" type="checkbox" /> Heartbeat check
        </label>
        <label v-if="form.heartbeat_enabled" class="wide">Heartbeat query (returns lag seconds)
          <input v-model="form.heartbeat_query"
            placeholder="SELECT TIMESTAMPDIFF(SECOND, ts, NOW()) FROM heartbeat.heartbeat LIMIT 1" />
        </label>
      </div>
      <p v-if="formError" class="error">{{ formError }}</p>
      <div class="actions">
        <button class="primary" type="submit" :disabled="saving">{{ saving ? 'Saving…' : 'Save' }}</button>
        <button type="button" @click="closeForm">Cancel</button>
      </div>
    </form>

    <p v-if="loading && targets.length === 0" class="muted">Loading…</p>
    <p v-else-if="targets.length === 0" class="muted">
      No DB targets yet. Add a replica to watch its replication lag.
    </p>

    <ul class="db-list">
      <li v-for="t in sorted" :key="t.id" class="db-card" :style="{ borderLeftColor: statusColor(t.status) }">
        <div class="db-main">
          <div class="db-title">
            <strong>{{ t.name }}</strong>
            <span class="badge" :style="{ background: statusColor(t.status) }">{{ t.status }}</span>
            <span v-if="!t.enabled" class="badge muted-badge">disabled</span>
          </div>
          <div class="db-meta">
            <span>on <code>{{ serverLabel(t) }}</code></span>
            <span>lag: <b>{{ lagText(t) }}</b> (threshold {{ t.lag_threshold_seconds }}s)</span>
            <span>IO: <b :style="{ color: t.io_running ? 'var(--status-reachable)' : 'var(--status-unreachable)' }">{{ t.io_running ? 'Yes' : 'No' }}</b></span>
            <span>SQL: <b :style="{ color: t.sql_running ? 'var(--status-reachable)' : 'var(--status-unreachable)' }">{{ t.sql_running ? 'Yes' : 'No' }}</b></span>
          </div>
          <p v-if="t.last_error" class="db-err">{{ t.last_error }}</p>
        </div>
        <div class="db-actions">
          <button :disabled="busy[t.id]" title="Check now" @click="checkNow(t)"><Play :size="15" /></button>
          <button title="Edit" @click="openEdit(t)"><Pencil :size="15" /></button>
          <button class="danger" :disabled="busy[t.id]" title="Delete" @click="remove(t)"><Trash2 :size="15" /></button>
        </div>
      </li>
    </ul>

    <button class="reload" :disabled="loading" @click="refresh"><RefreshCw :size="14" /> Reload</button>
  </section>
</template>

<style scoped>
.page-header { display: flex; align-items: center; justify-content: space-between; gap: 1rem; flex-wrap: wrap; }
.page-header h2 { display: inline-flex; align-items: center; gap: 0.5rem; }
button { display: inline-flex; align-items: center; gap: 0.35rem; background: var(--card-bg); color: var(--fg);
  border: 1px solid var(--card-border); padding: 0.35rem 0.7rem; border-radius: 5px; cursor: pointer; font: inherit; }
button:hover { border-color: var(--accent); }
button.primary { background: var(--accent); color: #fff; border-color: var(--accent); }
button.danger:hover { border-color: var(--status-unreachable); color: var(--status-unreachable); }
.error { color: var(--status-unreachable); }
.muted { color: var(--muted); }

.db-form { background: var(--card-bg); border: 1px solid var(--card-border); border-radius: 8px; padding: 1rem; margin: 1rem 0; }
.db-form h3 { margin-top: 0; }
.db-form .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 0.75rem; }
.db-form label { display: flex; flex-direction: column; gap: 0.25rem; font-size: 0.85rem; color: var(--muted); }
.db-form label.checkbox { flex-direction: row; align-items: center; gap: 0.4rem; }
.db-form label.wide { grid-column: 1 / -1; }
.db-form input, .db-form select { background: var(--bg); color: var(--fg); border: 1px solid var(--card-border);
  padding: 0.4rem 0.5rem; border-radius: 4px; font: inherit; }
.db-form .actions { margin-top: 0.75rem; display: flex; gap: 0.5rem; }

.db-list { list-style: none; padding: 0; margin: 1rem 0; display: flex; flex-direction: column; gap: 0.5rem; }
.db-card { display: flex; justify-content: space-between; gap: 1rem; align-items: flex-start;
  background: var(--card-bg); border: 1px solid var(--card-border); border-left-width: 4px; border-radius: 6px; padding: 0.75rem 1rem; }
.db-title { display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap; }
.badge { color: #fff; font-size: 0.7rem; padding: 0.1rem 0.45rem; border-radius: 999px; text-transform: uppercase; letter-spacing: 0.03em; }
.muted-badge { background: var(--muted) !important; }
.db-meta { display: flex; gap: 1rem; flex-wrap: wrap; color: var(--muted); font-size: 0.85rem; margin-top: 0.35rem; }
.db-err { color: var(--status-unreachable); font-size: 0.8rem; margin: 0.4rem 0 0; word-break: break-word; }
.db-actions { display: flex; gap: 0.3rem; flex-shrink: 0; }
.reload { margin-top: 0.5rem; }
</style>
