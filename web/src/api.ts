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

async function apiFetch(url: string, init: RequestInit = {}): Promise<Response> {
	return handle401(await fetch(url, { ...init, credentials: 'same-origin' }))
}

async function responseError(resp: Response): Promise<Error> {
	const body = await resp.text()
	let message = body.slice(0, 500)
	try {
		const parsed = JSON.parse(body) as { error?: unknown }
		if (typeof parsed.error === 'string') message = parsed.error
	} catch {
		// A non-JSON upstream/proxy error is still useful as plain text.
	}
	return new Error(message || `${resp.status} ${resp.statusText}`)
}

async function jsonGET<T>(url: string): Promise<T> {
	const resp = await apiFetch(url, {
      headers: { Accept: 'application/json' },
	})
	if (!resp.ok) throw await responseError(resp)
  return resp.json() as Promise<T>
}

async function jsonMutation<T>(url: string, init: RequestInit): Promise<T> {
	const resp = await apiFetch(url, {
		...init,
		headers: {
			Accept: 'application/json',
			...(init.body ? { 'Content-Type': 'application/json' } : {}),
			...init.headers,
		},
	})
	if (!resp.ok) throw await responseError(resp)
	return resp.json() as Promise<T>
}

/** PromQL/LogQL string literal escaping for operator-provided labels. */
export function queryStringLiteral(value: string): string {
	return JSON.stringify(value)
}

export function queryLabelMatchers(labels: Record<string, string>): string {
	return Object.entries(labels)
		.map(([key, value]) => `${key}=${queryStringLiteral(value)}`)
		.join(',')
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
  const resp = await fetch('/api/v1/logout', {
    method: 'POST',
    credentials: 'same-origin',
  })
  if (!resp.ok) throw await responseError(resp)
}

export interface AuthStatus {
  authenticated: boolean
  auth_enabled: boolean
  timezone: string
}

export async function whoami(): Promise<AuthStatus> {
  const resp = await fetch('/api/v1/whoami', { credentials: 'same-origin' })
  if (!resp.ok) {
    return { authenticated: false, auth_enabled: true, timezone: 'UTC' }
  }
  return resp.json() as Promise<AuthStatus>
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
	return jsonMutation<Server>('/api/v1/servers', {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

export async function patchServer(id: number, patch: Partial<NewServerInput>): Promise<Server> {
	return jsonMutation<Server>(`/api/v1/servers/${id}`, {
    method: 'PATCH',
    body: JSON.stringify(patch),
  })
}

export async function deleteServer(id: number): Promise<void> {
	const resp = await apiFetch(`/api/v1/servers/${id}`, { method: 'DELETE' })
	if (!resp.ok) throw await responseError(resp)
}

export interface TestConnectionResult {
  reachable: boolean
  latency_ms: number
  error?: string
  error_kind?: 'auth' | 'dial' | 'timeout' | 'host_key' | 'key' | 'unknown'
}

export async function testServerConnection(id: number): Promise<TestConnectionResult> {
	return jsonMutation<TestConnectionResult>(`/api/v1/servers/${id}/test-connection`, { method: 'POST' })
}

export async function testDraftServerConnection(
  input: Pick<NewServerInput, 'hostname' | 'ssh_user' | 'ssh_port' | 'ssh_key_path'>,
): Promise<TestConnectionResult> {
	return jsonMutation<TestConnectionResult>('/api/v1/servers/test-connection', {
    method: 'POST',
    body: JSON.stringify({
      hostname: input.hostname,
      ssh_user: input.ssh_user,
      ssh_port: input.ssh_port,
      ssh_key_path: input.ssh_key_path,
    }),
  })
}

// --- Local monitor settings -------------------------------------------

export interface HTTPServerSettings {
  listen_addr: string
  read_timeout_seconds: number
  write_timeout_seconds: number
  trusted_proxy_cidrs: string[]
}

export interface DatabaseSettings {
  path: string
}

export interface SSHSettings {
  dial_timeout_seconds: number
  command_timeout_seconds: number
  max_connections_per_host: number
  known_hosts_path: string
}

export interface LogSettings {
  level: 'debug' | 'info' | 'warn' | 'error'
  format: 'json' | 'text'
}

export interface MetricsSettings {
  vm_url: string
  push_timeout_seconds: number
  query_timeout_seconds: number
}

export interface LogsSettings {
  loki_url: string
  push_timeout_seconds: number
  query_timeout_seconds: number
}

export interface SchedulerSettings {
  default_interval_seconds: number
  max_parallel: number
  per_job_timeout_seconds: number
}

export interface PublicProbeTargetSettings {
  server: string
  bench: string
  site: string
  url: string
  disabled: boolean
}

export interface PublicProbeSettings {
  enabled: boolean
  auto_discover: boolean
  default_scheme: 'http' | 'https'
  path: string
  timeout_seconds: number
  max_parallel: number
  allow_private_targets: boolean
  require_pong: boolean
  targets: PublicProbeTargetSettings[]
}

export interface StreamingFileSettings {
  server_id: number
  id: string
  path: string
  log_type: string
  bench: string
  site: string
}

export interface StreamingSettings {
  enabled: boolean
  monitor_id: string
  script_path: string
  files: StreamingFileSettings[]
  flush_interval_seconds: number
  max_batch_lines: number
  max_buffered_lines: number
  max_buffered_bytes: number
  push_timeout_seconds: number
  min_backoff_seconds: number
  max_backoff_seconds: number
}

export interface SettingsResponse {
  server: HTTPServerSettings
  display: { timezone: string }
  database: DatabaseSettings
  ssh: SSHSettings
  log: LogSettings
  metrics: MetricsSettings
  logs: LogsSettings
  scheduler: SchedulerSettings
  public_probes: PublicProbeSettings
  auth: { realm: string; password_configured: boolean }
  alerts: {
    enabled: boolean
    evaluation_interval_seconds: number
    notify_repeat_seconds: number
    vm_query_timeout_seconds: number
    telegram: {
      chat_ids: string[]
      send_timeout_seconds: number
      bot_token_configured: boolean
    }
    rules: AlertRule[]
    disable_defaults: boolean
    bundled_rule_count: number
  }
  streaming: StreamingSettings
  restart_required: boolean
}

export interface SettingsUpdate {
  server: HTTPServerSettings
  display: { timezone: string }
  database: DatabaseSettings
  ssh: SSHSettings
  log: LogSettings
  metrics: MetricsSettings
  logs: LogsSettings
  scheduler: SchedulerSettings
  public_probes: PublicProbeSettings
  auth: { realm: string; password?: string }
  alerts: {
    enabled: boolean
    evaluation_interval_seconds: number
    notify_repeat_seconds: number
    vm_query_timeout_seconds: number
    telegram: { chat_ids: string[]; send_timeout_seconds: number; bot_token?: string }
    rules: AlertRule[]
    disable_defaults: boolean
  }
  streaming: StreamingSettings
}

export interface IntegrationTestResult {
  ok: boolean
  latency_ms: number
  message: string
}

export interface RuntimeStatus {
  started_at: string
  uptime_seconds: number
  goroutines: number
  heap_alloc_bytes: number
  heap_sys_bytes: number
  streaming: {
    enabled: boolean
    sessions: number
    connected: number
    launching: number
    buffered_lines: number
    buffered_bytes: number
    max_buffered_lines: number
    max_buffered_bytes: number
  }
}

export function fetchRuntimeStatus(): Promise<RuntimeStatus> {
  return jsonGET<RuntimeStatus>('/api/v1/status')
}

export function fetchSettings(): Promise<SettingsResponse> {
  return jsonGET<SettingsResponse>('/api/v1/settings')
}

export function saveSettings(input: SettingsUpdate): Promise<SettingsResponse> {
	return jsonMutation<SettingsResponse>('/api/v1/settings', {
    method: 'PUT',
    body: JSON.stringify(input),
  })
}

export function testIntegration(kind: 'metrics' | 'logs' | 'telegram', botToken?: string): Promise<IntegrationTestResult> {
	return jsonMutation<IntegrationTestResult>('/api/v1/settings/test', {
    method: 'POST',
    body: JSON.stringify({ kind, ...(botToken ? { bot_token: botToken } : {}) }),
  })
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
	if (!resp.ok) throw await responseError(resp)
  return (await resp.json()) as SystemSnapshotResp
}

export async function refreshSystemSnapshot(id: number): Promise<SystemSnapshotResp> {
	return jsonMutation<SystemSnapshotResp>(`/api/v1/servers/${id}/refresh-system`, { method: 'POST' })
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
	web_workers_configured: number
	background_workers_configured: number
  supervisor_running: number
  supervisor_total: number
  redis_queues: Record<string, number>
	workers_registered: number
	workers_busy: number
	workers_idle: number
	services: Array<{
		name: string
		kind: string
		queue?: string
		state: number
		pid: number
		cpu_percent: number
		memory_percent: number
		rss_bytes: number
		uptime_seconds: number
	}>
}

export interface SitePair {
  server: string
  bench: string
  site: string
  is_healthy: number
}

export interface SiteDetail {
  server: string
  bench: string
  site: string
  http_status_code: number
  http_response_ms: number
  is_healthy: number
	failure_reason_code: number
  internal_http_status_code: number
  internal_http_response_ms: number
  internal_is_healthy: number
  internal_failure_reason_code: number
  public_probe_available: boolean
  public_http_status_code: number
  public_http_response_ms: number
  public_is_healthy: number
  public_failure_reason_code: number
  public_tls_cert_expiry_timestamp_seconds: number
  native_monitor_enabled: number
  database_identities: Array<{ hash: string; kind: 'database' | 'user' }>
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
	incidents: AlertIncident[]
}

export interface AlertIncident {
	id: number
	rule_name: string
	fingerprint: string
	severity: string
	status: 'firing' | 'resolved'
	value: number
	labels: Record<string, string>
	started_at: string
	resolved_at?: string
	last_notified_at?: string
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
		start: (BigInt(Math.floor(range.from)) * 1_000_000_000n).toString(),
		end: (BigInt(Math.floor(range.to)) * 1_000_000_000n).toString(),
    limit: String(limit),
    direction: 'backward',
  })
  return jsonGET<LogsQueryResponse>(`/api/v1/logs/query?${params.toString()}`)
}
