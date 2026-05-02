// Typed wrappers around the monitor's HTTP API.
// All paths are same-origin so no CORS / token plumbing is needed —
// the monitor binary serves both the SPA and the API.

export interface Server {
  id: number
  name: string
  hostname: string
  ssh_user: string
  ssh_port: number
  ssh_key_path: string
  /** Optional explicit bench paths. Empty = auto-discover. */
  bench_paths: string[]
  status: 'unknown' | 'reachable' | 'unreachable'
  last_pinged_at: string | null
  last_error?: string
  created_at: string
  updated_at: string
  labels?: Record<string, string>
}

export interface MetricsResultValue {
  metric: Record<string, string>
  values: [number, string][] // [unix_seconds, sample_string]
}

export interface MetricsQueryResponse {
  status: 'success' | 'error'
  data: {
    resultType: string
    result: MetricsResultValue[]
  }
  error?: string
}

export interface TimeRange {
  /** Unix seconds. */
  from: number
  /** Unix seconds. */
  to: number
  /** Step in seconds for query_range. */
  step: number
}

async function jsonGET<T>(url: string): Promise<T> {
  const resp = await fetch(url, { headers: { Accept: 'application/json' } })
  if (!resp.ok) {
    const body = await resp.text()
    throw new Error(`${resp.status} ${resp.statusText}: ${body.slice(0, 200)}`)
  }
  return resp.json() as Promise<T>
}

export function fetchServers(): Promise<Server[]> {
  return jsonGET<Server[]>('/api/v1/servers')
}

export function fetchServer(id: number): Promise<Server> {
  return jsonGET<Server>(`/api/v1/servers/${id}`)
}

export interface NewServerInput {
  name: string
  hostname: string
  ssh_user: string
  ssh_port: number
  ssh_key_path: string
  /** Optional. Empty = let the bench-side script auto-discover. */
  bench_paths?: string[]
  labels?: Record<string, string>
}

export async function createServer(input: NewServerInput): Promise<Server> {
  const resp = await fetch('/api/v1/servers', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(input),
  })
  if (!resp.ok) {
    const body = await resp.text()
    throw new Error(`${resp.status} ${resp.statusText}: ${body.slice(0, 200)}`)
  }
  return (await resp.json()) as Server
}

export async function patchServer(id: number, patch: Partial<NewServerInput>): Promise<Server> {
  const resp = await fetch(`/api/v1/servers/${id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(patch),
  })
  if (!resp.ok) {
    const body = await resp.text()
    throw new Error(`${resp.status} ${resp.statusText}: ${body.slice(0, 200)}`)
  }
  return (await resp.json()) as Server
}

export async function deleteServer(id: number): Promise<void> {
  const resp = await fetch(`/api/v1/servers/${id}`, { method: 'DELETE' })
  if (!resp.ok && resp.status !== 204) {
    const body = await resp.text()
    throw new Error(`${resp.status} ${resp.statusText}: ${body.slice(0, 200)}`)
  }
}

export interface TestConnectionResult {
  reachable: boolean
  latency_ms: number
  error?: string
  error_kind?: 'auth' | 'dial' | 'timeout' | 'unknown'
}

export async function testServerConnection(id: number): Promise<TestConnectionResult> {
  const resp = await fetch(`/api/v1/servers/${id}/test-connection`, { method: 'POST' })
  if (!resp.ok) {
    const body = await resp.text()
    throw new Error(`${resp.status} ${resp.statusText}: ${body.slice(0, 200)}`)
  }
  return (await resp.json()) as TestConnectionResult
}

export async function deployCollector(id: number): Promise<{ deployed: boolean; version: string }> {
  const resp = await fetch(`/api/v1/servers/${id}/deploy-collector`, { method: 'POST' })
  if (!resp.ok) {
    const body = await resp.text()
    throw new Error(`${resp.status} ${resp.statusText}: ${body.slice(0, 200)}`)
  }
  return (await resp.json()) as { deployed: boolean; version: string }
}

export function queryMetrics(query: string, range: TimeRange): Promise<MetricsQueryResponse> {
  const params = new URLSearchParams({
    query,
    start: String(range.from),
    end: String(range.to),
    step: String(range.step),
  })
  return jsonGET<MetricsQueryResponse>(`/api/v1/metrics/query?${params.toString()}`)
}

// --- Phase 5 hierarchy --------------------------------------------------

export interface BenchPair {
  server: string
  bench: string
}

export interface BenchDetail {
  server: string
  bench: string
  frappe_version?: string
  apps_count: number
  supervisor_running: number
  supervisor_total: number
  redis_queues: Record<string, number>
}

export interface SitePair {
  server: string
  bench: string
  site: string
}

export interface SiteDetail {
  server: string
  bench: string
  site: string
  http_status_code: number
  http_response_ms: number
  is_healthy: number
  avg_response_ms_1h: number
}

export function fetchBenches(): Promise<BenchPair[]> {
  return jsonGET<BenchPair[]>('/api/v1/benches')
}

export function fetchBench(server: string, bench: string): Promise<BenchDetail> {
  return jsonGET<BenchDetail>(
    `/api/v1/benches/${encodeURIComponent(server)}/${encodeURIComponent(bench)}`,
  )
}

export function fetchSites(): Promise<SitePair[]> {
  return jsonGET<SitePair[]>('/api/v1/sites')
}

export function fetchSite(server: string, bench: string, site: string): Promise<SiteDetail> {
  return jsonGET<SiteDetail>(
    `/api/v1/sites/${encodeURIComponent(server)}/${encodeURIComponent(bench)}/${encodeURIComponent(site)}`,
  )
}

// --- Logs (LogQL via /api/v1/logs/query proxy) -------------------------

export interface LogStreamEntry {
  // [unix_nanos_string, log_line]
  values: [string, string][]
  stream: Record<string, string>
}

export interface LogsQueryResponse {
  status: 'success' | 'error'
  data: {
    resultType: string
    result: LogStreamEntry[]
  }
  error?: string
}

export function queryLogs(
  query: string,
  range: TimeRange,
  limit = 100,
): Promise<LogsQueryResponse> {
  const params = new URLSearchParams({
    query,
    // Loki accepts unix nanoseconds for start/end.
    start: String(range.from * 1_000_000_000),
    end: String(range.to * 1_000_000_000),
    limit: String(limit),
    direction: 'backward',
  })
  return jsonGET<LogsQueryResponse>(`/api/v1/logs/query?${params.toString()}`)
}
