import { useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { VChart } from '@visactor/react-vchart'
import { BarChart3, LineChart, type LucideIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { useTheme } from '@/context/theme-provider'
import { formatLogQuota, formatNumber } from '@/lib/format'
import { VCHART_OPTION } from '@/lib/vchart'
import { Skeleton } from '@/components/ui/skeleton'
import type {
  TokenAnalyticsItem,
  TokenAnalyticsTrendPoint,
} from '../../types'
import { formatBucketLabel, normalizeNumber } from './token-analytics-utils'

let themeManagerPromise: Promise<
  (typeof import('@visactor/vchart'))['ThemeManager']
> | null = null

interface TokenAnalyticsChartsProps {
  trendSeries: TokenAnalyticsTrendPoint[]
  topTokens: Pick<
    TokenAnalyticsItem,
    'token_id' | 'token_name' | 'request_count' | 'quota_sum'
  >[]
  trendGranularity: 'hour' | 'day'
  loading?: boolean
}

function ChartShell({
  icon: Icon,
  title,
  total,
  loading,
  empty,
  children,
}: {
  icon: LucideIcon
  title: string
  total: string
  loading?: boolean
  empty: boolean
  children: ReactNode
}) {
  const { t } = useTranslation()

  return (
    <div className='overflow-hidden rounded-lg border'>
      <div className='flex items-center gap-2 border-b px-3 py-2 sm:px-5 sm:py-3'>
        <Icon className='text-muted-foreground/60 size-4' />
        <div className='text-sm font-semibold'>{title}</div>
        <span className='text-muted-foreground text-xs'>
          {t('Total:')} {total}
        </span>
      </div>
      <div className='h-[280px] p-1.5 sm:h-80 sm:p-2'>
        {loading ? (
          <Skeleton className='h-full w-full' />
        ) : empty ? (
          <div className='text-muted-foreground flex h-full items-center justify-center text-sm'>
            {t('No analytics data')}
          </div>
        ) : (
          children
        )}
      </div>
    </div>
  )
}

export function TokenAnalyticsCharts({
  trendSeries,
  topTokens,
  trendGranularity,
  loading,
}: TokenAnalyticsChartsProps) {
  const { t } = useTranslation()
  const { resolvedTheme } = useTheme()
  const [themeReady, setThemeReady] = useState(false)
  const themeManagerRef = useRef<
    (typeof import('@visactor/vchart'))['ThemeManager'] | null
  >(null)

  useEffect(() => {
    const updateTheme = async () => {
      setThemeReady(false)

      if (!themeManagerPromise) {
        themeManagerPromise = import('@visactor/vchart').then(
          (m) => m.ThemeManager
        )
      }

      const ThemeManager = await themeManagerPromise
      themeManagerRef.current = ThemeManager
      ThemeManager.setCurrentTheme(resolvedTheme === 'dark' ? 'dark' : 'light')
      setThemeReady(true)
    }

    updateTheme()
  }, [resolvedTheme])

  const trendValues = useMemo(
    () =>
      trendSeries.map((item) => ({
        Time: formatBucketLabel(item.bucket_start, trendGranularity),
        Usage: normalizeNumber(item.quota_sum),
        Requests: normalizeNumber(item.request_count),
      })),
    [trendGranularity, trendSeries]
  )

  const rankValues = useMemo(
    () =>
      topTokens.map((item) => ({
        Token: item.token_name || `#${item.token_id}`,
        Usage: normalizeNumber(item.quota_sum),
        Requests: normalizeNumber(item.request_count),
      })),
    [topTokens]
  )

  const trendSpec = useMemo(
    () => ({
      type: 'line',
      data: [{ id: 'trend', values: trendValues }],
      xField: 'Time',
      yField: 'Usage',
      point: { visible: true },
      axes: [
        {
          orient: 'left',
          label: {
            formatMethod: (value: number) => formatLogQuota(value),
          },
        },
      ],
      tooltip: {
        mark: {
          content: [
            {
              key: () => t('Usage'),
              value: (datum: Record<string, number>) => formatLogQuota(datum.Usage),
            },
            {
              key: () => t('Requests'),
              value: (datum: Record<string, number>) => formatNumber(datum.Requests),
            },
          ],
        },
      },
    }),
    [t, trendValues]
  )

  const rankSpec = useMemo(
    () => ({
      type: 'bar',
      data: [{ id: 'rank', values: rankValues }],
      xField: 'Token',
      yField: 'Usage',
      axes: [
        {
          orient: 'left',
          label: {
            formatMethod: (value: number) => formatLogQuota(value),
          },
        },
      ],
      tooltip: {
        mark: {
          content: [
            {
              key: () => t('Usage'),
              value: (datum: Record<string, number>) => formatLogQuota(datum.Usage),
            },
            {
              key: () => t('Requests'),
              value: (datum: Record<string, number>) => formatNumber(datum.Requests),
            },
          ],
        },
      },
    }),
    [rankValues, t]
  )

  const chartTheme = resolvedTheme === 'dark' ? 'dark' : 'light'
  const totalTrendUsage = trendValues.reduce((sum, item) => sum + item.Usage, 0)
  const totalRankUsage = rankValues.reduce((sum, item) => sum + item.Usage, 0)

  return (
    <div className='grid gap-3 xl:grid-cols-2'>
      <ChartShell
        icon={LineChart}
        title={t('Usage Trend')}
        total={formatLogQuota(totalTrendUsage)}
        loading={loading || !themeReady}
        empty={trendValues.length === 0}
      >
        <VChart
          key={`token-trend-${chartTheme}`}
          spec={{
            ...trendSpec,
            theme: chartTheme,
            background: 'transparent',
          }}
          option={VCHART_OPTION}
        />
      </ChartShell>

      <ChartShell
        icon={BarChart3}
        title={t('Token Usage Top 10')}
        total={formatLogQuota(totalRankUsage)}
        loading={loading || !themeReady}
        empty={rankValues.length === 0}
      >
        <VChart
          key={`token-rank-${chartTheme}`}
          spec={{
            ...rankSpec,
            theme: chartTheme,
            background: 'transparent',
          }}
          option={VCHART_OPTION}
        />
      </ChartShell>
    </div>
  )
}
