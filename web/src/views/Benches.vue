<script setup lang="ts">
import { computed, onMounted, onScopeDispose, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { fetchBenches, type BenchPair } from '../api'
import { useTimeRange } from '../composables/useTimeRange'

const benches = ref<BenchPair[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const { refreshSec } = useTimeRange()

async function refresh() {
  loading.value = true
  error.value = null
  try {
    benches.value = await fetchBenches()
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

// Group by server so the operator can scan one server at a time.
const grouped = computed<{ server: string; benches: string[] }[]>(() => {
  const m = new Map<string, string[]>()
  for (const b of benches.value) {
    const arr = m.get(b.server) ?? []
    arr.push(b.bench)
    m.set(b.server, arr)
  }
  return [...m.entries()]
    .sort((a, b) => a[0].localeCompare(b[0]))
    .map(([server, bs]) => ({ server, benches: bs.sort() }))
})
</script>

<template>
  <section>
    <header class="page-header">
      <h2>Benches</h2>
      <button class="refresh-btn" :disabled="loading" @click="refresh">
        {{ loading ? 'Refreshing…' : 'Refresh' }}
      </button>
    </header>

    <p v-if="error" class="error">Failed to load benches: {{ error }}</p>

    <p v-if="!loading && benches.length === 0" class="muted">
      No benches reporting yet. The collector populates this list once
      <code>frappe_bench_apps_count</code> is being pushed to VictoriaMetrics.
    </p>

    <div v-for="group in grouped" :key="group.server" class="server-group">
      <h3 class="server-name">{{ group.server }}</h3>
      <div class="grid">
        <RouterLink
          v-for="bench in group.benches"
          :key="bench"
          :to="`/benches/${encodeURIComponent(group.server)}/${encodeURIComponent(bench)}`"
          class="card"
        >
          <div class="bench-name">{{ bench }}</div>
          <div class="bench-sub">on {{ group.server }}</div>
        </RouterLink>
      </div>
    </div>
  </section>
</template>

<style scoped>
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  margin-bottom: 1rem;
}
.refresh-btn {
  background: var(--card-bg);
  color: var(--fg);
  border: 1px solid var(--card-border);
  padding: 0.35rem 0.85rem;
  border-radius: 4px;
  cursor: pointer;
  font: inherit;
}
.refresh-btn:hover:not(:disabled) {
  border-color: var(--accent);
}
.refresh-btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
.server-group {
  margin-bottom: 1.5rem;
}
.server-name {
  font-size: 0.95rem;
  color: var(--muted);
  margin: 0 0 0.5rem 0;
  font-family: ui-monospace, "SF Mono", Menlo, monospace;
}
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  gap: 0.75rem;
}
.card {
  display: block;
  padding: 0.75rem 1rem;
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  border-radius: 6px;
  text-decoration: none;
  color: inherit;
  transition: border-color 120ms;
}
.card:hover {
  border-color: var(--accent);
}
.bench-name {
  font-weight: 600;
}
.bench-sub {
  color: var(--muted);
  font-size: 0.8rem;
  margin-top: 0.25rem;
}
.error {
  padding: 0.5rem 0.75rem;
  background: color-mix(in srgb, var(--status-unreachable) 12%, transparent);
  color: var(--status-unreachable);
  border-radius: 4px;
}
.muted {
  color: var(--muted);
}
</style>
