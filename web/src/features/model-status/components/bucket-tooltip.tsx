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
import dayjs from '@/lib/dayjs'
import { cn } from '@/lib/utils'

import { STATUS_STYLES } from '../constants'
import {
  bucketTimeFormat,
  formatDuration,
  formatRate,
  statusOf,
} from '../lib/status'
import type { ModelStatusBucket, StatusRange } from '../types'

// Viewport anchor (top-centre of the hovered bar) plus the bucket it shows.
export type BucketTip = {
  x: number
  y: number
  index: number
  bucket: ModelStatusBucket | null
}

type BucketTooltipProps = {
  tip: BucketTip
  range: StatusRange
  windowStart: number
  bucketSeconds: number
}

// One shared fixed-position tooltip for every timeline bar; per-bar Tooltip
// instances would mount thousands of popups on a busy page.
export function BucketTooltip(props: BucketTooltipProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const format = bucketTimeFormat(props.range)
  const start = props.windowStart + props.tip.index * props.bucketSeconds
  const end = start + props.bucketSeconds
  const bucket = props.tip.bucket
  const level = statusOf(bucket?.rate)

  return (
    <div
      role='tooltip'
      className='bg-card pointer-events-none fixed z-60 min-w-[168px] -translate-x-1/2 -translate-y-full rounded-md border px-2.5 py-2 text-xs shadow-md'
      style={{ left: props.tip.x, top: props.tip.y - 8 }}
    >
      <div className='mb-1.5 font-semibold tabular-nums'>
        {dayjs.unix(start).format(format)} – {dayjs.unix(end).format(format)}
      </div>
      <div className='flex justify-between gap-3'>
        <span className='text-muted-foreground'>{t('Success rate')}</span>
        <span
          className={cn(
            'font-mono font-semibold tabular-nums',
            STATUS_STYLES[level].text
          )}
        >
          {bucket ? formatRate(bucket.rate, locale) : t('No requests')}
        </span>
      </div>
      <div className='flex justify-between gap-3'>
        <span className='text-muted-foreground'>{t('TTFT')}</span>
        <span className='font-mono tabular-nums'>
          {formatDuration(bucket?.ttft_ms, locale)}
        </span>
      </div>
    </div>
  )
}
