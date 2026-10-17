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
import type { TFunction } from 'i18next'
import { useTranslation } from 'react-i18next'

import { toIntlLocale } from '@/i18n/languages'
import { cn } from '@/lib/utils'

import { STATUS_STYLES } from '../constants'
import { bannerSummary, problemChips, type BannerSummary } from '../lib/status'
import type { ModelStatusGroup, StatusRange } from '../types'

type StatusBannerProps = {
  groups: ModelStatusGroup[]
  range: StatusRange
  updatedAt: number
}

function bannerTitle(summary: BannerSummary, t: TFunction): string {
  if (summary.failing > 0 && summary.degraded > 0) {
    return summary.failing === 1
      ? t('1 model failing, {{degraded}} degraded', {
          degraded: summary.degraded,
        })
      : t('{{failing}} models failing, {{degraded}} degraded', {
          failing: summary.failing,
          degraded: summary.degraded,
        })
  }
  if (summary.failing > 0) {
    return summary.failing === 1
      ? t('1 model failing')
      : t('{{count}} models failing', { count: summary.failing })
  }
  if (summary.degraded > 0) {
    return summary.degraded === 1
      ? t('1 model degraded')
      : t('{{count}} models degraded', { count: summary.degraded })
  }
  if (summary.healthy === 0) return t('No recent requests')
  return t('All models operational')
}

export function StatusBanner(props: StatusBannerProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const summary = bannerSummary(props.groups)
  const chips = problemChips(props.groups)
  const style = STATUS_STYLES[summary.level]

  const refreshLabel: Record<StatusRange, string> = {
    '5m': t('every 10 seconds'),
    '1h': t('every 30 seconds'),
    '24h': t('every 5 minutes'),
    '7d': t('every 30 minutes'),
    '30d': t('every hour'),
  }
  const updated = new Intl.DateTimeFormat(locale, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  }).format(props.updatedAt * 1000)

  return (
    <section className='bg-card/70 mb-4 overflow-hidden rounded-xl border'>
      <div className='flex flex-wrap items-center justify-between gap-3 px-5 py-4'>
        <div className='flex min-w-0 items-center gap-3'>
          <span className='relative inline-flex size-2.5 shrink-0'>
            <span
              aria-hidden='true'
              className={cn(
                'absolute inset-0 rounded-full opacity-70 motion-safe:animate-[ping_1.8s_cubic-bezier(0,0,0.2,1)_infinite]',
                style.dot
              )}
            />
            <span
              aria-hidden='true'
              className={cn('relative size-2.5 rounded-full', style.dot)}
            />
          </span>
          <div className='flex min-w-0 flex-col gap-0.5'>
            <span className={cn('text-[15px] font-semibold', style.text)}>
              {bannerTitle(summary, t)}
            </span>
            <span className='text-muted-foreground text-xs'>
              {t(
                '{{healthy}} / {{total}} group models healthy · Updated {{time}} · Auto-refresh {{interval}}',
                {
                  healthy: summary.healthy,
                  total: summary.total,
                  time: updated,
                  interval: refreshLabel[props.range],
                }
              )}
            </span>
          </div>
        </div>
        {chips.length > 0 && (
          <div className='flex flex-wrap items-center gap-1.5'>
            {chips.map((chip) => (
              <span
                key={`${chip.group}|${chip.model}`}
                className='bg-background inline-flex h-6 items-center gap-1.5 rounded-md border px-2 text-xs'
              >
                <span
                  aria-hidden='true'
                  className={cn(
                    'size-1.5 rounded-full',
                    STATUS_STYLES[chip.level].dot
                  )}
                />
                <span className='font-mono font-semibold'>{chip.model}</span>
                <span className='text-muted-foreground'>{chip.group}</span>
              </span>
            ))}
          </div>
        )}
      </div>
    </section>
  )
}
