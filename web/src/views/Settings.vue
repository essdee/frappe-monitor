<script setup lang="ts">
import { onMounted, ref } from 'vue'
import {
  Activity,
  BellRing,
  CheckCircle2,
  Database,
  FileText,
  KeyRound,
  Plus,
  RefreshCw,
  Save,
  ServerCog,
  Settings2,
  ShieldCheck,
  Trash2,
  Wifi,
} from 'lucide-vue-next'
import {
  fetchSettings,
  fetchRuntimeStatus,
  saveSettings,
  testIntegration as runIntegrationTest,
  type AlertRule,
  type IntegrationTestResult,
  type SettingsResponse,
  type SettingsUpdate,
  type PublicProbeTargetSettings,
  type RuntimeStatus,
  type StreamingFileSettings,
} from '../api'
import { displayTimezone, formatDateTime } from '../timeDisplay'

const settings = ref<SettingsResponse | null>(null)
const runtimeStatus = ref<RuntimeStatus | null>(null)
const runtimeError = ref('')
const loading = ref(true)
const saving = ref(false)
const error = ref('')
const saved = ref(false)
const trustedProxies = ref('')
const telegramChatIDs = ref('')
const newPassword = ref('')
const clearPassword = ref(false)
const newBotToken = ref('')
const clearBotToken = ref(false)
const testResults = ref<Record<string, IntegrationTestResult | undefined>>({})
const testing = ref<Record<string, boolean>>({})

function errorText(value: unknown): string {
  return value instanceof Error ? value.message : String(value)
}

function syncTextFields(value: SettingsResponse) {
  trustedProxies.value = value.server.trusted_proxy_cidrs.join('\n')
  telegramChatIDs.value = value.alerts.telegram.chat_ids.join('\n')
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    settings.value = await fetchSettings()
    syncTextFields(settings.value)
    runtimeError.value = ''
    try {
      runtimeStatus.value = await fetchRuntimeStatus()
    } catch (e) {
      runtimeError.value = errorText(e)
    }
  } catch (e) {
    error.value = errorText(e)
  } finally {
    loading.value = false
  }
}

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB']
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1)
  return `${(value / 1024 ** index).toFixed(index === 0 ? 0 : 1)} ${units[index]}`
}

function formatUptime(seconds: number): string {
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  if (days) return `${days}d ${hours}h`
  if (hours) return `${hours}h ${minutes}m`
  return `${minutes}m`
}

onMounted(load)

function lines(value: string): string[] {
  return value.split('\n').map((item) => item.trim()).filter(Boolean)
}

function updatePayload(value: SettingsResponse): SettingsUpdate {
  const auth: SettingsUpdate['auth'] = { realm: value.auth.realm }
  if (clearPassword.value) auth.password = ''
  else if (newPassword.value) auth.password = newPassword.value

  const telegram: SettingsUpdate['alerts']['telegram'] = {
    chat_ids: lines(telegramChatIDs.value),
    send_timeout_seconds: value.alerts.telegram.send_timeout_seconds,
  }
  if (clearBotToken.value) telegram.bot_token = ''
  else if (newBotToken.value) telegram.bot_token = newBotToken.value

  return {
    server: { ...value.server, trusted_proxy_cidrs: lines(trustedProxies.value) },
    display: { ...value.display },
    database: { ...value.database },
    ssh: { ...value.ssh },
    log: { ...value.log },
    metrics: { ...value.metrics },
    logs: { ...value.logs },
    scheduler: { ...value.scheduler },
    public_probes: { ...value.public_probes, targets: value.public_probes.targets.map((target) => ({ ...target })) },
    auth,
    alerts: {
      enabled: value.alerts.enabled,
      evaluation_interval_seconds: value.alerts.evaluation_interval_seconds,
      notify_repeat_seconds: value.alerts.notify_repeat_seconds,
      vm_query_timeout_seconds: value.alerts.vm_query_timeout_seconds,
      telegram,
      rules: value.alerts.rules.map((rule) => ({ ...rule, fingerprint_labels: [...(rule.fingerprint_labels ?? [])] })),
      disable_defaults: value.alerts.disable_defaults,
    },
    streaming: { ...value.streaming, files: value.streaming.files.map((file) => ({ ...file })) },
  }
}

async function save() {
  if (!settings.value) return
  saving.value = true
  saved.value = false
  error.value = ''
  try {
    settings.value = await saveSettings(updatePayload(settings.value))
    syncTextFields(settings.value)
    newPassword.value = ''
    clearPassword.value = false
    newBotToken.value = ''
    clearBotToken.value = false
    saved.value = true
  } catch (e) {
    error.value = errorText(e)
    window.scrollTo({ top: 0, behavior: 'smooth' })
  } finally {
    saving.value = false
  }
}

async function test(kind: 'metrics' | 'logs' | 'telegram') {
  testing.value[kind] = true
  testResults.value[kind] = undefined
  try {
    testResults.value[kind] = await runIntegrationTest(kind, kind === 'telegram' ? newBotToken.value || undefined : undefined)
  } catch (e) {
    testResults.value[kind] = { ok: false, latency_ms: 0, message: errorText(e) }
  } finally {
    testing.value[kind] = false
  }
}

function addRule() {
  settings.value?.alerts.rules.push({ name: '', expr: '', severity: 'warning', message: '', fingerprint_labels: [] })
}

function setFingerprintLabels(rule: AlertRule, event: Event) {
  rule.fingerprint_labels = (event.target as HTMLInputElement).value.split(',').map((label) => label.trim()).filter(Boolean)
}

function addStreamFile() {
  const file: StreamingFileSettings = { server_id: 0, id: '', path: '', log_type: 'frappe', bench: '', site: '' }
  settings.value?.streaming.files.push(file)
}

function addPublicProbeTarget() {
  const target: PublicProbeTargetSettings = { server: '', bench: '', site: '', url: '', disabled: false }
  settings.value?.public_probes.targets.push(target)
}

function scrollToSection(id: string) {
  document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}
</script>

<template>
  <section class="page settings-page">
    <header class="page-heading">
      <div><span class="eyebrow">Monitor administration</span><h1>Settings</h1><p>Configure the complete monitoring control plane from the UI. These changes affect only Frappe Monitor; target servers stay read-only.</p></div>
      <div class="page-actions"><button class="button" type="button" :disabled="loading" @click="load"><RefreshCw :size="15" :class="{ spin: loading }" />Reload</button><button class="button primary" type="button" :disabled="saving || loading || !settings" @click="save"><Save :size="16" />{{ saving ? 'Saving…' : 'Save changes' }}</button></div>
    </header>

    <div v-if="error" class="notice error">{{ error }}</div>
    <div v-if="saved" class="notice success"><CheckCircle2 :size="17" />Configuration saved safely. A backup of the previous YAML was created.</div>
    <div v-if="settings?.restart_required" class="restart-banner"><RefreshCw :size="18" /><div><strong>Restart required</strong><span>Saved settings are ready. Run <code>sudo systemctl restart frappe-monitor</code> to apply them to long-running collectors and HTTP services.</span></div></div>

    <div v-if="loading" class="panel empty-state"><RefreshCw class="spin" :size="26" /><h3>Loading configuration</h3></div>

    <div v-else-if="settings" class="settings-layout">
      <aside class="settings-nav panel" aria-label="Settings sections">
        <button type="button" @click="scrollToSection('general')"><Settings2 :size="16" />General</button>
        <button type="button" @click="scrollToSection('backends')"><Database :size="16" />Data backends</button>
        <button type="button" @click="scrollToSection('collection')"><ServerCog :size="16" />Collection</button>
        <button type="button" @click="scrollToSection('notifications')"><BellRing :size="16" />Alerts</button>
        <button type="button" @click="scrollToSection('streaming')"><FileText :size="16" />Log streaming</button>
      </aside>

      <form class="settings-content" @submit.prevent="save">
        <section id="general" class="settings-section">
          <div class="section-title"><span class="section-icon"><Settings2 :size="18" /></span><div><h2>General</h2><p>HTTP server, logging, proxy trust, and console access.</p></div></div>
          <div class="panel settings-card">
            <div class="panel-header"><div><h3 class="panel-title"><Activity :size="17" />Monitor runtime</h3><p>Live process and ingestion state from this monitor, independent of the metrics backend.</p></div></div>
            <div v-if="runtimeError" class="panel-body inline-empty">Runtime status is unavailable: {{ runtimeError }}</div>
            <div v-else-if="runtimeStatus" class="panel-body runtime-grid">
              <div class="runtime-stat"><span>Uptime</span><strong>{{ formatUptime(runtimeStatus.uptime_seconds) }}</strong><small>Started {{ formatDateTime(runtimeStatus.started_at) }}</small></div>
              <div class="runtime-stat"><span>Go heap</span><strong>{{ formatBytes(runtimeStatus.heap_alloc_bytes) }}</strong><small>{{ runtimeStatus.goroutines }} goroutines · {{ formatBytes(runtimeStatus.heap_sys_bytes) }} reserved</small></div>
              <div class="runtime-stat"><span>Log sessions</span><strong>{{ runtimeStatus.streaming.enabled ? `${runtimeStatus.streaming.connected}/${runtimeStatus.streaming.sessions}` : 'Disabled' }}</strong><small v-if="runtimeStatus.streaming.enabled">{{ runtimeStatus.streaming.launching }} launching</small><small v-else>Enable in Log streaming below</small></div>
              <div class="runtime-stat"><span>Stream buffer</span><strong>{{ runtimeStatus.streaming.buffered_lines }} / {{ runtimeStatus.streaming.max_buffered_lines }}</strong><small>{{ formatBytes(runtimeStatus.streaming.buffered_bytes) }} / {{ formatBytes(runtimeStatus.streaming.max_buffered_bytes) }}</small></div>
            </div>
          </div>
          <div class="panel settings-card">
            <div class="panel-header"><h3 class="panel-title">Web service</h3></div>
            <div class="panel-body field-grid three">
              <label class="field"><span>Listen address</span><input v-model="settings.server.listen_addr" required /><small>Example: <code>:8080</code> or <code>127.0.0.1:8080</code></small></label>
              <label class="field"><span>Read timeout (seconds)</span><input v-model.number="settings.server.read_timeout_seconds" type="number" min="1" required /></label>
              <label class="field"><span>Write timeout (seconds)</span><input v-model.number="settings.server.write_timeout_seconds" type="number" min="1" required /></label>
              <label class="field span-all"><span>Trusted proxy CIDRs</span><textarea v-model="trustedProxies" rows="3" placeholder="127.0.0.1/32&#10;::1/128" /><small>One CIDR per line. Only these proxies may supply forwarded client IP headers.</small></label>
            </div>
          </div>
          <div class="panel settings-card">
            <div class="panel-header"><h3 class="panel-title">Application logging</h3></div>
            <div class="panel-body field-grid">
              <label class="field"><span>Log level</span><select v-model="settings.log.level"><option value="debug">Debug</option><option value="info">Info</option><option value="warn">Warning</option><option value="error">Error</option></select></label>
              <label class="field"><span>Log format</span><select v-model="settings.log.format"><option value="json">JSON</option><option value="text">Text</option></select></label>
            </div>
          </div>
          <div class="panel settings-card">
            <div class="panel-header"><div><h3 class="panel-title">Time and date</h3><p>All stored timestamps stay in UTC; this controls dashboard and Telegram presentation.</p></div></div>
            <div class="panel-body field-grid">
              <label class="field"><span>Display timezone</span><input v-model="settings.display.timezone" list="timezone-options" required placeholder="Asia/Kolkata" /><datalist id="timezone-options"><option value="UTC" /><option value="Asia/Kolkata" /><option value="Asia/Dubai" /><option value="Asia/Singapore" /><option value="Europe/London" /><option value="America/New_York" /></datalist><small>Use an IANA timezone. Active now: <code>{{ displayTimezone }}</code>. A restart applies changes everywhere.</small></label>
            </div>
          </div>
          <div class="panel settings-card">
            <div class="panel-header"><div><h3 class="panel-title"><Database :size="17" />Local state</h3><p>SQLite stores server registrations, stream cursors, and incident history.</p></div></div>
            <div class="panel-body"><label class="field"><span>Database path</span><input v-model="settings.database.path" required /><small>Changing this path starts with a different local registry after restart. Existing data is not moved automatically.</small></label></div>
          </div>
          <div class="panel settings-card">
            <div class="panel-header"><div><h3 class="panel-title"><ShieldCheck :size="17" />Console security</h3><p>Secrets are never returned to the browser. Leave a secret field blank to keep its current value.</p></div></div>
            <div class="panel-body field-grid">
              <label class="field"><span>Authentication realm</span><input v-model="settings.auth.realm" required /></label>
              <label class="field"><span>New console password</span><input v-model="newPassword" type="password" autocomplete="new-password" :placeholder="settings.auth.password_configured ? 'Configured — leave blank to keep' : 'Set a password to enable sign-in'" :disabled="clearPassword" /><small>{{ settings.auth.password_configured ? 'A password is currently configured.' : 'Authentication is currently disabled.' }}</small></label>
              <label class="check-row span-all"><input v-model="clearPassword" type="checkbox" /><span>Clear the console password and disable authentication after restart</span></label>
            </div>
          </div>
        </section>

        <section id="backends" class="settings-section">
          <div class="section-title"><span class="section-icon"><Database :size="18" /></span><div><h2>Data backends</h2><p>VictoriaMetrics stores measurements; Loki stores searchable log lines.</p></div></div>
          <div class="panel settings-card">
            <div class="panel-header"><div><h3 class="panel-title">VictoriaMetrics</h3><p>Metrics push and dashboard query endpoint.</p></div><button class="button" type="button" :disabled="testing.metrics" @click="test('metrics')"><Wifi :size="15" />{{ testing.metrics ? 'Testing…' : 'Test' }}</button></div>
            <div class="panel-body field-grid three"><label class="field span-all"><span>Base URL</span><input v-model="settings.metrics.vm_url" type="url" required /></label><label class="field"><span>Push timeout</span><input v-model.number="settings.metrics.push_timeout_seconds" type="number" min="1" required /></label><label class="field"><span>Query timeout</span><input v-model.number="settings.metrics.query_timeout_seconds" type="number" min="1" required /></label></div>
            <div v-if="testResults.metrics" class="test-result" :class="testResults.metrics.ok ? 'ok' : 'failed'">{{ testResults.metrics.message }}<span v-if="testResults.metrics.latency_ms"> · {{ testResults.metrics.latency_ms }} ms</span></div>
          </div>
          <div class="panel settings-card">
            <div class="panel-header"><div><h3 class="panel-title">Loki</h3><p>Log ingestion and LogQL query endpoint.</p></div><button class="button" type="button" :disabled="testing.logs" @click="test('logs')"><Wifi :size="15" />{{ testing.logs ? 'Testing…' : 'Test' }}</button></div>
            <div class="panel-body field-grid three"><label class="field span-all"><span>Base URL</span><input v-model="settings.logs.loki_url" type="url" required /></label><label class="field"><span>Push timeout</span><input v-model.number="settings.logs.push_timeout_seconds" type="number" min="1" required /></label><label class="field"><span>Query timeout</span><input v-model.number="settings.logs.query_timeout_seconds" type="number" min="1" required /></label></div>
            <div v-if="testResults.logs" class="test-result" :class="testResults.logs.ok ? 'ok' : 'failed'">{{ testResults.logs.message }}<span v-if="testResults.logs.latency_ms"> · {{ testResults.logs.latency_ms }} ms</span></div>
          </div>
        </section>

        <section id="collection" class="settings-section">
          <div class="section-title"><span class="section-icon"><ServerCog :size="18" /></span><div><h2>Collection</h2><p>Control SSH safety limits, cadence, and fleet-wide concurrency.</p></div></div>
          <div class="panel settings-card">
            <div class="panel-header"><h3 class="panel-title">Schedule</h3></div>
            <div class="panel-body field-grid three"><label class="field"><span>Interval (seconds)</span><input v-model.number="settings.scheduler.default_interval_seconds" type="number" min="1" required /><small>How often every server is inspected.</small></label><label class="field"><span>Parallel servers</span><input v-model.number="settings.scheduler.max_parallel" type="number" min="1" required /></label><label class="field"><span>Job timeout (seconds)</span><input v-model.number="settings.scheduler.per_job_timeout_seconds" type="number" min="1" required /></label></div>
          </div>
          <div class="panel settings-card">
            <div class="panel-header"><h3 class="panel-title"><KeyRound :size="17" />SSH policy</h3></div>
            <div class="panel-body field-grid three"><label class="field"><span>Dial timeout</span><input v-model.number="settings.ssh.dial_timeout_seconds" type="number" min="1" required /></label><label class="field"><span>Command timeout</span><input v-model.number="settings.ssh.command_timeout_seconds" type="number" min="1" required /></label><label class="field"><span>Connections per host</span><input v-model.number="settings.ssh.max_connections_per_host" type="number" min="1" required /></label><label class="field span-all"><span>Known hosts file</span><input v-model="settings.ssh.known_hosts_path" required /><small>Strict host-key verification is enforced through this file.</small></label></div>
          </div>
          <div class="panel settings-card">
            <div class="panel-header"><div><h3 class="panel-title"><Wifi :size="17" />Public site probes</h3><p>Check DNS, TLS, reverse proxy routing, and Frappe from this monitor. SSH localhost probes remain active as the internal signal.</p></div><label class="switch"><input v-model="settings.public_probes.enabled" type="checkbox" /><span></span><b>{{ settings.public_probes.enabled ? 'Enabled' : 'Disabled' }}</b></label></div>
            <div class="panel-body field-grid three">
              <label class="field"><span>Default scheme</span><select v-model="settings.public_probes.default_scheme"><option value="https">HTTPS</option><option value="http">HTTP</option></select></label>
              <label class="field"><span>Probe path</span><input v-model="settings.public_probes.path" required placeholder="/api/method/ping" /></label>
              <label class="field"><span>Timeout (seconds)</span><input v-model.number="settings.public_probes.timeout_seconds" type="number" min="1" max="60" required /></label>
              <label class="field"><span>Parallel probes</span><input v-model.number="settings.public_probes.max_parallel" type="number" min="1" max="100" required /></label>
              <label class="check-row"><input v-model="settings.public_probes.auto_discover" type="checkbox" /><span>Automatically probe every discovered site name</span></label>
              <label class="check-row"><input v-model="settings.public_probes.require_pong" type="checkbox" /><span>Require Frappe's JSON <code>pong</code> response</span></label>
              <label class="check-row span-all"><input v-model="settings.public_probes.allow_private_targets" type="checkbox" /><span>Allow private and loopback targets. Keep this disabled unless private DNS is intentional.</span></label>
            </div>
            <div class="panel-header probe-overrides"><div><h3 class="panel-title">Per-site overrides</h3><p>Override a discovered site's URL or disable its public check.</p></div><button class="button" type="button" @click="addPublicProbeTarget"><Plus :size="15" />Add override</button></div>
            <div class="panel-body file-list">
              <div v-if="!settings.public_probes.targets.length" class="inline-empty">No overrides. Discovered site names use the defaults above.</div>
              <article v-for="(target, index) in settings.public_probes.targets" :key="index" class="file-editor">
                <div class="probe-editor-grid"><label class="field"><span>Server</span><input v-model="target.server" required placeholder="Production" /></label><label class="field"><span>Bench</span><input v-model="target.bench" required placeholder="frappe-bench" /></label><label class="field"><span>Site</span><input v-model="target.site" required placeholder="erp.example.com" /></label><label class="field probe-url"><span>Public URL</span><input v-model="target.url" type="url" :required="!target.disabled" :disabled="target.disabled" placeholder="https://erp.example.com/api/method/ping" /></label><label class="check-row probe-disabled"><input v-model="target.disabled" type="checkbox" /><span>Disabled</span></label><button class="button icon-button ghost file-remove" type="button" aria-label="Remove public probe override" @click="settings.public_probes.targets.splice(index, 1)"><Trash2 :size="15" /></button></div>
              </article>
            </div>
          </div>
        </section>

        <section id="notifications" class="settings-section">
          <div class="section-title"><span class="section-icon"><BellRing :size="18" /></span><div><h2>Alerts and Telegram</h2><p>Evaluate PromQL rules, retain incidents, and notify one or more Telegram chats.</p></div></div>
          <div class="panel settings-card">
            <div class="panel-header"><div><h3 class="panel-title">Alert engine</h3><p>{{ settings.alerts.bundled_rule_count }} production rules are bundled with the monitor.</p></div><label class="switch"><input v-model="settings.alerts.enabled" type="checkbox" /><span></span><b>{{ settings.alerts.enabled ? 'Enabled' : 'Disabled' }}</b></label></div>
            <div class="panel-body field-grid three"><label class="field"><span>Evaluation interval</span><input v-model.number="settings.alerts.evaluation_interval_seconds" type="number" min="15" required /></label><label class="field"><span>Repeat notification</span><input v-model.number="settings.alerts.notify_repeat_seconds" type="number" min="0" required /><small>0 sends only fire and recovery.</small></label><label class="field"><span>Query timeout</span><input v-model.number="settings.alerts.vm_query_timeout_seconds" type="number" min="1" required /></label><label class="check-row span-all"><input v-model="settings.alerts.disable_defaults" type="checkbox" /><span>Disable all bundled rules and use only the custom rules below</span></label></div>
          </div>
          <div class="panel settings-card">
            <div class="panel-header"><div><h3 class="panel-title">Telegram delivery</h3><p>Use a bot token from BotFather and numeric chat or group IDs.</p></div><button class="button" type="button" :disabled="testing.telegram" @click="test('telegram')"><Wifi :size="15" />{{ testing.telegram ? 'Testing…' : 'Test bot' }}</button></div>
            <div class="panel-body field-grid"><label class="field"><span>Bot token</span><input v-model="newBotToken" type="password" autocomplete="off" :placeholder="settings.alerts.telegram.bot_token_configured ? 'Configured — leave blank to keep' : '123456:ABC…'" :disabled="clearBotToken" /><small>{{ settings.alerts.telegram.bot_token_configured ? 'A token is currently configured.' : 'No token is configured.' }}</small></label><label class="field"><span>Send timeout (seconds)</span><input v-model.number="settings.alerts.telegram.send_timeout_seconds" type="number" min="1" required /></label><label class="field span-all"><span>Chat IDs</span><textarea v-model="telegramChatIDs" rows="3" placeholder="-1001234567890" /><small>One destination per line. Every firing and recovery message is sent to each destination.</small></label><label class="check-row span-all"><input v-model="clearBotToken" type="checkbox" /><span>Clear the stored bot token</span></label></div>
            <div v-if="testResults.telegram" class="test-result" :class="testResults.telegram.ok ? 'ok' : 'failed'">{{ testResults.telegram.message }}<span v-if="testResults.telegram.latency_ms"> · {{ testResults.telegram.latency_ms }} ms</span></div>
          </div>
          <div class="panel settings-card">
            <div class="panel-header"><div><h3 class="panel-title">Custom PromQL rules</h3><p>Override a bundled rule by using the same name, or add your own.</p></div><button class="button" type="button" @click="addRule"><Plus :size="15" />Add rule</button></div>
            <div class="panel-body rule-list">
              <div v-if="!settings.alerts.rules.length" class="inline-empty">No custom rules. Bundled rules provide the default monitoring policy.</div>
              <article v-for="(rule, index) in settings.alerts.rules" :key="index" class="rule-editor">
                <div class="rule-heading"><strong>{{ rule.name || `New rule ${index + 1}` }}</strong><button class="button icon-button ghost" type="button" aria-label="Remove rule" @click="settings.alerts.rules.splice(index, 1)"><Trash2 :size="15" /></button></div>
                <div class="field-grid"><label class="field"><span>Rule name</span><input v-model="rule.name" required placeholder="site_unhealthy" /></label><label class="field"><span>Severity</span><select v-model="rule.severity"><option value="critical">Critical</option><option value="warning">Warning</option><option value="info">Info</option></select></label><label class="field span-all"><span>PromQL expression</span><textarea v-model="rule.expr" rows="3" required class="mono" /></label><label class="field span-all"><span>Notification message template</span><textarea v-model="rule.message" rows="2" :placeholder="'Site {{.Labels.site}} is unhealthy.'" /></label><label class="field span-all"><span>Fingerprint labels</span><input :value="(rule.fingerprint_labels ?? []).join(', ')" placeholder="server, bench, site" @input="setFingerprintLabels(rule, $event)" /><small>Comma-separated. Empty uses every returned series label.</small></label></div>
              </article>
            </div>
          </div>
        </section>

        <section id="streaming" class="settings-section">
          <div class="section-title"><span class="section-icon"><FileText :size="18" /></span><div><h2>Live log streaming</h2><p>Tail Frappe, worker, web, database, and system logs over resumable read-only SSH sessions.</p></div></div>
          <div class="panel settings-card">
            <div class="panel-header"><div><h3 class="panel-title">Streamer</h3><p>Standard log locations are auto-discovered. File entries below are optional overrides.</p></div><label class="switch"><input v-model="settings.streaming.enabled" type="checkbox" /><span></span><b>{{ settings.streaming.enabled ? 'Enabled' : 'Disabled' }}</b></label></div>
            <div class="panel-body field-grid three"><label class="field span-all"><span>Monitor ID</span><input v-model="settings.streaming.monitor_id" placeholder="primary-monitor" /><small>Added as a Loki label when multiple monitors share a backend.</small></label><label class="field"><span>Flush interval</span><input v-model.number="settings.streaming.flush_interval_seconds" type="number" min="1" required /></label><label class="field"><span>Maximum batch lines</span><input v-model.number="settings.streaming.max_batch_lines" type="number" min="1" required /></label><label class="field"><span>Global buffered lines</span><input v-model.number="settings.streaming.max_buffered_lines" type="number" :min="settings.streaming.max_batch_lines" required /><small>Hard cap across every log file.</small></label><label class="field"><span>Global buffered bytes</span><input v-model.number="settings.streaming.max_buffered_bytes" type="number" min="1048576" step="1048576" required /><small>Hard memory-payload cap; default 33554432 (32 MiB).</small></label><label class="field"><span>Push timeout</span><input v-model.number="settings.streaming.push_timeout_seconds" type="number" min="1" required /></label><label class="field"><span>Minimum reconnect backoff</span><input v-model.number="settings.streaming.min_backoff_seconds" type="number" min="1" required /></label><label class="field"><span>Maximum reconnect backoff</span><input v-model.number="settings.streaming.max_backoff_seconds" type="number" min="1" required /></label></div>
          </div>
          <div class="panel settings-card">
            <div class="panel-header"><div><h3 class="panel-title">Manual file overrides</h3><p>Server ID 0 applies an entry to every registered target.</p></div><button class="button" type="button" @click="addStreamFile"><Plus :size="15" />Add file</button></div>
            <div class="panel-body file-list">
              <div v-if="!settings.streaming.files.length" class="inline-empty">No overrides. Standard Frappe paths will be discovered automatically.</div>
              <article v-for="(file, index) in settings.streaming.files" :key="index" class="file-editor">
                <div class="file-editor-grid"><label class="field"><span>Server ID</span><input v-model.number="file.server_id" type="number" min="0" /></label><label class="field"><span>Stream ID</span><input v-model="file.id" required placeholder="web-error" /></label><label class="field file-path"><span>Absolute log path</span><input v-model="file.path" required placeholder="/home/frappe/frappe-bench/logs/web.error.log" /></label><label class="field"><span>Log type</span><input v-model="file.log_type" placeholder="frappe" /></label><label class="field"><span>Bench label</span><input v-model="file.bench" /></label><label class="field"><span>Site label</span><input v-model="file.site" /></label><button class="button icon-button ghost file-remove" type="button" aria-label="Remove file" @click="settings.streaming.files.splice(index, 1)"><Trash2 :size="15" /></button></div>
              </article>
            </div>
          </div>
        </section>

        <div class="save-bar panel"><div><strong>Ready to apply?</strong><span>Settings are validated and saved atomically with a rollback copy.</span></div><button class="button primary" type="submit" :disabled="saving"><Save :size="16" />{{ saving ? 'Saving…' : 'Save all settings' }}</button></div>
      </form>
    </div>
  </section>
</template>

<style scoped>
.settings-page{max-width:1320px;margin:0 auto}.restart-banner{display:flex;gap:.75rem;align-items:flex-start;margin:.8rem 0 1.25rem;padding:.9rem 1rem;border:1px solid color-mix(in srgb,var(--status-warning) 35%,var(--card-border));border-radius:11px;color:var(--status-warning);background:color-mix(in srgb,var(--status-warning) 6%,var(--card-bg))}.restart-banner div{display:flex;flex-direction:column;gap:.24rem}.restart-banner span{color:#d8bd91;font-size:.79rem;line-height:1.5}.settings-layout{display:grid;grid-template-columns:210px minmax(0,1fr);gap:1rem;align-items:start}.settings-nav{position:sticky;top:90px;padding:.45rem;display:flex;flex-direction:column;gap:.15rem}.settings-nav button{display:flex;align-items:center;gap:.55rem;width:100%;padding:.65rem;border:0;border-radius:8px;background:transparent;color:var(--muted);font-size:.78rem;text-align:left;cursor:pointer}.settings-nav button:hover{color:var(--fg);background:var(--bg-hover)}.settings-content{min-width:0}.settings-section{scroll-margin-top:90px;margin-bottom:2rem}.section-title{display:flex;align-items:center;gap:.7rem;margin-bottom:.7rem}.section-title h2{margin:0;color:var(--fg-strong);font-size:1.08rem}.section-title p{margin:.15rem 0 0;color:var(--muted);font-size:.76rem}.section-icon{width:38px;height:38px;display:grid;place-items:center;flex:0 0 auto;color:var(--accent-strong);border-radius:10px;background:var(--accent-soft)}.settings-card{overflow:hidden;margin-bottom:.7rem}.panel-header p{margin:.2rem 0 0;color:var(--muted);font-size:.73rem;line-height:1.4}.runtime-grid{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:.65rem}.runtime-stat{min-width:0;padding:.7rem;border:1px solid var(--card-border);border-radius:9px;background:color-mix(in srgb,var(--bg) 74%,transparent);display:flex;flex-direction:column;gap:.25rem}.runtime-stat span{color:var(--muted);font-size:.68rem;text-transform:uppercase;letter-spacing:.06em}.runtime-stat strong{color:var(--fg-strong);font-size:1rem}.runtime-stat small{color:var(--muted-2);font-size:.68rem;overflow-wrap:anywhere}.field-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:1rem}.field-grid.three{grid-template-columns:repeat(3,minmax(0,1fr))}.span-all{grid-column:1/-1}.field{display:flex;flex-direction:column;gap:.38rem;min-width:0}.field>span{color:var(--fg);font-size:.76rem;font-weight:650}.field small{color:var(--muted-2);font-size:.69rem;line-height:1.45}.field input,.field textarea,.field select{width:100%;min-height:39px;padding:.56rem .65rem;border:1px solid var(--card-border-strong);border-radius:8px;outline:0;background:var(--bg)}.field textarea{resize:vertical;line-height:1.45}.field input:focus,.field textarea:focus,.field select:focus{border-color:var(--accent)}.check-row{display:flex;align-items:flex-start;gap:.55rem;color:var(--muted);font-size:.76rem;line-height:1.4}.check-row input{margin-top:.12rem;accent-color:var(--accent)}.switch{display:flex;align-items:center;gap:.5rem;cursor:pointer;color:var(--muted);font-size:.72rem}.switch input{position:absolute;opacity:0;pointer-events:none}.switch span{width:36px;height:20px;padding:2px;display:block;border-radius:999px;background:var(--muted-2);transition:background .15s}.switch span::after{content:'';display:block;width:16px;height:16px;border-radius:50%;background:white;transition:transform .15s}.switch input:checked+span{background:var(--accent)}.switch input:checked+span::after{transform:translateX(16px)}.switch b{font-weight:650}.test-result{margin:0 1.1rem 1rem;padding:.55rem .65rem;border-radius:7px;font-size:.73rem}.test-result.ok{color:#7de8b9;background:color-mix(in srgb,var(--status-reachable) 7%,var(--bg))}.test-result.failed{color:#ff9aaa;background:color-mix(in srgb,var(--status-unreachable) 7%,var(--bg))}.inline-empty{padding:.4rem 0;color:var(--muted-2);font-size:.78rem}.rule-list,.file-list{display:grid;gap:.65rem}.rule-editor,.file-editor{padding:.8rem;border:1px solid var(--card-border);border-radius:10px;background:color-mix(in srgb,var(--bg) 76%,transparent)}.rule-heading{display:flex;align-items:center;justify-content:space-between;margin-bottom:.65rem;color:var(--fg-strong);font-size:.8rem}.file-editor-grid{display:grid;grid-template-columns:110px minmax(120px,1fr) minmax(200px,2fr) repeat(3,minmax(100px,1fr)) 40px;align-items:end;gap:.55rem}.probe-overrides{border-top:1px solid var(--card-border)}.probe-editor-grid{display:grid;grid-template-columns:repeat(3,minmax(110px,1fr)) minmax(250px,2fr) 90px 40px;align-items:end;gap:.55rem}.probe-disabled{align-self:center}.file-remove{margin-bottom:0}.save-bar{position:sticky;bottom:1rem;z-index:10;display:flex;align-items:center;justify-content:space-between;gap:1rem;padding:.8rem 1rem;border-color:var(--card-border-strong);background:color-mix(in srgb,var(--card-bg) 92%,transparent);backdrop-filter:blur(16px)}.save-bar div{display:flex;flex-direction:column;gap:.15rem}.save-bar strong{font-size:.8rem}.save-bar span{color:var(--muted);font-size:.7rem}@media(max-width:1100px){.runtime-grid{grid-template-columns:repeat(2,minmax(0,1fr))}.file-editor-grid,.probe-editor-grid{grid-template-columns:repeat(3,minmax(0,1fr))}.file-path,.probe-url{grid-column:span 2}.file-remove{grid-column:3;grid-row:1}}@media(max-width:900px){.settings-layout{grid-template-columns:1fr}.settings-nav{position:static;flex-direction:row;overflow-x:auto}.settings-nav button{width:auto;white-space:nowrap}.settings-section{scroll-margin-top:12px}.field-grid.three{grid-template-columns:repeat(2,minmax(0,1fr))}}@media(max-width:620px){.runtime-grid,.field-grid,.field-grid.three{grid-template-columns:1fr}.panel-header{align-items:flex-start;flex-direction:column}.panel-header>.button{width:100%}.file-editor-grid,.probe-editor-grid{grid-template-columns:1fr 1fr}.file-path,.probe-url{grid-column:1/-1}.file-remove{grid-column:2;grid-row:1;justify-self:end}.save-bar{bottom:76px;padding:.55rem}.save-bar>div{display:none}.save-bar .button{width:100%}.settings-nav{margin:0 -.2rem}.settings-nav button{padding:.55rem}.settings-nav button svg{display:none}}
</style>
