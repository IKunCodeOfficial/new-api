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
import { getLobeIcon } from '@/lib/lobe-icon'
import { cn } from '@/lib/utils'

import { CURRENT_STATUS_LABEL_KEYS, STATUS_STYLES } from '../constants'
import {
  EMPTY_VALUE,
  formatDuration,
  formatRate,
  statusOf,
} from '../lib/status'
import type { ModelStatusGroup, ModelStatusModel } from '../types'

// Group columns never shrink below GROUP_COLUMN_PX: with many groups the
// matrix scrolls sideways while the model column and the header stay pinned.
const MODEL_COLUMN_PX = 220
const GROUP_COLUMN_PX = 112
const COLUMN_GAP_PX = 8
const ROW_PADDING_PX = 20

type StatusMatrixProps = {
  groups: ModelStatusGroup[]
  query: string
}

export function StatusMatrix(props: StatusMatrixProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const query = props.query.trim().toLowerCase()
  const columns = `${MODEL_COLUMN_PX}px repeat(${props.groups.length}, minmax(${GROUP_COLUMN_PX}px, 1fr))`
  const minWidth =
    MODEL_COLUMN_PX +
    props.groups.length * (GROUP_COLUMN_PX + COLUMN_GAP_PX) +
    ROW_PADDING_PX

  // Rows follow first appearance across groups, which keeps the backend's
  // volume order; each group is indexed by model name for the cell lookup.
  const rows = new Map<string, ModelStatusModel>()
  const byGroup = props.groups.map((g) => {
    const index = new Map<string, ModelStatusModel>()
    for (const m of g.models) {
      index.set(m.name, m)
      if (!rows.has(m.name) && m.name.toLowerCase().includes(query)) {
        rows.set(m.name, m)
      }
    }
    return index
  })

  return (
    <section className='bg-card overflow-hidden rounded-xl border'>
      <div
        role='table'
        aria-label={t('Model / Group')}
        className='max-h-[75vh] overflow-auto overscroll-x-contain'
      >
        <div style={{ minWidth }}>
          <div
            role='row'
            className='bg-muted border-border/60 sticky top-0 z-20 grid gap-2 border-b pr-5'
            style={{ gridTemplateColumns: columns }}
          >
            <span
              role='columnheader'
              className='bg-muted text-muted-foreground/70 sticky left-0 z-10 flex items-end py-3 pl-5 text-[11px] font-medium'
            >
              {t('Model / Group')}
            </span>
            {props.groups.map((g) => (
              <div
                key={g.key}
                role='columnheader'
                title={g.desc ? `${g.key}\n${g.desc}` : g.key}
                className='flex min-w-0 flex-col gap-0.5 py-3'
              >
                <span className='flex min-w-0 items-baseline gap-1.5'>
                  <span className='truncate font-mono text-[13px] font-bold'>
                    {g.key}
                  </span>
                  <span className='text-muted-foreground shrink-0 font-mono text-[11px] tabular-nums'>
                    {formatNumber(g.ratio, locale)}x
                  </span>
                </span>
                <span className='text-muted-foreground line-clamp-2 min-h-[2lh] text-[11px] leading-snug break-words'>
                  {g.desc}
                </span>
              </div>
            ))}
          </div>
          {[...rows.values()].map((row) => (
            <div
              key={row.name}
              role='row'
              className='border-border/40 grid gap-2 border-b pr-5'
              style={{ gridTemplateColumns: columns }}
            >
              <div
                role='rowheader'
                title={row.name}
                className='bg-card sticky left-0 z-10 flex min-w-0 items-center gap-2.5 py-2 pl-5'
              >
                <div className='bg-muted/70 flex size-7 shrink-0 items-center justify-center rounded-lg'>
                  {getLobeIcon(row.vendor_icon || row.name, 16)}
                </div>
                <span className='truncate font-mono text-[13px] font-bold'>
                  {row.name}
                </span>
              </div>
              {props.groups.map((g, gi) => {
                const cell = byGroup[gi].get(row.name)
                if (!cell) {
                  return (
                    <div
                      key={g.key}
                      role='cell'
                      title={t('{{group}} does not offer this model', {
                        group: g.key,
                      })}
                      className='border-border/50 text-muted-foreground/40 my-2 flex h-12 items-center justify-center rounded-lg border border-dashed'
                    >
                      <span aria-hidden='true' className='font-mono text-sm'>
                        {EMPTY_VALUE}
                      </span>
                      <span className='sr-only'>{t('Not offered')}</span>
                    </div>
                  )
                }
                const current = statusOf(cell.current)
                const range = statusOf(cell.availability)
                return (
                  <div
                    key={g.key}
                    role='cell'
                    title={`${row.name} @ ${g.key} · ${t(CURRENT_STATUS_LABEL_KEYS[current])}`}
                    className={cn(
                      'my-2 flex h-12 min-w-0 flex-col justify-center gap-0.5 rounded-lg border px-2.5',
                      STATUS_STYLES[current].cell
                    )}
                  >
                    <span className='flex items-center gap-1.5 whitespace-nowrap'>
                      <span
                        aria-hidden='true'
                        className={cn(
                          'size-1.5 shrink-0 rounded-full',
                          STATUS_STYLES[current].dot
                        )}
                      />
                      <span
                        className={cn(
                          'font-mono text-[13px] leading-5 font-semibold tabular-nums',
                          range === 'success'
                            ? 'text-foreground'
                            : STATUS_STYLES[range].text
                        )}
                      >
                        {formatRate(cell.availability, locale)}
                      </span>
                    </span>
                    <span className='text-muted-foreground pl-3 font-mono text-[11px] leading-4 whitespace-nowrap tabular-nums'>
                      {formatDuration(
                        cell.availability == null ? null : cell.ttft_avg_ms,
                        locale
                      )}
                    </span>
                  </div>
                )
              })}
            </div>
          ))}
        </div>
      </div>
      {rows.size === 0 && (
        <div className='text-muted-foreground px-5 py-7 text-center text-[13px]'>
          {t('No matching models')}
        </div>
      )}
    </section>
  )
}
