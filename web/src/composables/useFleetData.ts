import { computed, ref, type Ref } from 'vue'
import {
  fetchAlerts,
  fetchBenches,
  fetchServers,
  fetchSites,
  type AlertsResponse,
  type BenchPair,
  type Server,
  type SitePair,
} from '../api'
import { usePolling } from './usePolling'

export function useFleetData(refreshSeconds: Ref<number>) {
  const servers = ref<Server[]>([])
  const benches = ref<BenchPair[]>([])
  const sites = ref<SitePair[]>([])
  const alerts = ref<AlertsResponse>({ enabled: false, rules: [], firing: [], incidents: [] })
  const loading = ref(false)
  const errors = ref<string[]>([])
  const lastUpdated = ref<Date | null>(null)

  async function refresh() {
    loading.value = true
    const results = await Promise.allSettled([
      fetchServers(), fetchBenches(), fetchSites(), fetchAlerts(),
    ])
    const names = ['servers', 'benches', 'sites', 'alerts']
    errors.value = results.flatMap((result, index) =>
      result.status === 'rejected'
        ? [`${names[index]}: ${result.reason instanceof Error ? result.reason.message : String(result.reason)}`]
        : [],
    )
    if (results[0].status === 'fulfilled') servers.value = results[0].value
    if (results[1].status === 'fulfilled') benches.value = results[1].value
    if (results[2].status === 'fulfilled') sites.value = results[2].value
    if (results[3].status === 'fulfilled') alerts.value = results[3].value
    if (results.some((result) => result.status === 'fulfilled')) lastUpdated.value = new Date()
    loading.value = false
  }

  const reachableServers = computed(() => servers.value.filter((server) => server.status === 'reachable'))
  const unhealthySites = computed(() => sites.value.filter((site) => site.is_healthy === 0))
  const unknownSites = computed(() => sites.value.filter((site) => site.is_healthy < 0))
  const healthySites = computed(() => sites.value.filter((site) => site.is_healthy === 1))
  const attentionCount = computed(() =>
    servers.value.filter((server) => server.status !== 'reachable').length +
    unhealthySites.value.length + unknownSites.value.length + alerts.value.firing.length,
  )

  const { refresh: refreshNow } = usePolling(refresh, refreshSeconds)
  return {
    servers, benches, sites, alerts, loading, errors, lastUpdated,
    reachableServers, healthySites, unhealthySites, unknownSites, attentionCount,
    refresh: refreshNow,
  }
}
