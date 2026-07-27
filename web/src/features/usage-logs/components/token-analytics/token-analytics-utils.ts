import dayjs from '@/lib/dayjs'

export function normalizeNumber(value: unknown): number {
  const num = Number(value)
  return Number.isFinite(num) ? num : 0
}

export function secondsFromDate(value: Date): number {
  return Math.floor(value.getTime() / 1000)
}

export function formatBucketLabel(timestamp: number, granularity: 'hour' | 'day') {
  if (!timestamp || timestamp <= 0) return '-'
  return granularity === 'hour'
    ? dayjs.unix(timestamp).format('MM-DD HH:00')
    : dayjs.unix(timestamp).format('YYYY-MM-DD')
}
