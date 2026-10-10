import { afterEach, describe, expect, it } from 'vitest'
import { displayTimezone, formatChartTime, setDisplayTimezone } from './timeDisplay'

afterEach(() => setDisplayTimezone('UTC'))

describe('configured display timezone', () => {
  it('changes human-facing timestamps without changing the input instant', () => {
    const instant = Date.UTC(2026, 7, 14, 0, 0, 0)
    setDisplayTimezone('UTC')
    const utc = formatChartTime(instant, 60_000)
    setDisplayTimezone('Asia/Kolkata')
    const india = formatChartTime(instant, 60_000)

    expect(displayTimezone.value).toBe('Asia/Kolkata')
    expect(india).not.toBe(utc)
  })

  it('falls back safely when a browser does not recognize a timezone', () => {
    setDisplayTimezone('Mars/Olympus')
    expect(displayTimezone.value).toBe('UTC')
  })
})
