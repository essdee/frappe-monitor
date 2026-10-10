<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import {
  AlertTriangle,
  ArrowRight,
  BellRing,
  Boxes,
  Globe2,
  RefreshCw,
  Server as ServerIcon,
  Sparkles,
} from 'lucide-vue-next'
import { useTimeRange } from '../composables/useTimeRange'
import { useFleetData } from '../composables/useFleetData'
import ServerCard from '../components/ServerCard.vue'

const { refreshSec } = useTimeRange()
const fleet = useFleetData(refreshSec)

const sortedServers = computed(() => [...fleet.servers.value].sort((a, b) => {
  const priority = { unreachable: 0, unknown: 1, reachable: 2 }
  return priority[a.status] - priority[b.status] || a.name.localeCompare(b.name)
}))

const headline = computed(() => {
  if (fleet.servers.value.length === 0) return 'Connect your first Frappe server'
  if (fleet.attentionCount.value === 0) return 'Everything is operating normally'
  return `${fleet.attentionCount.value} ${fleet.attentionCount.value === 1 ? 'item needs' : 'items need'} attention`
})

const headlineDetail = computed(() => {
  if (fleet.servers.value.length === 0) return 'Register an SSH target in the monitor configuration; collection starts automatically.'
  if (fleet.attentionCount.value === 0) return `${fleet.servers.value.length} servers and ${fleet.sites.value.length} sites are reporting without active incidents.`
  const parts: string[] = []
  const serverIssues = fleet.servers.value.length - fleet.reachableServers.value.length
  if (serverIssues) parts.push(`${serverIssues} server ${serverIssues === 1 ? 'issue' : 'issues'}`)
  if (fleet.unhealthySites.value.length) parts.push(`${fleet.unhealthySites.value.length} unhealthy ${fleet.unhealthySites.value.length === 1 ? 'site' : 'sites'}`)
  if (fleet.unknownSites.value.length) parts.push(`${fleet.unknownSites.value.length} unknown ${fleet.unknownSites.value.length === 1 ? 'probe' : 'probes'}`)
  if (fleet.alerts.value.firing.length) parts.push(`${fleet.alerts.value.firing.length} active ${fleet.alerts.value.firing.length === 1 ? 'incident' : 'incidents'}`)
  return parts.join(' · ')
})

function countsFor(server: string) {
  const benches = fleet.benches.value.filter((item) => item.server === server).length
  const sites = fleet.sites.value.filter((item) => item.server === server)
  return {
    benches,
    sites: sites.length,
    healthy: sites.filter((site) => site.is_healthy === 1).length,
  }
}

function alertTarget(labels: Record<string, string>): string {
  if (labels.server && labels.bench && labels.site) {
    return `/sites/${encodeURIComponent(labels.server)}/${encodeURIComponent(labels.bench)}/${encodeURIComponent(labels.site)}`
  }
  if (labels.server && labels.bench) {
    return `/benches/${encodeURIComponent(labels.server)}/${encodeURIComponent(labels.bench)}`
  }
  return '/alerts'
}
</script>

<template>
  <section class="page overview-page">
    <header class="page-heading">
      <div>
        <span class="eyebrow">Fleet status</span>
        <h1>{{ headline }}</h1>
        <p>{{ headlineDetail }}</p>
      </div>
      <div class="page-actions">
        <button class="button" :disabled="fleet.loading.value" @click="fleet.refresh">
          <RefreshCw :size="15" :class="{ spin: fleet.loading.value }" />
          {{ fleet.loading.value ? 'Refreshing' : 'Refresh fleet' }}
        </button>
      </div>
    </header>

    <div v-if="fleet.errors.value.length" class="notice error">
      <AlertTriangle :size="18" />
      <span>Some data sources could not be refreshed: {{ fleet.errors.value.join(' · ') }}</span>
    </div>

    <div class="summary-grid" aria-label="Fleet summary">
      <RouterLink to="/servers" class="panel summary-card">
        <span class="summary-icon server"><ServerIcon :size="18" /></span>
        <div class="summary-label">Servers online</div>
        <div class="summary-value">{{ fleet.reachableServers.value.length }}<small>/{{ fleet.servers.value.length }}</small></div>
        <div class="summary-note">Read-only SSH targets</div>
      </RouterLink>
      <RouterLink to="/benches" class="panel summary-card">
        <span class="summary-icon bench"><Boxes :size="18" /></span>
        <div class="summary-label">Benches</div>
        <div class="summary-value">{{ fleet.benches.value.length }}</div>
        <div class="summary-note">Active Frappe installations</div>
      </RouterLink>
      <RouterLink to="/sites" class="panel summary-card">
        <span class="summary-icon site"><Globe2 :size="18" /></span>
        <div class="summary-label">Healthy sites</div>
        <div class="summary-value">{{ fleet.healthySites.value.length }}<small>/{{ fleet.sites.value.length }}</small></div>
        <div class="summary-note">HTTP probes passing</div>
      </RouterLink>
      <RouterLink to="/alerts" class="panel summary-card" :class="{ danger: fleet.alerts.value.firing.length > 0 }">
        <span class="summary-icon alert"><BellRing :size="18" /></span>
        <div class="summary-label">Active incidents</div>
        <div class="summary-value">{{ fleet.alerts.value.firing.length }}</div>
        <div class="summary-note">{{ fleet.alerts.value.enabled ? 'Telegram evaluation enabled' : 'Alert delivery disabled' }}</div>
      </RouterLink>
    </div>

    <div v-if="fleet.attentionCount.value > 0" class="section-heading">
      <div><h2>Needs attention</h2><p>Failures and unknown states are shown first.</p></div>
      <RouterLink to="/alerts" class="section-link">Open incidents <ArrowRight :size="14" /></RouterLink>
    </div>
    <div v-if="fleet.attentionCount.value > 0" class="attention-grid">
      <RouterLink
        v-for="server in fleet.servers.value.filter((item) => item.status !== 'reachable')"
        :key="`server-${server.id}`"
        :to="`/servers/${server.id}`"
        class="panel attention-card"
      >
        <AlertTriangle :size="18" />
        <span><strong>{{ server.name }}</strong><small>{{ server.last_error || `Server is ${server.status}` }}</small></span>
        <ArrowRight :size="15" class="attention-arrow" />
      </RouterLink>
      <RouterLink
        v-for="site in [...fleet.unhealthySites.value, ...fleet.unknownSites.value]"
        :key="`site-${site.server}-${site.bench}-${site.site}`"
        :to="`/sites/${encodeURIComponent(site.server)}/${encodeURIComponent(site.bench)}/${encodeURIComponent(site.site)}`"
        class="panel attention-card"
      >
        <Globe2 :size="18" />
        <span><strong>{{ site.site }}</strong><small>{{ site.is_healthy < 0 ? 'Probe result unknown' : 'Site health check failed' }} · {{ site.server }}</small></span>
        <ArrowRight :size="15" class="attention-arrow" />
      </RouterLink>
      <RouterLink
        v-for="alert in fleet.alerts.value.firing"
        :key="`alert-${alert.id}`"
        :to="alertTarget(alert.labels)"
        class="panel attention-card"
      >
        <BellRing :size="18" />
        <span><strong>{{ alert.rule_name }}</strong><small>{{ Object.values(alert.labels).filter(Boolean).join(' · ') }}</small></span>
        <ArrowRight :size="15" class="attention-arrow" />
      </RouterLink>
    </div>

    <div class="section-heading">
      <div><h2>Infrastructure</h2><p>Live reachability and workload coverage for every registered host.</p></div>
      <RouterLink v-if="fleet.servers.value.length" to="/servers" class="section-link">View servers <ArrowRight :size="14" /></RouterLink>
    </div>

    <div v-if="fleet.loading.value && fleet.servers.value.length === 0" class="server-grid">
      <div v-for="n in 3" :key="n" class="panel skeleton-card"><span /><span /><span /></div>
    </div>
    <div v-else-if="fleet.servers.value.length" class="server-grid">
      <ServerCard
        v-for="server in sortedServers"
        :key="server.id"
        :server="server"
        :bench-count="countsFor(server.name).benches"
        :site-count="countsFor(server.name).sites"
        :healthy-site-count="countsFor(server.name).healthy"
      />
    </div>
    <div v-else class="panel empty-state onboarding">
      <Sparkles :size="28" />
      <h3>Your monitoring fleet starts here</h3>
      <p>Register an SSH-reachable host in the monitor configuration. Agentless inspection will discover benches and sites and begin recording health metrics.</p>
			<p class="config-note">The dashboard and all target access remain read-only.</p>
    </div>
  </section>
</template>

<style scoped>
.overview-page { padding-bottom: 2rem; }
.summary-card { color: inherit; transition: transform 150ms ease, border-color 150ms ease; }
.summary-card:hover { color: inherit; transform: translateY(-2px); border-color: var(--card-border-strong); }
.summary-card.danger { border-color: color-mix(in srgb, var(--status-unreachable) 35%, var(--card-border)); }
.summary-value small { color: var(--muted-2); font-size: .9rem; margin-left: .15rem; font-weight: 600; }
.summary-icon { width: 34px; height: 34px; display: grid; place-items: center; float: right; border-radius: 9px; color: var(--accent-strong); background: var(--accent-soft); }
.summary-icon.site { color: var(--status-reachable); background: color-mix(in srgb, var(--status-reachable) 10%, transparent); }
.summary-icon.alert { color: var(--status-warning); background: color-mix(in srgb, var(--status-warning) 10%, transparent); }
.section-link { display: inline-flex; align-items: center; gap: .3rem; color: var(--accent-strong); font-size: .78rem; font-weight: 650; }
.attention-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: .65rem; }
.attention-card { min-width: 0; padding: .8rem .9rem; display: flex; align-items: center; gap: .7rem; color: var(--status-unreachable); }
.attention-card span { min-width: 0; display: flex; flex-direction: column; }
.attention-card strong { color: var(--fg); font-size: .83rem; }
.attention-card small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--muted); font-size: .73rem; margin-top: .15rem; }
.attention-arrow { margin-left: auto; color: var(--muted-2); }
.server-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); gap: .8rem; }
.skeleton-card { padding: 1rem; min-height: 154px; }
.skeleton-card span { display: block; height: 12px; width: 60%; margin-bottom: .8rem; border-radius: 6px; background: linear-gradient(90deg, var(--bg-hover), #202a38, var(--bg-hover)); background-size: 200% 100%; animation: shimmer 1.4s infinite; }
.skeleton-card span:nth-child(2) { width: 85%; }.skeleton-card span:nth-child(3) { width: 42%; }
@keyframes shimmer { to { background-position: -200% 0; } }
.config-note { margin-top: .8rem!important; color: var(--accent-strong)!important; }
@media (max-width: 720px) { .attention-grid, .server-grid { grid-template-columns: 1fr; } }
</style>
