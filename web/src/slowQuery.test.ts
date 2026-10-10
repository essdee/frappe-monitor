import { describe, expect, it } from 'vitest'
import { formatSlowQueryDuration, parseSlowQueryEvent } from './slowQuery'

describe('parseSlowQueryEvent', () => {
  it('parses a safe correlated slow-query summary', () => {
    expect(parseSlowQueryEvent('{"engine":"mysql","operation":"select","duration_seconds":4.25,"lock_seconds":0.1,"rows_sent":1,"rows_examined":99,"trace_id":"trace-1"}')).toEqual({
      engine: 'mysql', operation: 'select', durationSeconds: 4.25,
      lockSeconds: 0.1, rowsSent: 1, rowsExamined: 99, traceID: 'trace-1',
    })
  })

  it('rejects raw and malformed log lines', () => {
    expect(parseSlowQueryEvent('SELECT * FROM tabUser')).toBeNull()
    expect(parseSlowQueryEvent('{"engine":"mysql","duration_seconds":-1}')).toBeNull()
  })

  it('formats millisecond and second durations', () => {
    expect(formatSlowQueryDuration(0.125)).toBe('125 ms')
    expect(formatSlowQueryDuration(4.25)).toBe('4.25 s')
  })
})
