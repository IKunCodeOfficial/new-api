/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { formatNumber } from '@/lib/format'

import type { ModelStatusGroup, ModelStatusModel, StatusRange } from '../types'

export type StatusLevel = 'success' | 'warning' | 'danger' | 'neutral'

export type ProblemChip = {
  model: string
  group: string
  level: StatusLevel
}

export type BannerSummary = {
  level: StatusLevel
  failing: number
  degraded: number
  healthy: number
  total: number
}

const SUCCESS_THRESHOLD = 0.99
const WARNING_THRESHOLD = 0.95

export function statusOf(rate: number | null | undefined): StatusLevel {
  if (rate == null) return 'neutral'
  if (rate >= SUCCESS_THRESHOLD) return 'success'
  if (rate >= WARNING_THRESHOLD) return 'warning'
  return 'danger'
}

const SEVERITY: Record<StatusLevel, number> = {
  danger: 0,
  warning: 1,
  success: 2,
  neutral: 3,
}

export function severityRank(level: StatusLevel): number {
  return SEVERITY[level]
}

// Array.prototype.sort is stable, so the backend's volume order is kept
// within each severity level.
export function sortModels(models: ModelStatusModel[]): ModelStatusModel[] {
  return [...models].sort(
    (a, b) =>
      severityRank(statusOf(a.current)) - severityRank(statusOf(b.current))
  )
}

export function problemChips(
  groups: ModelStatusGroup[],
  max = 5
): ProblemChip[] {
  const danger: ProblemChip[] = []
  const warning: ProblemChip[] = []
  for (const g of groups) {
    for (const m of g.models) {
      const level = statusOf(m.current)
      const chip = { model: m.name, group: g.key, level }
      if (level === 'danger') danger.push(chip)
      if (level === 'warning') warning.push(chip)
    }
  }
  return [...danger, ...warning].slice(0, max)
}

export function bucketTimeFormat(range: StatusRange): string {
  if (range === '5m') return 'HH:mm:ss'
  if (range === '1h' || range === '24h') return 'HH:mm'
  return 'MM-DD HH:mm'
}

export function bannerSummary(groups: ModelStatusGroup[]): BannerSummary {
  let failing = 0
  let degraded = 0
  let healthy = 0
  let total = 0
  for (const g of groups) {
    for (const m of g.models) {
      total++
      const level = statusOf(m.current)
      if (level === 'danger') failing++
      else if (level === 'warning') degraded++
      else if (level === 'success') healthy++
    }
  }
  let level: StatusLevel = 'neutral'
  if (failing > 0) level = 'danger'
  else if (degraded > 0) level = 'warning'
  else if (healthy > 0) level = 'success'
  return { level, failing, degraded, healthy, total }
}

export const EMPTY_VALUE = '—'

// Durations below one second render in whole milliseconds ("820 ms"),
// longer ones in seconds with one decimal ("1.4 s"). Zero or missing values
// mean "no data" and render as an em dash.
export function formatDuration(
  ms: number | null | undefined,
  locale?: string
): string {
  if (ms == null || !(ms > 0)) return EMPTY_VALUE
  if (ms < 1000) return `${formatNumber(Math.round(ms), locale)} ms`
  return `${formatNumber(Math.round(ms / 100) / 10, locale)} s`
}

// Availability is read at two-decimal precision (99.95%), so the digits are
// fixed instead of trimmed; formatPercent in @/lib/format trims trailing zeros
// and ignores the viewer's locale.
export function formatRate(
  rate: number | null | undefined,
  locale?: string,
  fractionDigits = 2
): string {
  if (rate == null || Number.isNaN(rate)) return EMPTY_VALUE
  return new Intl.NumberFormat(locale, {
    style: 'percent',
    minimumFractionDigits: fractionDigits,
    maximumFractionDigits: fractionDigits,
  }).format(rate)
}
