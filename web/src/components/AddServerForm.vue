<script setup lang="ts">
import { ref } from 'vue'
import { createServer, type NewServerInput } from '../api'

const emit = defineEmits<{ created: []; cancelled: [] }>()

const form = ref<NewServerInput>({
  name: '',
  hostname: '',
  ssh_user: 'monitor',
  ssh_port: 22,
  ssh_key_path: '',
})
// Bench paths are entered as one path per line in a textarea —
// most operator-friendly UX for an unbounded list.
const benchPathsText = ref('')
const submitting = ref(false)
const error = ref<string | null>(null)

function parseBenchPaths(s: string): string[] {
  return s
    .split('\n')
    .map((p) => p.trim())
    .filter((p) => p.length > 0)
}

async function submit() {
  if (!form.value.name || !form.value.hostname || !form.value.ssh_key_path) {
    error.value = 'name, hostname, and ssh_key_path are required'
    return
  }
  submitting.value = true
  error.value = null
  try {
    const payload: NewServerInput = { ...form.value }
    const paths = parseBenchPaths(benchPathsText.value)
    if (paths.length > 0) {
      payload.bench_paths = paths
    }
    await createServer(payload)
    emit('created')
    // Reset for next time the form opens.
    form.value = {
      name: '',
      hostname: '',
      ssh_user: 'monitor',
      ssh_port: 22,
      ssh_key_path: '',
    }
    benchPathsText.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <form class="add-form" @submit.prevent="submit">
    <h3>Add server</h3>
    <p class="hint">
      The monitor SSHes to this host every cycle. The key path is on the
      <em>monitor</em> host and must be readable by the <code>frappe-monitor</code> user.
    </p>

    <label>
      <span>Name</span>
      <input v-model.trim="form.name" placeholder="prod1" required autocomplete="off" />
    </label>
    <label>
      <span>Hostname</span>
      <input v-model.trim="form.hostname" placeholder="bench1.example.com" required autocomplete="off" />
    </label>
    <div class="row">
      <label class="grow">
        <span>SSH user</span>
        <input v-model.trim="form.ssh_user" placeholder="frappe" required autocomplete="off" />
      </label>
      <label class="port">
        <span>Port</span>
        <input v-model.number="form.ssh_port" type="number" min="1" max="65535" required />
      </label>
    </div>
    <label>
      <span>SSH key path</span>
      <input
        v-model.trim="form.ssh_key_path"
        placeholder="/var/lib/frappe-monitor/.ssh/id_ed25519"
        required
        autocomplete="off"
      />
    </label>

    <label>
      <span>
        Bench paths
        <em class="muted">(one per line, on the remote host. Leave blank to auto-discover under <code>/home/*/frappe-bench</code>, <code>/home/*/bench-*</code>, <code>/opt/bench/*</code>.)</em>
      </span>
      <textarea
        v-model="benchPathsText"
        rows="3"
        placeholder="/home/anas/frappe-bench&#10;/home/anas/bench-staging"
      ></textarea>
    </label>

    <p v-if="error" class="error">{{ error }}</p>

    <div class="actions">
      <button type="button" class="secondary" :disabled="submitting" @click="emit('cancelled')">
        Cancel
      </button>
      <button type="submit" class="primary" :disabled="submitting">
        {{ submitting ? 'Adding…' : 'Add server' }}
      </button>
    </div>
  </form>
</template>

<style scoped>
.add-form {
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  border-radius: 6px;
  padding: 1rem 1.25rem;
  margin-bottom: 1rem;
  max-width: 600px;
}
.add-form h3 {
  margin: 0 0 0.25rem 0;
  font-size: 1rem;
}
.hint {
  color: var(--muted);
  font-size: 0.85rem;
  margin: 0 0 1rem 0;
}
.hint code {
  font-family: ui-monospace, "SF Mono", Menlo, monospace;
}
label {
  display: block;
  margin-bottom: 0.6rem;
  font-size: 0.85rem;
}
label span {
  display: block;
  color: var(--muted);
  margin-bottom: 0.2rem;
}
input,
textarea {
  width: 100%;
  padding: 0.4rem 0.6rem;
  border: 1px solid var(--card-border);
  border-radius: 4px;
  background: transparent;
  color: var(--fg);
  font: inherit;
  box-sizing: border-box;
}
textarea {
  font-family: ui-monospace, "SF Mono", Menlo, monospace;
  font-size: 0.85rem;
  resize: vertical;
}
input:focus,
textarea:focus {
  outline: none;
  border-color: var(--accent);
}
.muted {
  color: var(--muted);
  font-style: normal;
  font-size: 0.78rem;
}
.muted code {
  font-family: ui-monospace, "SF Mono", Menlo, monospace;
}
.row {
  display: flex;
  gap: 0.75rem;
}
.row .grow { flex: 1; }
.row .port { width: 100px; }
.actions {
  display: flex;
  gap: 0.5rem;
  justify-content: flex-end;
  margin-top: 0.5rem;
}
button {
  padding: 0.45rem 1rem;
  border-radius: 4px;
  font: inherit;
  cursor: pointer;
  border: 1px solid var(--card-border);
  background: var(--card-bg);
  color: var(--fg);
}
button:hover:not(:disabled) { border-color: var(--accent); }
button:disabled { opacity: 0.6; cursor: not-allowed; }
button.primary {
  background: var(--accent);
  color: white;
  border-color: var(--accent);
}
.error {
  padding: 0.5rem 0.75rem;
  background: color-mix(in srgb, var(--status-unreachable) 12%, transparent);
  color: var(--status-unreachable);
  border-radius: 4px;
  font-size: 0.85rem;
  margin: 0.5rem 0;
}

@media (max-width: 720px) {
  .add-form { padding: 0.85rem 0.95rem; max-width: 100%; }
  .row { flex-direction: column; gap: 0.5rem; }
  .row .port { width: 100%; }
  .actions {
    flex-direction: column-reverse;
    gap: 0.5rem;
  }
  .actions button {
    width: 100%;
    padding: 0.55rem 1rem;
  }
}
</style>
