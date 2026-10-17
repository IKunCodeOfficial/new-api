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
import { useTranslation } from 'react-i18next'

import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import { LIST_GRID_CLASS, STATUS_STYLES } from '../constants'
import {
  bannerSummary,
  formatRate,
  sortModels,
  statusOf,
  type StatusLevel,
} from '../lib/status'
import type { ModelStatusGroup, StatusRange } from '../types'
import { ModelRow } from './model-row'

type GroupCardProps = {
  group: ModelStatusGroup
  range: StatusRange
  hasDetail: boolean
  query: string
  expandedKey: string | null
  onExpandedChange: (key: string | null) => void
}

export function GroupCard(props: GroupCardProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const group = props.group
  const summary = bannerSummary([group])
  const groupLevel = statusOf(group.availability)
  const query = props.query.trim().toLowerCase()
  const models = sortModels(group.models).filter((m) =>
    m.name.toLowerCase().includes(query)
  )

  const axisStart: Record<StatusRange, string> = {
    '5m': t('5 minutes ago'),
    '1h': t('1 hour ago'),
    '24h': t('24 hours ago'),
    '7d': t('7 days ago'),
    '30d': t('30 days ago'),
  }

  let issueLevel: StatusLevel =
    summary.level === 'neutral' ? 'success' : summary.level
  let issueText = t('All healthy')
  if (summary.failing > 0 && summary.degraded > 0) {
    issueText = t('{{failing}} failing · {{degraded}} degraded', {
      failing: summary.failing,
      degraded: summary.degraded,
    })
  } else if (summary.failing > 0) {
    issueText = t('{{count}} failing', { count: summary.failing })
  } else if (summary.degraded > 0) {
    issueText = t('{{count}} degraded', { count: summary.degraded })
  } else if (summary.healthy === 0) {
    issueLevel = 'neutral'
    issueText = t('No requests')
  }

  return (
    <section className='bg-card/60 overflow-hidden rounded-xl border'>
      <div className='overflow-x-auto overflow-y-hidden'>
        <div className='min-w-[960px]'>
          <div className='bg-muted/30 border-border/60 flex items-center justify-between gap-3 border-b px-5 py-3'>
            <div className='flex min-w-0 items-baseline gap-2.5'>
              <h2 className='font-mono text-[15px] font-bold'>{group.key}</h2>
              {group.desc && (
                <span className='text-muted-foreground text-[13px]'>
                  {group.desc}
                </span>
              )}
              <span className='text-muted-foreground/70 text-xs tabular-nums'>
                {t('Ratio {{ratio}}x · Models: {{count}}', {
                  ratio: formatNumber(group.ratio, locale),
                  count: group.models.length,
                })}
              </span>
            </div>
            <div className='flex items-center gap-4'>
              <span
                className={cn(
                  'inline-flex items-center gap-1.5 text-xs font-medium',
                  STATUS_STYLES[issueLevel].text
                )}
              >
                <span
                  aria-hidden='true'
                  className={cn(
                    'size-1.5 rounded-full',
                    STATUS_STYLES[issueLevel].dot
                  )}
                />
                {issueText}
              </span>
              <span
                className={cn(
                  'font-mono text-[15px] font-semibold tabular-nums',
                  STATUS_STYLES[groupLevel].text
                )}
              >
                {formatRate(group.availability, locale)}
              </span>
            </div>
          </div>
          <div
            className={cn(
              LIST_GRID_CLASS,
              'text-muted-foreground/70 border-border/40 border-b px-5 py-2 text-[11px] font-medium'
            )}
          >
            <span>{t('Model')}</span>
            <span className='flex justify-between'>
              <span>{axisStart[props.range]}</span>
              <span>{t('Now')}</span>
            </span>
            <span className='text-right'>{t('Availability')}</span>
            <span className='text-right'>{t('TTFT')}</span>
            <span className='text-right'>{t('Avg latency')}</span>
            <span />
          </div>
          {models.map((model) => {
            const key = `${group.key}|${model.name}`
            return (
              <ModelRow
                key={key}
                model={model}
                hasDetail={props.hasDetail}
                rowKey={key}
                open={props.expandedKey === key}
                onExpandedChange={props.onExpandedChange}
              />
            )
          })}
          {models.length === 0 && (
            <div className='text-muted-foreground px-5 py-7 text-center text-[13px]'>
              {t('No matching models')}
            </div>
          )}
        </div>
      </div>
    </section>
  )
}
