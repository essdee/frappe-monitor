import { ref } from 'vue'

export const displayTimezone = ref('UTC')

export function setDisplayTimezone(timezone: string | undefined) {
  const candidate = timezone?.trim() || 'UTC'
  try {
    new Intl.DateTimeFormat('en', { timeZone: candidate }).format(0)
    displayTimezone.value = candidate
  } catch {
    displayTimezone.value = 'UTC'
  }
}

export function formatDateTime(
  value: string | number | Date,
  options: Intl.DateTimeFormatOptions = {},
): string {
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: 'medium',
    timeStyle: 'medium',
    timeZone: displayTimezone.value,
    ...options,
  }).format(new Date(value))
}

export function formatDate(value: string | number | Date): string {
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: 'medium',
    timeZone: displayTimezone.value,
  }).format(new Date(value))
}

export function formatChartTime(timestamp: number, spanMs: number): string {
  const options: Intl.DateTimeFormatOptions = spanMs >= 86_400_000
    ? { month: 'short', day: 'numeric' }
    : { hour: '2-digit', minute: '2-digit' }
  return new Intl.DateTimeFormat(undefined, {
    ...options,
    timeZone: displayTimezone.value,
  }).format(timestamp)
}

export function formatLogTimestamp(timestamp: number): string {
  const base = new Intl.DateTimeFormat(undefined, {
    month: 'short',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    timeZone: displayTimezone.value,
  }).format(timestamp)
  return `${base}.${String(new Date(timestamp).getUTCMilliseconds()).padStart(3, '0')}`
}
