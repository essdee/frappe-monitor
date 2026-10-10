import { describe, expect, it } from 'vitest'
import { formatMonitorBytes, formatMonitorDuration, parseNativeMonitorEvent } from './nativeMonitor'

describe('native Frappe monitor presentation', () => {
  it('parses a nested request without showing raw JSON as the primary view', () => {
    const event = parseNativeMonitorEvent(JSON.stringify({
      site: 'erp.local', transaction_type: 'request', duration: 125400, uuid: 'trace-1',
      request: { method: 'POST', path: '/api/method/erp.submit', status_code: 500, response_length: 2048 },
      exception: 'frappe.ValidationError',
    }))
    expect(event).toMatchObject({
      transactionType: 'request', site: 'erp.local', method: 'POST',
      path: '/api/method/erp.submit', statusCode: 500, durationMs: 125.4,
      responseBytes: 2048, exception: 'frappe.ValidationError', uuid: 'trace-1',
    })
    expect(event?.formatted).toContain('\n')
  })

  it('parses jobs and older flat request records', () => {
    expect(parseNativeMonitorEvent(JSON.stringify({
      site: 'erp.local', transaction_type: 'job', duration: 2_000_000,
      job: { method: 'erp.run', scheduled: true, wait: 250_000 },
    }))).toMatchObject({ method: 'erp.run', scheduled: true, durationMs: 2000, waitMs: 250 })
    expect(parseNativeMonitorEvent(JSON.stringify({
      site: 'legacy.local', transaction_type: 'request', duration: 1000,
      method: 'GET', path: '/api/method/ping', status_code: 200,
    }))).toMatchObject({ method: 'GET', path: '/api/method/ping', statusCode: 200 })
  })

  it('rejects non-monitor lines and formats units', () => {
    expect(parseNativeMonitorEvent('Traceback: failed')).toBeNull()
    expect(parseNativeMonitorEvent('{"transaction_type":"other"}')).toBeNull()
    expect(formatMonitorDuration(1250)).toBe('1.25 s')
    expect(formatMonitorBytes(2048)).toBe('2.0 KiB')
  })
})
