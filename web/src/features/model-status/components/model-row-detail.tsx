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
import { Fragment } from 'react'
import { useTranslation } from 'react-i18next'

import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import { EMPTY_BAR_CLASS } from '../constants'
import { EMPTY_VALUE, formatDuration, formatRate } from '../lib/status'
import type { ModelStatusModel } from '../types'

type ModelRowDetailProps = {
  model: ModelStatusModel
  hasDetail: boolean
}

const PANEL_CLASS = 'bg-background rounded-lg border px-3.5 py-3'
const PANEL_TITLE_CLASS = 'text-muted-foreground mb-2.5 text-xs font-medium'

export function ModelRowDetail(props: ModelRowDetailProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  // A model without traffic in the window may still carry a zero-filled
  // detail block; those zeros are not real measurements.
  const detail = props.model.availability == null ? null : props.model.detail
  const failShare = detail?.fail_share ?? null

  const percentiles = [
    { key: 'P50', ttft: detail?.ttft_p50, latency: detail?.latency_p50 },
    { key: 'P95', ttft: detail?.ttft_p95, latency: detail?.latency_p95 },
    { key: 'P99', ttft: detail?.ttft_p99, latency: detail?.latency_p99 },
  ]
  const reasons = [
    { key: '5xx', label: t('Upstream 5xx'), color: 'bg-rose-500' },
    { key: '429', label: t('Rate limit 429'), color: 'bg-amber-500' },
    { key: 'timeout', label: t('Timeout'), color: 'bg-teal-600' },
    { key: 'other', label: t('Other'), color: 'bg-slate-400' },
  ]
  const tps =
    props.model.availability != null && props.model.tps > 0
      ? `${formatNumber(Math.round(props.model.tps), locale)} tok/s`
      : EMPTY_VALUE

  return (
    <div className='grid grid-cols-3 gap-3 pt-1 pr-5 pb-4 pl-[62px]'>
      {props.hasDetail && (
        <div className={PANEL_CLASS}>
          <div className={PANEL_TITLE_CLASS}>{t('Latency percentiles')}</div>
          <div className='grid grid-cols-[auto_1fr_1fr] gap-x-3 gap-y-1.5 text-xs tabular-nums'>
            <span />
            <span className='text-muted-foreground text-right'>
              {t('First token')}
            </span>
            <span className='text-muted-foreground text-right'>
              {t('Total time')}
            </span>
            {percentiles.map((p) => (
              <Fragment key={p.key}>
                <span className='text-muted-foreground'>{p.key}</span>
                <span className='text-right font-mono font-semibold'>
                  {formatDuration(p.ttft, locale)}
                </span>
                <span className='text-right font-mono font-semibold'>
                  {formatDuration(p.latency, locale)}
                </span>
              </Fragment>
            ))}
          </div>
        </div>
      )}
      {props.hasDetail && (
        <div className={PANEL_CLASS}>
          <div className={PANEL_TITLE_CLASS}>{t('Failure reasons')}</div>
          <div
            className={cn(
              'mb-2.5 flex h-1.5 overflow-hidden rounded-full',
              EMPTY_BAR_CLASS
            )}
          >
            {reasons.map((r) => (
              <span
                key={r.key}
                className={r.color}
                style={{ width: `${(failShare?.[r.key] ?? 0) * 100}%` }}
              />
            ))}
          </div>
          <div className='grid grid-cols-2 gap-x-3 gap-y-1 text-xs'>
            {reasons.map((r) => (
              <span key={r.key} className='flex min-w-0 items-center gap-1.5'>
                <span
                  aria-hidden='true'
                  className={cn('size-1.5 shrink-0 rounded-[2px]', r.color)}
                />
                <span className='text-muted-foreground'>{r.label}</span>
                <span className='ml-auto font-mono tabular-nums'>
                  {failShare
                    ? formatRate(failShare[r.key] ?? 0, locale, 0)
                    : EMPTY_VALUE}
                </span>
              </span>
            ))}
          </div>
        </div>
      )}
      <div className={PANEL_CLASS}>
        <div className={PANEL_TITLE_CLASS}>{t('Throughput')}</div>
        <div className='flex justify-between text-xs tabular-nums'>
          <span className='text-muted-foreground'>{t('Output speed')}</span>
          <span className='font-mono font-semibold'>{tps}</span>
        </div>
      </div>
    </div>
  )
}
