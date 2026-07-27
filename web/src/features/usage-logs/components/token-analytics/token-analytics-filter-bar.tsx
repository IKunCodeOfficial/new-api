import { Search, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { CompactDateTimeRangePicker } from '../compact-date-time-range-picker'
import type {
  TokenAnalyticsSortBy,
  TokenAnalyticsSortOrder,
} from '../../types'

interface TokenAnalyticsFilters {
  keyword: string
  startTime?: number
  endTime?: number
  sortBy: TokenAnalyticsSortBy
  sortOrder: TokenAnalyticsSortOrder
}

interface TokenAnalyticsFilterBarProps {
  filters: TokenAnalyticsFilters
  onFiltersChange: (filters: Partial<TokenAnalyticsFilters>) => void
  onReset: () => void
  loading?: boolean
}

const sortOptions: Array<{ value: TokenAnalyticsSortBy; labelKey: string }> = [
  { value: 'quota_sum', labelKey: 'Sort by usage' },
  { value: 'request_count', labelKey: 'Sort by requests' },
  { value: 'last_used_at', labelKey: 'Sort by last used' },
  { value: 'token_created_time', labelKey: 'Sort by token creation' },
  { value: 'token_name', labelKey: 'Sort by token name' },
  { value: 'token_id', labelKey: 'Sort by token ID' },
]

const sortOrderOptions: Array<{
  value: TokenAnalyticsSortOrder
  labelKey: string
}> = [
  { value: 'desc', labelKey: 'Descending' },
  { value: 'asc', labelKey: 'Ascending' },
]

function dateFromMs(value?: number): Date | undefined {
  return typeof value === 'number' && value > 0 ? new Date(value) : undefined
}

export function TokenAnalyticsFilterBar({
  filters,
  onFiltersChange,
  onReset,
  loading,
}: TokenAnalyticsFilterBarProps) {
  const { t } = useTranslation()

  return (
    <div className='rounded-md border bg-card/50 p-2 shadow-xs sm:p-3'>
      <div className='grid gap-2 lg:grid-cols-[minmax(260px,1.5fr)_minmax(180px,1fr)_minmax(150px,0.7fr)_auto]'>
        <CompactDateTimeRangePicker
          start={dateFromMs(filters.startTime)}
          end={dateFromMs(filters.endTime)}
          onChange={(range) =>
            onFiltersChange({
              startTime: range.start?.getTime(),
              endTime: range.end?.getTime(),
            })
          }
        />

        <div className='relative'>
          <Search className='text-muted-foreground absolute top-1/2 left-3 size-4 -translate-y-1/2' />
          <Input
            value={filters.keyword}
            onChange={(event) => onFiltersChange({ keyword: event.target.value })}
            placeholder={t('Token name / ID')}
            className='pl-9'
          />
        </div>

        <Select
          value={filters.sortBy}
          onValueChange={(value) =>
            onFiltersChange({ sortBy: value as TokenAnalyticsSortBy })
          }
        >
          <SelectTrigger className='w-full'>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {sortOptions.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {t(option.labelKey)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <div className='flex gap-2'>
          <Select
            value={filters.sortOrder}
            onValueChange={(value) =>
              onFiltersChange({ sortOrder: value as TokenAnalyticsSortOrder })
            }
          >
            <SelectTrigger className='w-full lg:w-[130px]'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {sortOrderOptions.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {t(option.labelKey)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Button
            type='button'
            variant='outline'
            size='icon'
            onClick={onReset}
            disabled={loading}
          >
            <span className='sr-only'>{t('Reset')}</span>
            <X className='size-4' />
          </Button>
        </div>
      </div>
    </div>
  )
}
