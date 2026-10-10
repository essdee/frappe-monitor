<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import { CheckCircle2, Plus, Server, Trash2, Wifi, X } from 'lucide-vue-next'
import {
  createServer,
  deleteServer,
  fetchServer,
  patchServer,
  testDraftServerConnection,
  type NewServerInput,
  type TestConnectionResult,
} from '../api'

const props = defineProps<{ id?: number }>()
const router = useRouter()
const editing = computed(() => Number.isFinite(props.id))
const loading = ref(false)
const saving = ref(false)
const testing = ref(false)
const deleting = ref(false)
const error = ref('')
const connection = ref<TestConnectionResult | null>(null)
const benchPathsText = ref('')
const labelRows = ref<Array<{ key: string; value: string }>>([])
const deleteConfirmation = ref('')
const form = reactive<NewServerInput>({
  name: '',
  hostname: '',
  ssh_user: 'monitor',
  ssh_port: 22,
  ssh_key_path: '/var/lib/frappe-monitor/.ssh/id_ed25519',
  bench_paths: [],
  labels: {},
})

function errorText(value: unknown): string {
  return value instanceof Error ? value.message : String(value)
}

async function load() {
  if (!editing.value || !props.id) return
  loading.value = true
  error.value = ''
  try {
    const server = await fetchServer(props.id)
    Object.assign(form, {
      name: server.name,
      hostname: server.hostname,
      ssh_user: server.ssh_user,
      ssh_port: server.ssh_port,
      ssh_key_path: server.ssh_key_path,
    })
    benchPathsText.value = server.bench_paths.join('\n')
    labelRows.value = Object.entries(server.labels ?? {}).map(([key, value]) => ({ key, value }))
  } catch (e) {
    error.value = errorText(e)
  } finally {
    loading.value = false
  }
}

watch(() => props.id, load, { immediate: true })

function payload(): NewServerInput {
  const labels = Object.fromEntries(
    labelRows.value
      .map(({ key, value }) => [key.trim(), value.trim()])
      .filter(([key]) => key),
  )
  return {
    name: form.name.trim(),
    hostname: form.hostname.trim(),
    ssh_user: form.ssh_user.trim(),
    ssh_port: Number(form.ssh_port),
    ssh_key_path: form.ssh_key_path.trim(),
    bench_paths: benchPathsText.value.split('\n').map((path) => path.trim()).filter(Boolean),
    labels,
  }
}

async function testConnection() {
  testing.value = true
  connection.value = null
  error.value = ''
  try {
    const data = payload()
    connection.value = await testDraftServerConnection(data)
  } catch (e) {
    error.value = errorText(e)
  } finally {
    testing.value = false
  }
}

async function save() {
  saving.value = true
  error.value = ''
  try {
    const saved = editing.value && props.id
      ? await patchServer(props.id, payload())
      : await createServer(payload())
    await router.push(`/servers/${saved.id}`)
  } catch (e) {
    error.value = errorText(e)
  } finally {
    saving.value = false
  }
}

async function removeServer() {
  if (!props.id || deleteConfirmation.value !== form.name) return
  deleting.value = true
  error.value = ''
  try {
    await deleteServer(props.id)
    await router.push('/servers')
  } catch (e) {
    error.value = errorText(e)
    deleting.value = false
  }
}
</script>

<template>
  <section class="page form-page">
    <nav class="breadcrumbs"><RouterLink to="/servers">Servers</RouterLink><span>/</span><span>{{ editing ? 'Configure' : 'New server' }}</span></nav>
    <header class="page-heading">
      <div><span class="eyebrow">Fleet configuration</span><h1>{{ editing ? `Configure ${form.name || 'server'}` : 'Add a server' }}</h1><p>Connect an existing Frappe host over SSH. Collection and inspection commands remain read-only.</p></div>
      <div class="page-actions"><RouterLink class="button" :to="editing && id ? `/servers/${id}` : '/servers'">Cancel</RouterLink><button class="button primary" type="button" :disabled="saving || loading" @click="save"><CheckCircle2 :size="16" />{{ saving ? 'Saving…' : 'Save server' }}</button></div>
    </header>

    <div v-if="error" class="notice error">{{ error }}</div>
    <div v-if="connection" class="notice" :class="connection.reachable ? 'success' : 'error'">
      <CheckCircle2 v-if="connection.reachable" :size="17" /><X v-else :size="17" />
      <span v-if="connection.reachable">SSH connection succeeded in {{ connection.latency_ms }} ms.</span>
      <span v-else>SSH test failed{{ connection.error_kind ? ` (${connection.error_kind})` : '' }}: {{ connection.error }}</span>
    </div>
    <div v-if="connection?.error_kind === 'key'" class="notice help-notice">
      The production service runs as <code>frappe-monitor</code> with personal home directories hidden. Use the dedicated key at <code>/var/lib/frappe-monitor/.ssh/id_ed25519</code> and authorize its <code>.pub</code> file on this target.
    </div>

    <form v-if="!loading" class="form-stack" @submit.prevent="save">
      <section class="panel settings-card">
        <div class="panel-header"><div><h2 class="panel-title"><Server :size="17" />Identity</h2><p>Name this target exactly as you want it to appear in metrics and alerts.</p></div></div>
        <div class="panel-body field-grid">
          <label class="field"><span>Server name</span><input v-model="form.name" required maxlength="128" placeholder="production-1" /><small>Stable display name and the <code>server</code> metrics label.</small></label>
          <label class="field"><span>Hostname or IP</span><input v-model="form.hostname" required maxlength="253" placeholder="10.0.0.12" autocomplete="off" /><small>Do not include a scheme or port.</small></label>
        </div>
      </section>

      <section class="panel settings-card">
        <div class="panel-header"><div><h2 class="panel-title"><Wifi :size="17" />SSH access</h2><p>The private key stays on this monitor host and must permit non-interactive login.</p></div><button class="button" type="button" :disabled="testing" @click="testConnection"><Wifi :size="15" />{{ testing ? 'Testing…' : 'Test connection' }}</button></div>
        <div class="panel-body field-grid three">
          <label class="field"><span>SSH user</span><input v-model="form.ssh_user" required maxlength="64" autocomplete="username" /></label>
          <label class="field"><span>SSH port</span><input v-model.number="form.ssh_port" required type="number" min="1" max="65535" inputmode="numeric" /></label>
          <label class="field wide"><span>Private key path</span><input v-model="form.ssh_key_path" required placeholder="/var/lib/frappe-monitor/.ssh/id_ed25519" autocomplete="off" /><small>Path readable by the <code>frappe-monitor</code> service. Personal <code>/home</code> directories are intentionally unavailable in production.</small></label>
        </div>
      </section>

      <section class="panel settings-card">
        <div class="panel-header"><div><h2 class="panel-title">Discovery</h2><p>Leave bench paths empty to discover standard Frappe bench locations automatically.</p></div></div>
        <div class="panel-body"><label class="field"><span>Bench paths</span><textarea v-model="benchPathsText" rows="5" placeholder="/home/frappe/frappe-bench&#10;/srv/erpnext-bench" /><small>One absolute path per line. These paths are inspected, never modified.</small></label></div>
      </section>

      <section class="panel settings-card">
        <div class="panel-header"><div><h2 class="panel-title">Labels</h2><p>Add environment, region, owner, or other searchable context.</p></div><button class="button" type="button" @click="labelRows.push({ key: '', value: '' })"><Plus :size="15" />Add label</button></div>
        <div class="panel-body">
          <div v-if="!labelRows.length" class="inline-empty">No custom labels.</div>
          <div v-for="(row, index) in labelRows" :key="index" class="label-row"><input v-model="row.key" maxlength="64" placeholder="environment" aria-label="Label key" /><input v-model="row.value" maxlength="256" placeholder="production" aria-label="Label value" /><button class="button icon-button ghost" type="button" aria-label="Remove label" @click="labelRows.splice(index, 1)"><Trash2 :size="16" /></button></div>
        </div>
      </section>

      <section v-if="editing" class="panel settings-card danger-card">
        <div class="panel-header"><div><h2 class="panel-title">Danger zone</h2><p>Deleting a server removes its local registration. Historical data remains in the metrics and logs backends until retention expires.</p></div></div>
        <div class="panel-body danger-row"><label class="field"><span>Type <strong>{{ form.name }}</strong> to confirm</span><input v-model="deleteConfirmation" autocomplete="off" /></label><button class="button danger" type="button" :disabled="deleteConfirmation !== form.name || deleting" @click="removeServer"><Trash2 :size="16" />{{ deleting ? 'Deleting…' : 'Delete server' }}</button></div>
      </section>
    </form>
  </section>
</template>

<style scoped>
.form-page{max-width:1040px;margin:0 auto}.form-stack{display:grid;gap:.85rem}.help-notice{margin-top:-.45rem;color:var(--muted);background:var(--card-bg)}.settings-card{overflow:hidden}.panel-header p{margin:.22rem 0 0;color:var(--muted);font-size:.76rem;line-height:1.45}.field-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:1rem}.field-grid.three{grid-template-columns:1fr 140px 2fr}.field-grid .wide{grid-column:auto}.field{display:flex;flex-direction:column;gap:.38rem;min-width:0}.field>span{color:var(--fg);font-size:.78rem;font-weight:650}.field small{color:var(--muted-2);font-size:.7rem;line-height:1.4}.field input,.field textarea,.label-row input{width:100%;border:1px solid var(--card-border-strong);border-radius:8px;background:var(--bg);padding:.62rem .7rem;outline:0}.field input:focus,.field textarea:focus,.label-row input:focus{border-color:var(--accent)}.field textarea{resize:vertical;line-height:1.5;font-family:"SFMono-Regular",Consolas,monospace;font-size:.8rem}.label-row{display:grid;grid-template-columns:minmax(120px,.8fr) minmax(160px,1.2fr) 42px;gap:.55rem;margin-bottom:.55rem}.inline-empty{padding:.4rem 0;color:var(--muted-2);font-size:.8rem}.danger-card{border-color:color-mix(in srgb,var(--status-unreachable) 28%,var(--card-border))}.danger-row{display:grid;grid-template-columns:minmax(0,1fr) auto;align-items:end;gap:1rem}.danger-row .button{margin-bottom:1px}@media(max-width:760px){.field-grid,.field-grid.three{grid-template-columns:1fr}.panel-header{align-items:flex-start;flex-direction:column}.panel-header>.button{width:100%}.label-row{grid-template-columns:1fr 1fr 40px}.danger-row{grid-template-columns:1fr}.danger-row .button{width:100%}}@media(max-width:480px){.label-row{grid-template-columns:1fr 40px}.label-row input:nth-child(2){grid-column:1/2}.label-row .icon-button{grid-column:2;grid-row:1/3;height:100%}}
</style>
