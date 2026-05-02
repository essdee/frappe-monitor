<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { Activity, Lock, AlertCircle, CheckCircle2 } from 'lucide-vue-next'
import { login, whoami } from '../api'

const router = useRouter()
const route = useRoute()
const password = ref('')
const submitting = ref(false)
const error = ref<string | null>(null)
const justSignedOut = computed(() => route.query.signed_out === '1')

// If we're already authed (e.g. someone bookmarked /login), bounce
// straight to the next page instead of showing the form.
onMounted(async () => {
  if (await whoami()) {
    redirectNext()
  }
})

function redirectNext() {
  const next = (route.query.next as string) || '/'
  // Reject open-redirects: only allow same-origin paths.
  const safe = next.startsWith('/') && !next.startsWith('//') ? next : '/'
  router.replace(safe)
}

async function submit() {
  if (!password.value) {
    error.value = 'Enter your password'
    return
  }
  submitting.value = true
  error.value = null
  try {
    await login(password.value)
    redirectNext()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    password.value = ''
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="login-shell">
    <div class="login-card">
      <div class="brand">
        <Activity :size="28" :stroke-width="2.25" class="brand-icon" />
        <div>
          <h1>frappe-monitor</h1>
          <span class="brand-sub">production dashboard</span>
        </div>
      </div>

      <p v-if="justSignedOut" class="signed-out">
        <CheckCircle2 :size="14" :stroke-width="2.25" />
        You've been signed out.
      </p>

      <form @submit.prevent="submit">
        <label>
          <span><Lock :size="14" :stroke-width="2" /> Password</span>
          <input
            v-model="password"
            type="password"
            autocomplete="current-password"
            autofocus
            :disabled="submitting"
          />
        </label>

        <p v-if="error" class="error">
          <AlertCircle :size="14" :stroke-width="2.25" />
          {{ error }}
        </p>

        <button type="submit" class="primary" :disabled="submitting || !password">
          {{ submitting ? 'Signing in…' : 'Sign in' }}
        </button>
      </form>

      <p class="hint">
        Set the password in
        <code>/etc/frappe-monitor/monitor.yaml</code>
        under <code>auth.password</code>, then
        <code>sudo systemctl restart frappe-monitor</code>.
      </p>
    </div>
  </div>
</template>

<style scoped>
.login-shell {
  min-height: 100vh;
  display: grid;
  place-items: center;
  padding: 1.5rem;
  background:
    radial-gradient(circle at 20% 0%, color-mix(in srgb, var(--accent) 12%, transparent), transparent 50%),
    radial-gradient(circle at 80% 100%, color-mix(in srgb, var(--accent) 8%, transparent), transparent 50%),
    var(--bg);
}
.login-card {
  width: 100%;
  max-width: 380px;
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  border-radius: 12px;
  padding: 2rem 1.75rem 1.5rem;
  box-shadow: var(--card-shadow);
}
.brand {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding-bottom: 1.5rem;
  border-bottom: 1px solid var(--card-border);
  margin-bottom: 1.5rem;
}
.brand-icon { color: var(--accent); }
.brand h1 {
  margin: 0;
  font-size: 1.1rem;
  letter-spacing: -0.02em;
}
.brand-sub {
  display: block;
  font-size: 0.7rem;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.08em;
  margin-top: 0.1rem;
}

label {
  display: block;
  margin-bottom: 0.85rem;
}
label span {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
  color: var(--muted);
  font-size: 0.8rem;
  margin-bottom: 0.35rem;
}
input {
  width: 100%;
  padding: 0.6rem 0.75rem;
  background: var(--bg-elevated);
  border: 1px solid var(--card-border);
  border-radius: 6px;
  color: var(--fg);
  font-size: 0.95rem;
  font: inherit;
  transition: border-color 120ms;
}
input:focus {
  outline: none;
  border-color: var(--accent);
}

.primary {
  width: 100%;
  background: var(--accent);
  color: var(--fg-strong);
  border: 1px solid var(--accent);
  padding: 0.65rem 0.85rem;
  border-radius: 6px;
  font: inherit;
  font-size: 0.9rem;
  font-weight: 600;
  cursor: pointer;
  transition: background 120ms;
}
.primary:hover:not(:disabled) {
  background: var(--accent-strong);
  border-color: var(--accent-strong);
}
.primary:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}

.error {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  margin: 0 0 0.85rem 0;
  padding: 0.5rem 0.7rem;
  background: color-mix(in srgb, var(--status-unreachable) 12%, transparent);
  border: 1px solid color-mix(in srgb, var(--status-unreachable) 35%, transparent);
  color: var(--status-unreachable);
  border-radius: 6px;
  font-size: 0.85rem;
  width: 100%;
  box-sizing: border-box;
}

.signed-out {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  margin: 0 0 1rem 0;
  padding: 0.55rem 0.75rem;
  background: color-mix(in srgb, var(--status-reachable) 12%, transparent);
  border: 1px solid color-mix(in srgb, var(--status-reachable) 35%, transparent);
  color: var(--status-reachable);
  border-radius: 6px;
  font-size: 0.85rem;
  width: 100%;
  box-sizing: border-box;
}

.hint {
  margin: 1.25rem 0 0 0;
  padding-top: 1rem;
  border-top: 1px solid var(--card-border);
  font-size: 0.78rem;
  color: var(--muted);
  line-height: 1.55;
}

@media (max-width: 480px) {
  .login-card { padding: 1.5rem 1.25rem 1.25rem; }
}
</style>
