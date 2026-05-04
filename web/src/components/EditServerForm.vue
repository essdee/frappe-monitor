<script setup lang="ts">
import { ref, watch } from 'vue'
import { patchServer, type NewServerInput, type Server } from '../api'

// Edit form for an already-registered server. Backed by PATCH
// /api/v1/servers/{id} (partial update — server-side leaves nil
// fields untouched). Rather than reuse AddServerForm with a "mode"
// prop, this component owns the slightly different concerns:
//   - the `name` field is read-only here (it's the metric label;
//     renaming would orphan all historical VM series for this host),
//   - bench_paths comes pre-populated from the existing record,
//   - submit calls patchServer instead of createServer.
const props = defineProps<{ server: Server }>()
const emit = defineEmits<{ saved: [Server]; cancelled: [] }>()

const form = ref<Omit<NewServerInput, 'name'>>({
  hostname: '',
  ssh_user: '',
  ssh_port: 22,
  ssh_key_path: '',
})
const benchPathsText = ref('')
const submitting = ref(false)
const error = ref<string | null>(null)

// Re-prime whenever the input record changes (e.g. user closes the
// form after a save and re-opens later — page may have refreshed
// the server prop in the background).
watch(
  () => props.server,
  (s) => {
    form.value = {
      hostname: s.hostname,
      ssh_user: s.ssh_user,
      ssh_port: s.ssh_port,
      ssh_key_path: s.ssh_key_path,
    }
    benchPathsText.value = (s.bench_paths ?? []).join('\n')
  },
  { immediate: true, deep: true },
)

function parseBenchPaths(s: string): string[] {
  return s
    .split('\n')
    .map((p) => p.trim())
    .filter((p) => p.length > 0)
}

async function submit() {
  if (!form.value.hostname || !form.value.ssh_key_path) {
    error.value = 'hostname and ssh_key_path are required'
    return
  }
  submitting.value = true
  error.value = null
  try {
    // Always send bench_paths — empty array means "clear overrides
    // and let the bench script auto-discover" and that's a real
    // intent operators express by clearing the textarea.
    const updated = await patchServer(props.server.id, {
      hostname: form.value.hostname,
      ssh_user: form.value.ssh_user,
      ssh_port: form.value.ssh_port,
      ssh_key_path: form.value.ssh_key_path,
      bench_paths: parseBenchPaths(benchPathsText.value),
    })
    emit('saved', updated)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <form class="edit-form" @submit.prevent="submit">
    <h3>Edit server</h3>
    <p class="hint">
      Updates apply on the next collector cycle — no restart needed.
      The <strong>name</strong> is the metric label
      (<code>server="{{ props.server.name }}"</code>) and isn't editable;
      renaming would orphan every historical series for this host.
    </p>

    <label>
      <span>Name</span>
      <input :value="props.server.name" disabled />
    </label>
    <label>
      <span>Hostname / IP</span>
      <input
        v-model.trim="form.hostname"
        placeholder="bench1.example.com or 10.0.0.7"
        required
        autocomplete="off"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
      />
    </label>
    <div class="row">
      <label class="grow">
        <span>SSH user</span>
        <input v-model.trim="form.ssh_user" required autocomplete="off" />
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
        <em class="muted">
          (one per line, on the remote host. Empty = auto-discover under
          <code>/home/*/frappe-bench</code>, <code>/home/*/bench-*</code>,
          <code>/opt/bench/*</code>.)
        </em>
      </span>
      <textarea
        v-model="benchPathsText"
        rows="3"
        placeholder="/home/anas/frappe-bench&#10;/home/anas/bench-staging"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
      ></textarea>
    </label>

    <p v-if="error" class="error">{{ error }}</p>

    <div class="actions">
      <button type="button" class="secondary" :disabled="submitting" @click="emit('cancelled')">
        Cancel
      </button>
      <button type="submit" class="primary" :disabled="submitting">
        {{ submitting ? 'Saving…' : 'Save changes' }}
      </button>
    </div>
  </form>
</template>

<style scoped>
.edit-form {
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  border-radius: 8px;
  padding: 1rem 1.25rem;
  margin-bottom: 1rem;
  box-shadow: var(--card-shadow);
}
.edit-form h3 {
  margin: 0 0 0.25rem 0;
  font-size: 1rem;
}
.hint {
  color: var(--muted);
  font-size: 0.85rem;
  margin: 0 0 1rem 0;
  line-height: 1.5;
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
  margin-bottom: 0.25rem;
}
input,
textarea {
  width: 100%;
  padding: 0.5rem 0.65rem;
  border: 1px solid var(--card-border);
  border-radius: 6px;
  background: var(--bg-elevated);
  color: var(--fg);
  font: inherit;
  box-sizing: border-box;
}
input:disabled {
  opacity: 0.7;
  cursor: not-allowed;
}
textarea {
  font-family: "JetBrains Mono", ui-monospace, "SF Mono", Menlo, monospace;
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
  display: block;
  margin-top: 0.15rem;
  line-height: 1.4;
}
.muted code {
  font-family: ui-monospace, "SF Mono", Menlo, monospace;
}
.row {
  display: flex;
  gap: 0.75rem;
}
.row .grow { flex: 1; min-width: 0; }
.row .port { width: 110px; flex-shrink: 0; }
.actions {
  display: flex;
  gap: 0.5rem;
  justify-content: flex-end;
  margin-top: 0.75rem;
}
button {
  padding: 0.5rem 1rem;
  border-radius: 6px;
  font: inherit;
  font-weight: 500;
  cursor: pointer;
  border: 1px solid var(--card-border);
  background: var(--card-bg);
  color: var(--fg);
}
button:hover:not(:disabled) {
  border-color: var(--accent);
  background: var(--bg-hover);
}
button:disabled { opacity: 0.6; cursor: not-allowed; }
button.primary {
  background: var(--accent);
  color: var(--fg-strong);
  border-color: var(--accent);
}
button.primary:hover:not(:disabled) {
  background: var(--accent-strong);
  border-color: var(--accent-strong);
}
.error {
  padding: 0.5rem 0.75rem;
  background: color-mix(in srgb, var(--status-unreachable) 12%, transparent);
  color: var(--status-unreachable);
  border-radius: 6px;
  font-size: 0.85rem;
  margin: 0.5rem 0;
}

@media (max-width: 720px) {
  .edit-form { padding: 0.85rem 0.95rem; }
  .row { flex-direction: column; gap: 0.5rem; }
  .row .port { width: 100%; }
  .actions {
    flex-direction: column-reverse;
    gap: 0.5rem;
  }
  .actions button {
    width: 100%;
    padding: 0.6rem 1rem;
  }
}
</style>
