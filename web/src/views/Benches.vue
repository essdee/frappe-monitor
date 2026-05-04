<script setup lang="ts">
import { computed, onMounted, onScopeDispose, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { Boxes, Server, RefreshCw, ChevronRight } from 'lucide-vue-next'
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
      <div class="title">
        <Boxes :size="22" :stroke-width="2.25" class="title-icon" />
        <h2>Benches</h2>
        <span class="count">{{ benches.length }}</span>
      </div>
      <button class="refresh-btn" :disabled="loading" @click="refresh">
        <RefreshCw :size="14" :stroke-width="2" :class="{ spin: loading }" />
        {{ loading ? 'Refreshing' : 'Refresh' }}
      </button>
    </header>

    <p v-if="error" class="error">Failed to load benches: {{ error }}</p>

    <p v-if="!loading && benches.length === 0" class="muted">
      No benches reporting yet. The collector populates this list once
      <code>frappe_bench_apps_count</code> is being pushed to VictoriaMetrics.
    </p>

    <div v-for="group in grouped" :key="group.server" class="server-group">
      <h3 class="server-name">
        <Server :size="14" :stroke-width="2" />
        {{ group.server }}
      </h3>
      <div class="grid">
        <RouterLink
          v-for="bench in group.benches"
          :key="bench"
          :to="`/benches/${encodeURIComponent(group.server)}/${encodeURIComponent(bench)}`"
          class="card"
        >
          <div class="card-head">
            <Boxes :size="16" :stroke-width="2" class="card-icon" />
            <span class="bench-name">{{ bench }}</span>
            <ChevronRight :size="14" :stroke-width="2" class="chev" />
          </div>
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
  align-items: center;
  margin-bottom: 1.5rem;
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
  transition: border-color 120ms, background 120ms;
}
.refresh-btn:hover:not(:disabled) {
  border-color: var(--accent);
  background: var(--bg-hover);
}
.refresh-btn:disabled { opacity: 0.6; cursor: not-allowed; }
.spin { animation: spin 0.9s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
.server-group {
  margin-bottom: 1.5rem;
}
.server-name {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  font-size: 0.85rem;
  color: var(--muted);
  margin: 0 0 0.6rem 0;
  font-family: "JetBrains Mono", ui-monospace, "SF Mono", Menlo, monospace;
  font-weight: 500;
  text-transform: lowercase;
  letter-spacing: 0.01em;
}
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
  gap: 0.85rem;
}
.card {
  display: block;
  padding: 0.85rem 1rem;
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  border-radius: 8px;
  text-decoration: none;
  color: inherit;
  transition: border-color 120ms, transform 120ms;
  box-shadow: var(--card-shadow);
}
.card:hover {
  border-color: var(--accent);
  transform: translateY(-1px);
}
.card-head {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}
.card-icon { color: var(--muted); }
.bench-name {
  flex: 1;
  font-weight: 600;
  letter-spacing: -0.01em;
}
.chev { color: var(--muted-2); }
.card:hover .chev,
.card:hover .card-icon { color: var(--accent); }
.bench-sub {
  color: var(--muted);
  font-size: 0.78rem;
  margin-top: 0.35rem;
  margin-left: 1.5rem;
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

@media (max-width: 720px) {
  .grid {
    grid-template-columns: 1fr;
    gap: 0.6rem;
  }
  .card { padding: 0.7rem 0.85rem; }
  .bench-sub { margin-left: 1.4rem; font-size: 0.75rem; }
  .server-name { font-size: 0.8rem; }
}
</style>
