import { useEffect, useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import type { OnChangeFn, PaginationState } from '@tanstack/react-table'
import { toast } from 'sonner'
import { useTranslation } from 'react-i18next'
import { useMediaQuery } from '@/hooks'
import { getDefaultTimeRange } from '../../lib/utils'
import {
  getUserTokenAnalytics,
  getUserTokenAnalyticsTrend,
} from '../../api'
import type {
  TokenAnalyticsItem,
  TokenAnalyticsSortBy,
  TokenAnalyticsSortOrder,
  TokenAnalyticsTrendPoint,
} from '../../types'
import { secondsFromDate, normalizeNumber } from './token-analytics-utils'
import { TokenAnalyticsCharts } from './token-analytics-charts'
import { TokenAnalyticsFilterBar } from './token-analytics-filter-bar'
import { TokenAnalyticsSummary } from './token-analytics-summary'
import { TokenAnalyticsTable } from './token-analytics-table'

const route = getRouteApi('/_authenticated/usage-logs/$section')

const DEFAULT_SORT_BY: TokenAnalyticsSortBy = 'quota_sum'
const DEFAULT_SORT_ORDER: TokenAnalyticsSortOrder = 'desc'

function normalizeSortBy(value: unknown): TokenAnalyticsSortBy {
  const allowed: TokenAnalyticsSortBy[] = [
    'quota_sum',
    'request_count',
    'last_used_at',
    'token_created_time',
    'token_name',
    'token_id',
    'prompt_tokens_sum',
    'completion_tokens_sum',
    'token_status',
  ]
  return allowed.includes(value as TokenAnalyticsSortBy)
    ? (value as TokenAnalyticsSortBy)
    : DEFAULT_SORT_BY
}

function normalizeSortOrder(value: unknown): TokenAnalyticsSortOrder {
  return value === 'asc' ? 'asc' : DEFAULT_SORT_ORDER
}

function normalizeItem(item: TokenAnalyticsItem): TokenAnalyticsItem {
  return {
    ...item,
    token_id: normalizeNumber(item.token_id),
    token_status: normalizeNumber(item.token_status),
    token_created_time: normalizeNumber(item.token_created_time),
    token_expired_time: normalizeNumber(item.token_expired_time),
    request_count: normalizeNumber(item.request_count),
    quota_sum: normalizeNumber(item.quota_sum),
    prompt_tokens_sum: normalizeNumber(item.prompt_tokens_sum),
    completion_tokens_sum: normalizeNumber(item.completion_tokens_sum),
    last_used_at: normalizeNumber(item.last_used_at),
  }
}

function normalizeTrendPoint(
  point: TokenAnalyticsTrendPoint
): TokenAnalyticsTrendPoint {
  return {
    bucket_start: normalizeNumber(point.bucket_start),
    request_count: normalizeNumber(point.request_count),
    quota_sum: normalizeNumber(point.quota_sum),
  }
}

export function TokenAnalyticsPage() {
  const { t } = useTranslation()
  const isMobile = useMediaQuery('(max-width: 640px)')
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const defaultTimeRange = useMemo(getDefaultTimeRange, [])

  const page = typeof search.page === 'number' && search.page > 0 ? search.page : 1
  const defaultPageSize = isMobile ? 20 : 100
  const pageSize =
    typeof search.pageSize === 'number' && search.pageSize > 0
      ? search.pageSize
      : defaultPageSize
  const keyword = typeof search.keyword === 'string' ? search.keyword : ''
  const startTime =
    typeof search.startTime === 'number'
      ? search.startTime
      : defaultTimeRange.start.getTime()
  const endTime =
    typeof search.endTime === 'number'
      ? search.endTime
      : defaultTimeRange.end.getTime()
  const sortBy = normalizeSortBy(search.sortBy)
  const sortOrder = normalizeSortOrder(search.sortOrder)

  const baseParams = {
    keyword,
    start_timestamp: secondsFromDate(new Date(startTime)),
    end_timestamp: secondsFromDate(new Date(endTime)),
  }

  const listQuery = useQuery({
    queryKey: [
      'token-analytics',
      page,
      pageSize,
      keyword,
      startTime,
      endTime,
      sortBy,
      sortOrder,
    ],
    queryFn: async () => {
      const result = await getUserTokenAnalytics({
        ...baseParams,
        p: page,
        page_size: pageSize,
        sort_by: sortBy,
        sort_order: sortOrder,
      })

      if (!result.success) {
        toast.error(result.message || t('Failed to load token analytics'))
        return { items: [], total: 0, page, page_size: pageSize }
      }

      return {
        items: (result.data?.items ?? []).map(normalizeItem),
        total: result.data?.total ?? 0,
        page: result.data?.page ?? page,
        page_size: result.data?.page_size ?? pageSize,
      }
    },
    placeholderData: (previousData) => previousData,
  })

  const trendQuery = useQuery({
    queryKey: ['token-analytics-trend', keyword, startTime, endTime],
    queryFn: async () => {
      const result = await getUserTokenAnalyticsTrend(baseParams)

      if (!result.success) {
        toast.error(result.message || t('Failed to load token analytics'))
        return {
          granularity: 'day' as const,
          series: [],
          top_tokens: [],
        }
      }

      return {
        granularity: result.data?.granularity ?? ('day' as const),
        series: (result.data?.series ?? []).map(normalizeTrendPoint),
        top_tokens: (result.data?.top_tokens ?? []).map((item) => ({
          token_id: normalizeNumber(item.token_id),
          token_name: item.token_name || '',
          request_count: normalizeNumber(item.request_count),
          quota_sum: normalizeNumber(item.quota_sum),
        })),
      }
    },
    placeholderData: (previousData) => previousData,
  })

  const pagination = useMemo<PaginationState>(
    () => ({ pageIndex: page - 1, pageSize }),
    [page, pageSize]
  )

  const onPaginationChange: OnChangeFn<PaginationState> = (updater) => {
    const next = typeof updater === 'function' ? updater(pagination) : updater
    void navigate({
      search: (prev) => ({
        ...prev,
        page: next.pageIndex <= 0 ? undefined : next.pageIndex + 1,
        pageSize: next.pageSize === defaultPageSize ? undefined : next.pageSize,
      }),
    })
  }

  const updateFilters = (filters: {
    keyword?: string
    startTime?: number
    endTime?: number
    sortBy?: TokenAnalyticsSortBy
    sortOrder?: TokenAnalyticsSortOrder
  }) => {
    void navigate({
      search: (prev) => ({
        ...prev,
        page: undefined,
        keyword: filters.keyword === '' ? undefined : (filters.keyword ?? prev.keyword),
        startTime: filters.startTime ?? prev.startTime,
        endTime: filters.endTime ?? prev.endTime,
        sortBy:
          filters.sortBy === DEFAULT_SORT_BY
            ? undefined
            : (filters.sortBy ?? prev.sortBy),
        sortOrder:
          filters.sortOrder === DEFAULT_SORT_ORDER
            ? undefined
            : (filters.sortOrder ?? prev.sortOrder),
      }),
    })
  }

  const resetFilters = () => {
    void navigate({
      search: (prev) => ({
        ...prev,
        page: undefined,
        keyword: undefined,
        startTime: undefined,
        endTime: undefined,
        sortBy: undefined,
        sortOrder: undefined,
      }),
    })
  }

  const pageCount = Math.ceil((listQuery.data?.total ?? 0) / pageSize)
  useEffect(() => {
    if (pageCount > 0 && page > pageCount) {
      void navigate({
        replace: true,
        search: (prev) => ({ ...prev, page: undefined }),
      })
    }
  }, [navigate, page, pageCount])

  const trendSeries = trendQuery.data?.series ?? []
  const totalQuota = trendSeries.reduce((sum, point) => sum + point.quota_sum, 0)
  const totalRequests = trendSeries.reduce(
    (sum, point) => sum + point.request_count,
    0
  )

  return (
    <div className='space-y-3 sm:space-y-4'>
      <TokenAnalyticsSummary
        totalQuota={totalQuota}
        totalRequests={totalRequests}
        totalTokens={listQuery.data?.total ?? 0}
        loading={trendQuery.isLoading}
      />

      <TokenAnalyticsFilterBar
        filters={{
          keyword,
          startTime,
          endTime,
          sortBy,
          sortOrder,
        }}
        onFiltersChange={updateFilters}
        onReset={resetFilters}
        loading={listQuery.isFetching || trendQuery.isFetching}
      />

      <TokenAnalyticsCharts
        trendSeries={trendSeries}
        topTokens={trendQuery.data?.top_tokens ?? []}
        trendGranularity={trendQuery.data?.granularity ?? 'day'}
        loading={trendQuery.isLoading}
      />

      <TokenAnalyticsTable
        items={listQuery.data?.items ?? []}
        total={listQuery.data?.total ?? 0}
        pagination={pagination}
        onPaginationChange={onPaginationChange}
        loading={listQuery.isLoading}
      />
    </div>
  )
}
