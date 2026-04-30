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

export function queryMetrics(query: string, range: TimeRange): Promise<MetricsQueryResponse> {
  const params = new URLSearchParams({
    query,
    start: String(range.from),
    end: String(range.to),
    step: String(range.step),
  })
  return jsonGET<MetricsQueryResponse>(`/api/v1/metrics/query?${params.toString()}`)
}
