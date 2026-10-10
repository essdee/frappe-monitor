<script setup lang="ts">
import { RouterLink } from 'vue-router'
import { ArrowUpRight, Boxes, Clock3, Globe2, Server as ServerIcon } from 'lucide-vue-next'
import type { Server } from '../api'

withDefaults(defineProps<{
  server: Server
  benchCount?: number
  siteCount?: number
  healthySiteCount?: number
}>(), { benchCount: 0, siteCount: 0, healthySiteCount: 0 })

function relativeTime(iso: string | null): string {
  if (!iso) return 'Never collected'
  const age = Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 1000))
  if (age < 60) return `${age}s ago`
  if (age < 3600) return `${Math.floor(age / 60)}m ago`
  if (age < 86400) return `${Math.floor(age / 3600)}h ago`
  return `${Math.floor(age / 86400)}d ago`
}
</script>

<template>
  <RouterLink :to="`/servers/${server.id}`" class="panel server-card" :class="server.status">
    <div class="card-head">
      <span class="server-icon"><ServerIcon :size="18" /></span>
      <div class="identity">
        <strong>{{ server.name }}</strong>
        <span>{{ server.ssh_user }}@{{ server.hostname }}:{{ server.ssh_port }}</span>
      </div>
      <ArrowUpRight :size="17" class="arrow" />
    </div>

    <div class="card-status">
      <span class="status-pill" :class="server.status">{{ server.status }}</span>
      <span class="last-seen"><Clock3 :size="13" /> {{ relativeTime(server.last_pinged_at) }}</span>
    </div>

    <p v-if="server.last_error" class="last-error" :title="server.last_error">{{ server.last_error }}</p>

    <div class="workload-counts">
      <span><Boxes :size="14" /><strong>{{ benchCount }}</strong> benches</span>
      <span><Globe2 :size="14" /><strong>{{ healthySiteCount }}/{{ siteCount }}</strong> sites healthy</span>
    </div>
  </RouterLink>
</template>

<style scoped>
.server-card { min-width: 0; padding: 1rem; color: inherit; transition: transform 150ms ease, border-color 150ms ease, box-shadow 150ms ease; }
.server-card:hover { color: inherit; transform: translateY(-2px); border-color: var(--accent); box-shadow: 0 16px 38px rgba(0,0,0,.27); }
.server-card.unreachable { border-color: color-mix(in srgb, var(--status-unreachable) 30%, var(--card-border)); }
.card-head { display: flex; align-items: center; gap: .7rem; min-width: 0; }
.server-icon { width: 38px; height: 38px; display: grid; place-items: center; flex: 0 0 auto; color: var(--accent-strong); border-radius: 10px; background: var(--accent-soft); }
.identity { min-width: 0; display: flex; flex-direction: column; }
.identity strong { color: var(--fg-strong); font-size: .94rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.identity span { color: var(--muted); font: .72rem "SFMono-Regular", Consolas, monospace; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; margin-top: .15rem; }
.arrow { margin-left: auto; color: var(--muted-2); flex: 0 0 auto; }
.card-status { display: flex; justify-content: space-between; align-items: center; gap: .7rem; margin-top: .95rem; }
.last-seen { display: inline-flex; align-items: center; gap: .3rem; color: var(--muted); font-size: .72rem; white-space: nowrap; }
.last-error { margin: .7rem 0 0; padding: .5rem .6rem; color: #ff9aaa; background: color-mix(in srgb, var(--status-unreachable) 7%, var(--bg)); border-radius: 7px; font-size: .72rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.workload-counts { display: grid; grid-template-columns: 1fr 1fr; gap: .5rem; padding-top: .85rem; margin-top: .85rem; border-top: 1px solid var(--card-border); }
.workload-counts span { display: flex; align-items: center; gap: .35rem; color: var(--muted); font-size: .71rem; }
.workload-counts svg { color: var(--muted-2); }.workload-counts strong { color: var(--fg); font-size: .77rem; }
</style>
