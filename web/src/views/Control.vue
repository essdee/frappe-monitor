<script setup lang="ts">
import { computed, onMounted, reactive, ref, type Component } from 'vue'
import {
  SlidersHorizontal,
  Play,
  RefreshCw,
  ChevronDown,
  FileCog,
  TriangleAlert,
  Loader,
  CheckCircle2,
  XCircle,
  Clock,
} from 'lucide-vue-next'
import {
  fetchServers,
  fetchControlActions,
  runControlAction,
  fetchSiteConfig,
  writeSiteConfig,
  type Server,
  type ControlActionDef,
} from '../api'
import { useControlHistory } from '../composables/useControlHistory'

const { history, loading, error, refresh } = useControlHistory(60)

const servers = ref<Server[]>([])
const actions = ref<ControlActionDef[]>([])
const loadErr = ref<string | null>(null)

onMounted(async () => {
  try {
    ;[servers.value, actions.value] = await Promise.all([fetchServers(), fetchControlActions()])
    if (servers.value[0]) {
      runForm.server_id = servers.value[0].id
      cfg.server_id = servers.value[0].id
    }
    if (actions.value[0]) runForm.action = actions.value[0].key
  } catch (e) {
    loadErr.value = e instanceof Error ? e.message : String(e)
  }
})

const serverById = (id: number) => servers.value.find((s) => s.id === id)
const labelOf = (key: string) =>
  key === 'site.config-edit' ? 'Edit site config' : actions.value.find((a) => a.key === key)?.label ?? key

// --- run a command ---------------------------------------------------------
const runForm = reactive({ server_id: 0, action: '', bench_path: '', site: '' })
const running = ref(false)
const runError = ref<string | null>(null)

const selectedDef = computed(() => actions.value.find((a) => a.key === runForm.action))
const needsBench = computed(() => ['bench', 'site'].includes(selectedDef.value?.scope ?? ''))
const needsSite = computed(() => selectedDef.value?.scope === 'site')
const benchOptions = computed(() => serverById(runForm.server_id)?.bench_paths ?? [])

const grouped = computed(() => {
  const g: Record<string, ControlActionDef[]> = { server: [], bench: [], site: [] }
  for (const a of actions.value) (g[a.scope] ?? (g[a.scope] = [])).push(a)
  return g
})

const canRun = computed(
  () =>
    !!runForm.server_id &&
    !!runForm.action &&
    (!needsBench.value || !!runForm.bench_path.trim()) &&
    (!needsSite.value || !!runForm.site.trim()),
)

async function run() {
  if (!canRun.value || running.value) return
  const def = selectedDef.value
  const srv = serverById(runForm.server_id)
  let msg = `Run "${def?.label ?? runForm.action}" on ${srv?.name ?? 'this server'}`
  if (needsSite.value) msg += ` · site ${runForm.site}`
  else if (needsBench.value) msg += ` · ${runForm.bench_path}`
  if (def?.dangerous) msg += '\n\n⚠ This is disruptive and may take several minutes.'
  msg += '\n\nProceed?'
  if (!window.confirm(msg)) return

  running.value = true
  runError.value = null
  try {
    await runControlAction({
      server_id: runForm.server_id,
      action: runForm.action,
      bench_path: needsBench.value ? runForm.bench_path.trim() : undefined,
      site: needsSite.value ? runForm.site.trim() : undefined,
    })
    // The control.started WS event lands the new row; nothing else to do.
  } catch (e) {
    runError.value = e instanceof Error ? e.message : String(e)
  } finally {
    running.value = false
  }
}

// --- site config editor ----------------------------------------------------
const cfg = reactive({ server_id: 0, bench_path: '', site: '', content: '', restart: false })
const cfgLoaded = ref(false)
const cfgBusy = ref(false)
const cfgError = ref<string | null>(null)
const cfgNote = ref<string | null>(null)
const cfgBenchOptions = computed(() => serverById(cfg.server_id)?.bench_paths ?? [])

async function loadConfig() {
  if (!cfg.server_id || !cfg.bench_path.trim() || !cfg.site.trim()) {
    cfgError.value = 'server, bench path and site are required'
    return
  }
  cfgBusy.value = true
  cfgError.value = null
  cfgNote.value = null
  try {
    const raw = await fetchSiteConfig(cfg.server_id, cfg.bench_path.trim(), cfg.site.trim())
    // Pretty-print when it parses, so it's pleasant to edit; fall back to raw.
    try {
      cfg.content = JSON.stringify(JSON.parse(raw), null, 2)
    } catch {
      cfg.content = raw
    }
    cfgLoaded.value = true
  } catch (e) {
    cfgError.value = e instanceof Error ? e.message : String(e)
  } finally {
    cfgBusy.value = false
  }
}

async function saveConfig() {
  cfgError.value = null
  cfgNote.value = null
  try {
    JSON.parse(cfg.content) // client-side guard before we touch the server
  } catch {
    cfgError.value = 'Config is not valid JSON — fix it before saving.'
    return
  }
  const verb = cfg.restart ? 'Save site config and restart the bench' : 'Save site config'
  if (!window.confirm(`${verb} for ${cfg.site}? The previous config is backed up (.bak).`)) return
  cfgBusy.value = true
  try {
    await writeSiteConfig({
      server_id: cfg.server_id,
      bench_path: cfg.bench_path.trim(),
      site: cfg.site.trim(),
      content: cfg.content,
      restart: cfg.restart,
    })
    cfgNote.value = 'Saved — see the run in History below.'
  } catch (e) {
    cfgError.value = e instanceof Error ? e.message : String(e)
  } finally {
    cfgBusy.value = false
  }
}

// --- history feed ----------------------------------------------------------
const expanded = reactive<Record<number, boolean>>({})
const toggle = (id: number) => (expanded[id] = !expanded[id])

function dur(ms: number): string {
  if (!ms) return ''
  if (ms < 1000) return `${ms}ms`
  const s = ms / 1000
  if (s < 60) return `${s.toFixed(1)}s`
  const m = Math.floor(s / 60)
  return `${m}m ${Math.round(s - m * 60)}s`
}

function rel(iso: string): string {
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return ''
  const d = Math.max(0, Date.now() - t) / 1000
  if (d < 60) return 'just now'
  if (d < 3600) return `${Math.floor(d / 60)}m ago`
  if (d < 86400) return `${Math.floor(d / 3600)}h ago`
  return `${Math.floor(d / 86400)}d ago`
}

const STATUS_ICONS: Record<string, Component> = {
  success: CheckCircle2,
  failed: XCircle,
  running: Loader,
  pending: Clock,
}
const CHIP_CLASSES: Record<string, string> = {
  success: 'chip-ok',
  failed: 'chip-bad',
  running: 'chip-warn',
  pending: 'chip-muted',
}
const statusIcon = (s: string): Component => STATUS_ICONS[s] ?? Clock
const chipClass = (s: string): string => CHIP_CLASSES[s] ?? 'chip-muted'
</script>

<template>
  <section class="control">
    <header class="page-head">
      <div class="title">
        <h2><SlidersHorizontal :size="20" /> Control panel</h2>
        <p class="sub">
          Run allowlisted bench &amp; service commands and edit site config over SSH. Every run is
          recorded below — no arbitrary shell, no site create/rename/drop.
        </p>
      </div>
    </header>

    <p v-if="loadErr" class="err">{{ loadErr }}</p>
    <div v-if="servers.length === 0 && !loadErr" class="empty card-v2">
      <SlidersHorizontal :size="28" />
      <p>No servers registered yet. Add a server first, then run commands against it here.</p>
    </div>

    <div v-else class="grid-2">
      <!-- Run a command --------------------------------------------------- -->
      <div class="panel card-v2">
        <h3><Play :size="16" /> Run a command</h3>
        <div class="form">
          <label class="field">
            Server
            <select v-model.number="runForm.server_id">
              <option v-for="s in servers" :key="s.id" :value="s.id">{{ s.name }} ({{ s.hostname }})</option>
            </select>
          </label>

          <label class="field">
            Action
            <select v-model="runForm.action">
              <optgroup v-if="grouped.server.length" label="Server">
                <option v-for="a in grouped.server" :key="a.key" :value="a.key">
                  {{ a.label }}{{ a.dangerous ? '  ⚠' : '' }}
                </option>
              </optgroup>
              <optgroup v-if="grouped.bench.length" label="Bench">
                <option v-for="a in grouped.bench" :key="a.key" :value="a.key">
                  {{ a.label }}{{ a.dangerous ? '  ⚠' : '' }}
                </option>
              </optgroup>
              <optgroup v-if="grouped.site.length" label="Site">
                <option v-for="a in grouped.site" :key="a.key" :value="a.key">
                  {{ a.label }}{{ a.dangerous ? '  ⚠' : '' }}
                </option>
              </optgroup>
            </select>
          </label>

          <p v-if="selectedDef" class="action-desc">{{ selectedDef.description }}</p>

          <label v-if="needsBench" class="field">
            Bench path
            <input v-model="runForm.bench_path" list="run-bench-paths" placeholder="/home/frappe/frappe-bench" />
            <datalist id="run-bench-paths">
              <option v-for="b in benchOptions" :key="b" :value="b" />
            </datalist>
          </label>

          <label v-if="needsSite" class="field">
            Site
            <input v-model="runForm.site" placeholder="site1.local" />
          </label>

          <div v-if="selectedDef?.dangerous" class="warn">
            <TriangleAlert :size="15" /> Disruptive &amp; long-running — extra confirmation required.
          </div>

          <button class="btn btn-primary run-btn" :disabled="!canRun || running" @click="run">
            <Loader v-if="running" :size="15" class="spin" />
            <Play v-else :size="15" />
            {{ running ? 'Starting…' : 'Run command' }}
          </button>
          <p v-if="runError" class="err">{{ runError }}</p>
        </div>
      </div>

      <!-- Edit site config ------------------------------------------------ -->
      <div class="panel card-v2">
        <h3><FileCog :size="16" /> Edit site config</h3>
        <div class="form">
          <label class="field">
            Server
            <select v-model.number="cfg.server_id">
              <option v-for="s in servers" :key="s.id" :value="s.id">{{ s.name }} ({{ s.hostname }})</option>
            </select>
          </label>
          <div class="row">
            <label class="field grow">
              Bench path
              <input v-model="cfg.bench_path" list="cfg-bench-paths" placeholder="/home/frappe/frappe-bench" />
              <datalist id="cfg-bench-paths">
                <option v-for="b in cfgBenchOptions" :key="b" :value="b" />
              </datalist>
            </label>
            <label class="field grow">
              Site
              <input v-model="cfg.site" placeholder="site1.local" />
            </label>
          </div>
          <button class="btn" :disabled="cfgBusy" @click="loadConfig">
            <RefreshCw :size="14" :class="{ spin: cfgBusy }" /> Load current config
          </button>

          <template v-if="cfgLoaded">
            <textarea v-model="cfg.content" class="config-editor" spellcheck="false" rows="12" />
            <label class="restart-row">
              <input v-model="cfg.restart" type="checkbox" />
              Restart bench after saving (clear-cache + bench restart)
            </label>
            <button class="btn btn-primary" :disabled="cfgBusy" @click="saveConfig">
              <FileCog :size="15" /> Save config
            </button>
          </template>

          <p v-if="cfgError" class="err">{{ cfgError }}</p>
          <p v-if="cfgNote" class="note">{{ cfgNote }}</p>
        </div>
      </div>
    </div>

    <!-- History feed ------------------------------------------------------ -->
    <div class="history">
      <div class="history-head">
        <h3>History</h3>
        <button class="btn btn-sm" :disabled="loading" @click="refresh">
          <RefreshCw :size="14" :class="{ spin: loading }" /> Reload
        </button>
      </div>

      <p v-if="error" class="err">{{ error }}</p>
      <div v-if="history.length === 0 && !loading" class="empty card-v2">
        <Clock :size="26" />
        <p>No commands have been run yet. Anything you run above appears here, live.</p>
      </div>

      <ul class="runs">
        <li v-for="a in history" :key="a.id" class="run card-v2">
          <button class="run-head" @click="toggle(a.id)">
            <span class="chip" :class="chipClass(a.status)">
              <component :is="statusIcon(a.status)" :size="12" :class="{ spin: a.status === 'running' }" />
              {{ a.status }}
            </span>
            <span class="run-title">{{ labelOf(a.action) }}</span>
            <span class="run-meta">
              {{ a.server || '#' + a.server_id }}<template v-if="a.site"> · {{ a.site }}</template>
            </span>
            <span class="run-time">
              {{ rel(a.created_at) }}<template v-if="a.duration_ms"> · {{ dur(a.duration_ms) }}</template>
            </span>
            <ChevronDown class="chev" :class="{ open: expanded[a.id] }" :size="16" />
          </button>
          <div v-if="expanded[a.id]" class="run-body">
            <code v-if="a.command" class="cmd">{{ a.command }}</code>
            <pre v-if="a.output" class="output">{{ a.output }}</pre>
            <p v-if="a.error" class="err">{{ a.error }}</p>
            <p class="byline">
              by {{ a.requested_by || 'operator' }}<template v-if="a.bench_path"> · {{ a.bench_path }}</template>
            </p>
          </div>
        </li>
      </ul>
    </div>
  </section>
</template>

<style scoped>
.page-head { margin-bottom: 1.25rem; }
.page-head h2 { display: inline-flex; align-items: center; gap: 0.5rem; margin: 0; }
.sub { color: var(--muted); margin: 0.35rem 0 0; max-width: 70ch; font-size: 0.9rem; }

.grid-2 { display: grid; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); gap: 1rem; }
.panel { padding: 1.1rem 1.15rem; }
.panel h3 { display: inline-flex; align-items: center; gap: 0.45rem; margin: 0 0 0.9rem; font-size: 0.98rem; }

.form { display: flex; flex-direction: column; gap: 0.75rem; }
.row { display: flex; gap: 0.6rem; }
.grow { flex: 1; min-width: 0; }
.action-desc { color: var(--muted); font-size: 0.84rem; margin: -0.2rem 0 0; line-height: 1.45; }

.warn {
  display: inline-flex; align-items: center; gap: 0.4rem;
  background: var(--status-warning-soft); color: var(--status-warning);
  border: 1px solid color-mix(in srgb, var(--status-warning) 30%, transparent);
  padding: 0.45rem 0.6rem; border-radius: var(--radius-sm); font-size: 0.82rem;
}
.run-btn { margin-top: 0.15rem; align-self: flex-start; }

.config-editor {
  width: 100%;
  background: var(--bg);
  color: var(--fg);
  border: 1px solid var(--card-border-strong);
  border-radius: var(--radius-sm);
  padding: 0.65rem 0.75rem;
  font-family: "JetBrains Mono", ui-monospace, "SF Mono", Menlo, monospace;
  font-size: 0.82rem;
  line-height: 1.5;
  resize: vertical;
}
.config-editor:focus { outline: none; border-color: var(--accent); box-shadow: var(--ring); }
.restart-row { display: flex; align-items: center; gap: 0.5rem; font-size: 0.86rem; color: var(--muted); }

.note { color: var(--status-reachable); font-size: 0.85rem; margin: 0; }
.err { color: var(--status-unreachable); font-size: 0.85rem; margin: 0; word-break: break-word; }

/* --- history --- */
.history { margin-top: 1.75rem; }
.history-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 0.75rem; }
.history-head h3 { margin: 0; }
.runs { list-style: none; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 0.5rem; }
.run { overflow: hidden; }
.run-head {
  display: flex; align-items: center; gap: 0.75rem; width: 100%;
  background: transparent; border: none; color: var(--fg);
  padding: 0.7rem 0.85rem; cursor: pointer; text-align: left; font: inherit;
}
.run-head:hover { background: var(--bg-hover); }
.run-title { font-weight: 600; font-size: 0.9rem; flex-shrink: 0; }
.run-meta { color: var(--muted); font-size: 0.84rem; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.run-time { margin-left: auto; color: var(--muted-2); font-size: 0.78rem; white-space: nowrap; flex-shrink: 0; }
.chev { color: var(--muted-2); transition: transform var(--transition); flex-shrink: 0; }
.chev.open { transform: rotate(180deg); }
.chip svg { flex-shrink: 0; }

.run-body { padding: 0.25rem 0.85rem 0.85rem; border-top: 1px solid var(--card-border); display: flex; flex-direction: column; gap: 0.55rem; }
.cmd { display: block; background: var(--bg); border: 1px solid var(--card-border); border-radius: var(--radius-sm); padding: 0.45rem 0.6rem; font-size: 0.8rem; margin-top: 0.55rem; }
.output {
  background: var(--bg); border: 1px solid var(--card-border); border-radius: var(--radius-sm);
  padding: 0.6rem 0.7rem; margin: 0; max-height: 360px; overflow: auto;
  font-family: "JetBrains Mono", ui-monospace, "SF Mono", Menlo, monospace;
  font-size: 0.78rem; line-height: 1.5; white-space: pre-wrap; word-break: break-word; color: var(--fg);
}
.byline { color: var(--muted-2); font-size: 0.78rem; margin: 0; word-break: break-word; }

@media (max-width: 560px) {
  .run-meta { display: none; }
  .row { flex-direction: column; }
}
</style>
