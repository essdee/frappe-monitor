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

// Single chokepoint for unauthenticated responses: when the API says
// 401, redirect the browser to /login. Subscribers (the auth guard)
// don't have to wire this themselves — every fetch in the app goes
// through one of these wrappers.
function handle401(resp: Response): Response {
  if (resp.status === 401 && !window.location.pathname.startsWith('/login')) {
    const next = encodeURIComponent(
      window.location.pathname + window.location.search,
    )
    window.location.assign(`/login?next=${next}`)
  }
  return resp
}

// apiFetch is the shared wrapper for non-GET / action calls: it sends
// same-origin credentials and routes the response through handle401 so a
// 401 on a write path (expired session mid-action) redirects to /login
// instead of surfacing a raw "401 Unauthorized" error banner. Auth
// endpoints (login/logout/whoami) deliberately do NOT use this — a 401
// there is "wrong password", not "session expired".
async function apiFetch(url: string, init?: RequestInit): Promise<Response> {
  return handle401(await fetch(url, { credentials: 'same-origin', ...init }))
}

async function jsonGET<T>(url: string): Promise<T> {
  const resp = handle401(
    await fetch(url, {
      headers: { Accept: 'application/json' },
      credentials: 'same-origin',
    }),
  )
  if (!resp.ok) {
    const body = await resp.text()
    throw new Error(`${resp.status} ${resp.statusText}: ${body.slice(0, 200)}`)
  }
  return resp.json() as Promise<T>
}

// --- Auth ---------------------------------------------------------------

export async function login(password: string): Promise<void> {
  const resp = await fetch('/api/v1/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'same-origin',
    body: JSON.stringify({ password }),
  })
  if (resp.status === 204) return
  if (resp.status === 401) throw new Error('Wrong password')
  if (resp.status === 429) throw new Error('Too many attempts — wait a minute')
  const body = await resp.text()
  throw new Error(`${resp.status} ${resp.statusText}: ${body.slice(0, 200)}`)
}

export async function logout(): Promise<void> {
  await fetch('/api/v1/logout', {
    method: 'POST',
    credentials: 'same-origin',
  })
}

export async function whoami(): Promise<boolean> {
  const resp = await fetch('/api/v1/whoami', { credentials: 'same-origin' })
  return resp.ok
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
  const resp = await apiFetch('/api/v1/servers', {
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
  const resp = await apiFetch(`/api/v1/servers/${id}`, {
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
  const resp = await apiFetch(`/api/v1/servers/${id}`, { method: 'DELETE' })
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
  const resp = await apiFetch(`/api/v1/servers/${id}/test-connection`, { method: 'POST' })
  if (!resp.ok) {
    const body = await resp.text()
    throw new Error(`${resp.status} ${resp.statusText}: ${body.slice(0, 200)}`)
  }
  return (await resp.json()) as TestConnectionResult
}

export async function deployCollector(id: number): Promise<{ deployed: boolean; version: string }> {
  const resp = await apiFetch(`/api/v1/servers/${id}/deploy-collector`, { method: 'POST' })
  if (!resp.ok) {
    const body = await resp.text()
    throw new Error(`${resp.status} ${resp.statusText}: ${body.slice(0, 200)}`)
  }
  return (await resp.json()) as { deployed: boolean; version: string }
}

// --- System snapshot ---------------------------------------------------

export interface SystemPayload {
  schema_version: string
  captured_at: number
  system: { os: string; kernel: string; arch: string; hostname: string; uptime_seconds: number }
  cpu:    { model: string; cores: number }
  memory: {
    total_bytes: number
    available_bytes: number
    free_bytes: number
    buffers_bytes: number
    cached_bytes: number
    swap_total_bytes: number
    swap_free_bytes: number
  }
  disks:  Array<{ device: string; mount: string; total_bytes: number; used_bytes: number; available_bytes: number; use_pct: string }>
  load:   { '1m': number; '5m': number; '15m': number }
  top_cpu: Array<ProcessRow>
  top_mem: Array<ProcessRow>
}
export interface ProcessRow {
  pid: number
  user: string
  cpu_pct: number
  mem_pct: number
  rss_kb: number
  command: string
}

export interface SystemSnapshotResp {
  captured_at: string
  payload?: SystemPayload
  last_error?: string
}

export async function fetchSystemSnapshot(id: number): Promise<SystemSnapshotResp | null> {
  const resp = await apiFetch(`/api/v1/servers/${id}/system`)
  if (resp.status === 404) return null
  if (!resp.ok) {
    const body = await resp.text()
    throw new Error(`${resp.status} ${resp.statusText}: ${body.slice(0, 200)}`)
  }
  return (await resp.json()) as SystemSnapshotResp
}

export async function refreshSystemSnapshot(id: number): Promise<SystemSnapshotResp> {
  const resp = await apiFetch(`/api/v1/servers/${id}/refresh-system`, { method: 'POST' })
  if (!resp.ok) {
    const body = await resp.text()
    throw new Error(`${resp.status} ${resp.statusText}: ${body.slice(0, 200)}`)
  }
  return (await resp.json()) as SystemSnapshotResp
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

// --- Phase 6 alerts ----------------------------------------------------

export interface AlertRule {
  name: string
  expr: string
  severity: string
  message?: string
  fingerprint_labels?: string[]
}

export interface FiringAlert {
  id: number
  rule_name: string
  fingerprint: string
  status: 'firing' | 'resolved'
  value: number
  labels: Record<string, string>
  first_fired_at: string
  last_notified_at: string
}

export interface AlertsResponse {
  enabled: boolean
  rules: AlertRule[]
  firing: FiringAlert[]
}

export function fetchAlerts(): Promise<AlertsResponse> {
  return jsonGET<AlertsResponse>('/api/v1/alerts')
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

// --- Phase 9 DB monitor (replication) ----------------------------------

export interface DBTarget {
  id: number
  server_id: number
  /** Resolved server name — only carried on db.status push events. */
  server?: string
  name: string
  enabled: boolean
  lag_threshold_seconds: number
  mysql_command: string
  defaults_file?: string
  socket?: string
  heartbeat_enabled: boolean
  heartbeat_query?: string
  status: string // unknown | healthy | lagging | broken | unreachable
  last_checked_at?: string
  lag_seconds?: number
  heartbeat_lag_seconds?: number
  io_running: boolean
  sql_running: boolean
  last_error?: string
  created_at: string
  updated_at: string
}

export interface NewDBTargetInput {
  server_id: number
  name: string
  enabled?: boolean
  lag_threshold_seconds?: number
  mysql_command?: string
  defaults_file?: string
  socket?: string
  heartbeat_enabled?: boolean
  heartbeat_query?: string
}

export function fetchDBTargets(): Promise<DBTarget[]> {
  return jsonGET<DBTarget[]>('/api/v1/db-targets')
}

export async function createDBTarget(input: NewDBTargetInput): Promise<DBTarget> {
  const resp = await apiFetch('/api/v1/db-targets', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(input),
  })
  if (!resp.ok) {
    const b = await resp.text()
    throw new Error(`${resp.status} ${resp.statusText}: ${b.slice(0, 200)}`)
  }
  return (await resp.json()) as DBTarget
}

export async function patchDBTarget(id: number, patch: Partial<NewDBTargetInput>): Promise<DBTarget> {
  const resp = await apiFetch(`/api/v1/db-targets/${id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(patch),
  })
  if (!resp.ok) {
    const b = await resp.text()
    throw new Error(`${resp.status} ${resp.statusText}: ${b.slice(0, 200)}`)
  }
  return (await resp.json()) as DBTarget
}

export async function deleteDBTarget(id: number): Promise<void> {
  const resp = await apiFetch(`/api/v1/db-targets/${id}`, { method: 'DELETE' })
  if (!resp.ok && resp.status !== 204) {
    const b = await resp.text()
    throw new Error(`${resp.status} ${resp.statusText}: ${b.slice(0, 200)}`)
  }
}

export async function checkDBTarget(id: number): Promise<DBTarget> {
  const resp = await apiFetch(`/api/v1/db-targets/${id}/check`, { method: 'POST' })
  if (!resp.ok) {
    const b = await resp.text()
    throw new Error(`${resp.status} ${resp.statusText}: ${b.slice(0, 200)}`)
  }
  return (await resp.json()) as DBTarget
}
