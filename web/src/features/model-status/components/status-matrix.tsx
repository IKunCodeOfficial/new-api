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

type StatusMatrixProps = {
  groups: ModelStatusGroup[]
  query: string
}

export function StatusMatrix(props: StatusMatrixProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const query = props.query.trim().toLowerCase()
  const columns = `236px repeat(${props.groups.length}, minmax(0, 1fr))`

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
    <section className='bg-card/60 overflow-hidden rounded-xl border'>
      <div className='overflow-x-auto overflow-y-hidden'>
        <div className='min-w-[860px]'>
          <div
            className='bg-muted/30 border-border/60 grid items-end gap-2 border-b px-5 py-3'
            style={{ gridTemplateColumns: columns }}
          >
            <span className='text-muted-foreground/70 text-[11px] font-medium'>
              {t('Model / Group')}
            </span>
            {props.groups.map((g) => (
              <div key={g.key} className='flex flex-col gap-0.5'>
                <span className='font-mono text-[13px] font-bold'>{g.key}</span>
                <span className='text-muted-foreground text-[11px]'>
                  {g.desc ? `${g.desc} · ` : ''}
                  {formatNumber(g.ratio, locale)}x
                </span>
              </div>
            ))}
          </div>
          {[...rows.values()].map((row) => (
            <div
              key={row.name}
              className='border-border/40 grid items-center gap-2 border-b px-5 py-2'
              style={{ gridTemplateColumns: columns }}
            >
              <div className='flex min-w-0 items-center gap-2.5'>
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
                      title={t('{{group}} does not offer this model', {
                        group: g.key,
                      })}
                      className='border-border/50 text-muted-foreground/50 flex h-12 items-center justify-between gap-2 rounded-lg border px-3'
                    >
                      <span className='font-mono text-sm font-semibold'>
                        {EMPTY_VALUE}
                      </span>
                      <span className='text-[11px] whitespace-nowrap'>
                        {t('Not offered')}
                      </span>
                    </div>
                  )
                }
                const current = statusOf(cell.current)
                const range = statusOf(cell.availability)
                return (
                  <div
                    key={g.key}
                    title={`${row.name} @ ${g.key} · ${t(CURRENT_STATUS_LABEL_KEYS[current])}`}
                    className={cn(
                      'flex h-12 items-center justify-between gap-2 rounded-lg border px-3',
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
                          'font-mono text-sm font-semibold tabular-nums',
                          range === 'success'
                            ? 'text-foreground'
                            : STATUS_STYLES[range].text
                        )}
                      >
                        {formatRate(cell.availability, locale)}
                      </span>
                    </span>
                    <span className='text-muted-foreground font-mono text-[11px] whitespace-nowrap tabular-nums'>
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
          {rows.size === 0 && (
            <div className='text-muted-foreground px-5 py-7 text-center text-[13px]'>
              {t('No matching models')}
            </div>
          )}
        </div>
      </div>
    </section>
  )
}
