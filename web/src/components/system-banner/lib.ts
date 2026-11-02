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
export type SystemBannerLevel = 'normal' | 'important'

export type SystemBannerStatus = {
  level: SystemBannerLevel
  text_zh: string
  text_en: string
  published_at: number
}

export const SYSTEM_BANNER_DISMISSED_KEY = 'system-banner-dismissed'

/** Chinese interface codes (zhCN, zhTW) get the Chinese text, all else English. */
export function pickSystemBannerText(
  banner: SystemBannerStatus,
  language: string | undefined
): string {
  return language?.startsWith('zh') ? banner.text_zh : banner.text_en
}

/**
 * Validate the `system_banner` field of a status payload. A stale or
 * malformed snapshot (for example from localStorage) yields `null`.
 */
export function parseSystemBanner(value: unknown): SystemBannerStatus | null {
  if (typeof value !== 'object' || value === null) return null
  const banner = value as Record<string, unknown>
  if (banner.level !== 'normal' && banner.level !== 'important') return null
  if (typeof banner.text_zh !== 'string') return null
  if (typeof banner.text_en !== 'string') return null
  if (typeof banner.published_at !== 'number') return null
  return {
    level: banner.level,
    text_zh: banner.text_zh,
    text_en: banner.text_en,
    published_at: banner.published_at,
  }
}
