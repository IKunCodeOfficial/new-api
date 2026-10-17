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
import type { StatusLevel } from './lib/status'
import type { StatusRange } from './types'

export const REFRESH_MS: Record<StatusRange, number> = {
  '5m': 10_000,
  '1h': 30_000,
  '24h': 300_000,
  '7d': 1_800_000,
  '30d': 3_600_000,
}

// labelKey is an i18n key; translate with t(labelKey) at render time.
export const RANGES: { value: StatusRange; labelKey: string }[] = [
  { value: '5m', labelKey: '5 min' },
  { value: '1h', labelKey: '1 hour' },
  { value: '24h', labelKey: '24 hours' },
  { value: '7d', labelKey: '7 days' },
  { value: '30d', labelKey: '30 days' },
]

export type StatusView = 'list' | 'matrix'

// Group filter value meaning "every group"; also the URL default.
export const ALL_GROUPS = 'all'

// Shared by the list column header and every model row so columns line up.
export const LIST_GRID_CLASS =
  'grid grid-cols-[236px_minmax(260px,1fr)_84px_92px_92px_28px] items-center gap-4'

export const EMPTY_BAR_CLASS =
  'bg-[oklch(0.93_0.008_255)] dark:bg-[oklch(0.33_0.016_265)]'

// Tailwind classes per status level: dot and bar fills, text colour, and the
// tinted background + border used by non-healthy matrix cells.
export const STATUS_STYLES: Record<
  StatusLevel,
  { dot: string; bar: string; text: string; cell: string }
> = {
  success: {
    dot: 'bg-emerald-500',
    bar: 'bg-emerald-500',
    text: 'text-emerald-700 dark:text-emerald-400',
    cell: 'bg-background border-border',
  },
  warning: {
    dot: 'bg-amber-500',
    bar: 'bg-amber-500',
    text: 'text-amber-700 dark:text-amber-400',
    cell: 'bg-amber-500/9 border-amber-500/35',
  },
  danger: {
    dot: 'bg-rose-500',
    bar: 'bg-rose-500',
    text: 'text-rose-700 dark:text-rose-400',
    cell: 'bg-rose-500/9 border-rose-500/35',
  },
  neutral: {
    dot: 'bg-slate-400',
    bar: EMPTY_BAR_CLASS,
    text: 'text-muted-foreground',
    cell: 'bg-background border-border',
  },
}

// labelKey values are i18n keys; translate with t() at render time.
export const CURRENT_STATUS_LABEL_KEYS: Record<StatusLevel, string> = {
  success: 'Healthy now',
  warning: 'Degraded now',
  danger: 'Failing now',
  neutral: 'No requests',
}
