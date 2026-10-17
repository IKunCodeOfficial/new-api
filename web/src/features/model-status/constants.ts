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
