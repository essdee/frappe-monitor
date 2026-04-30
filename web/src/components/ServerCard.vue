<script setup lang="ts">
import { RouterLink } from 'vue-router'
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
      <span class="dot" :style="{ background: statusColor(server.status) }" />
      <span class="name">{{ server.name }}</span>
    </div>
    <div class="hostname">{{ server.hostname }}</div>
    <div class="meta">
      <span class="status-text">{{ server.status }}</span>
      <span class="last-seen">{{ relativeTime(server.last_pinged_at) }}</span>
    </div>
    <div v-if="server.last_error" class="error" :title="server.last_error">
      {{ server.last_error }}
    </div>
  </RouterLink>
</template>

<style scoped>
.card {
  display: block;
  padding: 1rem;
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
.header {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  font-weight: 600;
  margin-bottom: 0.25rem;
}
.dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  display: inline-block;
}
.name {
  font-size: 1rem;
}
.hostname {
  color: var(--muted);
  font-size: 0.85rem;
  font-family: ui-monospace, "SF Mono", Menlo, monospace;
  margin-bottom: 0.5rem;
}
.meta {
  display: flex;
  justify-content: space-between;
  font-size: 0.85rem;
  color: var(--muted);
}
.status-text {
  text-transform: capitalize;
}
.error {
  margin-top: 0.5rem;
  padding: 0.4rem 0.6rem;
  background: color-mix(in srgb, var(--status-unreachable) 12%, transparent);
  color: var(--status-unreachable);
  border-radius: 4px;
  font-size: 0.75rem;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
