import { describe, expect, it } from 'vitest'
import { defaultStepFor, resolveTimeRange } from './useTimeRange'

describe('resolveTimeRange', () => {
  it('moves a preset window forward with wall-clock time', () => {
    const first = resolveTimeRange({ preset: '1h' }, 10_000)
    const second = resolveTimeRange({ preset: '1h' }, 10_030)

    expect(first).toEqual({ from: 6_400, to: 10_000, step: 15 })
    expect(second).toEqual({ from: 6_430, to: 10_030, step: 15 })
  })

  it('keeps an explicit valid range fixed', () => {
    expect(resolveTimeRange({ from: '100', to: '700', step: '30' }, 99_999))
      .toEqual({ from: 100, to: 700, step: 30 })
  })

  it('falls back safely for an invalid range and step', () => {
    expect(resolveTimeRange({ from: '700', to: '100', step: '-1' }, 10_000))
      .toEqual({ from: 6_400, to: 10_000, step: 15 })
  })
})

describe('defaultStepFor', () => {
  it.each([
    [3_600, 15],
    [21_600, 60],
    [86_400, 300],
    [604_800, 1_800],
  ])('uses a useful resolution for span %s', (span, step) => {
    expect(defaultStepFor(span)).toBe(step)
  })
})
