export interface SlowQueryEvent {
  engine: string
  operation: string
  durationSeconds: number
  lockSeconds: number
  rowsSent: number
  rowsExamined: number
  traceID: string
}

function finiteNumber(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : null
}

export function parseSlowQueryEvent(line: string): SlowQueryEvent | null {
  try {
    const value = JSON.parse(line) as Record<string, unknown>
    const durationSeconds = finiteNumber(value.duration_seconds)
    const lockSeconds = finiteNumber(value.lock_seconds)
    const rowsSent = finiteNumber(value.rows_sent)
    const rowsExamined = finiteNumber(value.rows_examined)
    if (typeof value.engine !== 'string' || typeof value.operation !== 'string' || durationSeconds === null || lockSeconds === null || rowsSent === null || rowsExamined === null) return null
    return {
      engine: value.engine,
      operation: value.operation,
      durationSeconds,
      lockSeconds,
      rowsSent,
      rowsExamined,
      traceID: typeof value.trace_id === 'string' ? value.trace_id : '',
    }
  } catch {
    return null
  }
}

export function formatSlowQueryDuration(seconds: number): string {
  return seconds < 1 ? `${(seconds * 1000).toFixed(0)} ms` : `${seconds.toFixed(2)} s`
}
