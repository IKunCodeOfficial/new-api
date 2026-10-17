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
import { ChevronDown } from 'lucide-react'
import { memo, type MouseEvent } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { toIntlLocale } from '@/i18n/languages'
import { getLobeIcon } from '@/lib/lobe-icon'
import { cn } from '@/lib/utils'

import {
  CURRENT_STATUS_LABEL_KEYS,
  EMPTY_BAR_CLASS,
  LIST_GRID_CLASS,
  STATUS_STYLES,
} from '../constants'
import { hideBucketTip, showBucketTip } from '../lib/bucket-tip-store'
import { formatDuration, formatRate, statusOf } from '../lib/status'
import type { ModelStatusModel } from '../types'
import { ModelRowDetail } from './model-row-detail'

type ModelRowProps = {
  model: ModelStatusModel
  hasDetail: boolean
  rowKey: string
  open: boolean
  // Must be identity-stable (a state setter) so memo skips unrelated rows.
  onExpandedChange: (key: string | null) => void
}

// Memoised: expanding one row or re-rendering its group card must not
// reconcile every other row's bar spans.
export const ModelRow = memo(function ModelRow(props: ModelRowProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const model = props.model
  const current = statusOf(model.current)
  const range = statusOf(model.availability)
  const idle = model.availability == null

  // One delegated handler for the whole timeline instead of one per bar.
  const handleTimelineOver = (e: MouseEvent<HTMLSpanElement>) => {
    const bar = (e.target as HTMLElement).closest<HTMLElement>('[data-bucket]')
    if (!bar) return
    const index = Number(bar.dataset.bucket)
    const rect = bar.getBoundingClientRect()
    showBucketTip({
      x: rect.left + rect.width / 2,
      y: rect.top,
      index,
      bucket: model.buckets[index] ?? null,
    })
  }

  return (
    <Collapsible
      open={props.open}
      onOpenChange={(open) =>
        props.onExpandedChange(open ? props.rowKey : null)
      }
      className='border-border/40 border-b'
    >
      <CollapsibleTrigger
        className={cn(
          LIST_GRID_CLASS,
          'hover:bg-muted/50 w-full cursor-pointer px-5 py-2.5 text-left transition-colors',
          current === 'danger' && 'bg-rose-500/5'
        )}
      >
        <span className='flex min-w-0 items-center gap-2.5'>
          <span className='bg-muted/70 flex size-8 shrink-0 items-center justify-center rounded-lg'>
            {getLobeIcon(model.vendor_icon || model.name, 18)}
          </span>
          <span className='flex min-w-0 flex-col gap-0.5'>
            <span className='truncate font-mono text-[13px] font-bold'>
              {model.name}
            </span>
            <span
              className={cn(
                'inline-flex items-center gap-1.5 text-xs font-medium',
                STATUS_STYLES[current].text
              )}
            >
              <span
                aria-hidden='true'
                className={cn(
                  'size-1.5 rounded-full',
                  STATUS_STYLES[current].dot
                )}
              />
              {t(CURRENT_STATUS_LABEL_KEYS[current])}
            </span>
          </span>
        </span>
        <span
          aria-hidden='true'
          className='flex h-7 min-w-0 items-stretch gap-px overflow-hidden'
          onMouseOver={handleTimelineOver}
          onMouseLeave={hideBucketTip}
        >
          {model.buckets.map((bucket, i) => (
            <span
              // eslint-disable-next-line react/no-array-index-key -- Buckets are fixed time slots; the slot position is their identity.
              key={i}
              data-bucket={i}
              className={cn(
                'min-w-0 flex-1 rounded-[2px] transition-opacity hover:opacity-65',
                bucket
                  ? STATUS_STYLES[statusOf(bucket.rate)].bar
                  : EMPTY_BAR_CLASS
              )}
            />
          ))}
        </span>
        <span
          className={cn(
            'text-right font-mono text-sm font-semibold tabular-nums',
            range === 'success' ? 'text-foreground' : STATUS_STYLES[range].text
          )}
        >
          {formatRate(model.availability, locale)}
        </span>
        <span className='text-right font-mono text-[13px] tabular-nums'>
          {formatDuration(idle ? null : model.ttft_avg_ms, locale)}
        </span>
        <span className='text-muted-foreground text-right font-mono text-[13px] tabular-nums'>
          {formatDuration(idle ? null : model.latency_avg_ms, locale)}
        </span>
        <span className='text-muted-foreground flex justify-end'>
          <ChevronDown
            className={cn(
              'size-3.5 transition-transform duration-200',
              props.open && 'rotate-180'
            )}
          />
        </span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <ModelRowDetail model={model} hasDetail={props.hasDetail} />
      </CollapsibleContent>
    </Collapsible>
  )
})
