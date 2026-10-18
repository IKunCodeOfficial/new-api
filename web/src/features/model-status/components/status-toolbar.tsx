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
import { Grid2X2, RotateCw, Rows3, Search } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { SegmentedControl } from '@/components/segmented-control'
import { Button } from '@/components/ui/button'
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from '@/components/ui/input-group'
import { cn } from '@/lib/utils'

import { ALL_GROUPS, RANGES, type StatusView } from '../constants'
import type { StatusRange } from '../types'

type StatusToolbarProps = {
  range: StatusRange
  onRangeChange: (range: StatusRange) => void
  group: string
  groupKeys: string[]
  onGroupChange: (group: string) => void
  query: string
  onQueryChange: (query: string) => void
  view: StatusView
  onViewChange: (view: StatusView) => void
  refreshing: boolean
  onRefresh: () => void
}

export function StatusToolbar(props: StatusToolbarProps) {
  const { t } = useTranslation()

  return (
    <div className='bg-background/85 sticky top-[72px] z-20 mb-4 rounded-xl border p-3 backdrop-blur-lg'>
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <div className='flex min-w-0 flex-wrap items-center gap-2'>
          <SegmentedControl
            value={props.range}
            onValueChange={props.onRangeChange}
            ariaLabel={t('Time range')}
            options={RANGES.map((r) => ({
              value: r.value,
              label: t(r.labelKey),
            }))}
          />
          {/* Many groups wrap onto further rows instead of one long strip. */}
          <SegmentedControl
            wrap
            value={props.group}
            onValueChange={props.onGroupChange}
            ariaLabel={t('Group')}
            options={[
              { value: ALL_GROUPS, label: t('All groups') },
              ...props.groupKeys.map((key) => ({ value: key, label: key })),
            ]}
          />
        </div>
        <div className='flex flex-wrap items-center gap-2'>
          <InputGroup className='bg-background h-8 w-[220px] rounded-md'>
            <InputGroupAddon>
              <Search className='text-muted-foreground/60 size-3.5' />
            </InputGroupAddon>
            <InputGroupInput
              value={props.query}
              onChange={(e) => props.onQueryChange(e.target.value)}
              placeholder={t('Search model name')}
              aria-label={t('Search models')}
              className='placeholder:text-muted-foreground/50 text-[13px]'
            />
          </InputGroup>
          <SegmentedControl
            value={props.view}
            onValueChange={props.onViewChange}
            ariaLabel={t('View mode')}
            options={[
              {
                value: 'list',
                label: (
                  <>
                    <Rows3 className='size-3.5' />
                    {t('List')}
                  </>
                ),
              },
              {
                value: 'matrix',
                label: (
                  <>
                    <Grid2X2 className='size-3.5' />
                    {t('Matrix')}
                  </>
                ),
              },
            ]}
          />
          <Button
            variant='outline'
            size='icon'
            onClick={props.onRefresh}
            aria-label={t('Refresh')}
            className='text-muted-foreground hover:text-foreground size-8 rounded-md bg-transparent'
          >
            <RotateCw
              className={cn(
                'size-3.5',
                props.refreshing && 'motion-safe:animate-spin'
              )}
            />
          </Button>
        </div>
      </div>
    </div>
  )
}
