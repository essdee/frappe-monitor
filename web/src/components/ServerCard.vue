<script setup lang="ts">
import { RouterLink } from 'vue-router'
import { CheckCircle2, AlertTriangle, HelpCircle, Clock } from 'lucide-vue-next'
import type { Server } from '../api'

defineProps<{ server: Server }>()

function statusColor(status: Server['status']): string {
  switch (status) {
    case 'reachable':
      return 'var(--status-reachable)'
    case 'unreachable':
      return 'var(--status-unreachable)'
    default:
      return 'var(--status-unknown)'
  }
}

function relativeTime(iso: string | null): string {
  if (!iso) return 'never'
  const ts = new Date(iso).getTime()
  if (Number.isNaN(ts)) return 'unknown'
  const ageMs = Date.now() - ts
  const ageSec = Math.floor(ageMs / 1000)
  if (ageSec < 60) return `${ageSec}s ago`
  if (ageSec < 3600) return `${Math.floor(ageSec / 60)}m ago`
  if (ageSec < 86400) return `${Math.floor(ageSec / 3600)}h ago`
  return `${Math.floor(ageSec / 86400)}d ago`
}
</script>

<template>
  <RouterLink :to="`/servers/${server.id}`" class="card">
    <div class="header">
      <CheckCircle2
        v-if="server.status === 'reachable'"
        :size="18"
        :stroke-width="2.25"
        :style="{ color: statusColor(server.status) }"
      />
      <AlertTriangle
        v-else-if="server.status === 'unreachable'"
        :size="18"
        :stroke-width="2.25"
        :style="{ color: statusColor(server.status) }"
      />
      <HelpCircle
        v-else
        :size="18"
        :stroke-width="2.25"
        :style="{ color: statusColor(server.status) }"
      />
      <span class="name">{{ server.name }}</span>
    </div>
    <div class="hostname">{{ server.hostname }}</div>
    <div class="meta">
      <span class="status-text" :style="{ color: statusColor(server.status) }">
        {{ server.status }}
      </span>
      <span class="last-seen">
        <Clock :size="12" :stroke-width="2" />
        {{ relativeTime(server.last_pinged_at) }}
      </span>
    </div>
    <div v-if="server.last_error" class="error" :title="server.last_error">
      {{ server.last_error }}
    </div>
  </RouterLink>
</template>

<style scoped>
.card {
  display: block;
  padding: 1rem 1.1rem;
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  border-radius: 8px;
  text-decoration: none;
  color: inherit;
  transition: border-color 120ms ease, transform 120ms ease;
  box-shadow: var(--card-shadow);
}
.card:hover {
  border-color: var(--accent);
  transform: translateY(-1px);
}
.header {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  font-weight: 600;
  margin-bottom: 0.35rem;
}
.name {
  font-size: 1rem;
  letter-spacing: -0.01em;
}
.hostname {
  color: var(--muted);
  font-size: 0.82rem;
  font-family: "JetBrains Mono", ui-monospace, "SF Mono", Menlo, monospace;
  margin-bottom: 0.75rem;
  word-break: break-all;
  overflow-wrap: anywhere;
}
.name {
  word-break: break-word;
  overflow-wrap: anywhere;
  min-width: 0;
}
.header {
  min-width: 0;
}
.meta {
  display: flex;
  justify-content: space-between;
  font-size: 0.82rem;
  color: var(--muted);
}
.status-text {
  text-transform: capitalize;
  font-weight: 500;
}
.last-seen {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  color: var(--muted);
}
.error {
  margin-top: 0.65rem;
  padding: 0.4rem 0.6rem;
  background: color-mix(in srgb, var(--status-unreachable) 15%, transparent);
  border: 1px solid color-mix(in srgb, var(--status-unreachable) 35%, transparent);
  color: var(--status-unreachable);
  border-radius: 4px;
  font-size: 0.75rem;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

@media (max-width: 720px) {
  .card { padding: 0.85rem 0.95rem; }
  .header { gap: 0.4rem; }
  .name { font-size: 0.95rem; }
  .meta { flex-wrap: wrap; gap: 0.35rem 0.75rem; }
  /* On a phone the card is full-width, so the error string can wrap
     to two lines instead of getting truncated to a few characters. */
  .error {
    white-space: normal;
    word-break: break-word;
    overflow: visible;
    text-overflow: clip;
    line-height: 1.4;
  }
}
</style>
