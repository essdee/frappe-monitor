export interface NativeMonitorEvent {
  transactionType: 'request' | 'job'
  site: string
  method: string
  path: string
  statusCode: number
  durationMs: number
  waitMs: number
  responseBytes: number
  scheduled: boolean
  exception: string
  uuid: string
  formatted: string
}

/** Parse current nested Frappe monitor events and older flat records. */
export function parseNativeMonitorEvent(line: string): NativeMonitorEvent | null {
  try {
    const value = JSON.parse(line) as Record<string, unknown>
    const transactionType = value.transaction_type
    if (transactionType !== 'request' && transactionType !== 'job') return null
    const nested = (value[transactionType] && typeof value[transactionType] === 'object'
      ? value[transactionType] : {}) as Record<string, unknown>
    return {
      transactionType,
      site: String(value.site ?? ''),
      method: String(nested.method ?? value.method ?? ''),
      path: String(nested.path ?? value.path ?? ''),
      statusCode: Number(nested.status_code ?? value.status_code ?? 0),
      durationMs: Number(value.duration ?? 0) / 1000,
      waitMs: Number(nested.wait ?? value.wait ?? 0) / 1000,
      responseBytes: Number(nested.response_length ?? value.response_length ?? 0),
      scheduled: Boolean(nested.scheduled ?? value.scheduled),
      exception: String(value.exception ?? ''),
      uuid: String(value.uuid ?? ''),
      formatted: JSON.stringify(value, null, 2),
    }
  } catch {
    return null
  }
}

export function formatMonitorDuration(ms: number) {
  return ms >= 1000 ? `${(ms / 1000).toFixed(2)} s` : `${ms.toFixed(1)} ms`
}

export function formatMonitorBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`
}
