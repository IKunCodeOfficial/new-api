import { Activity, Coins, KeyRound } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { formatLogQuota, formatNumber } from '@/lib/format'
import { Skeleton } from '@/components/ui/skeleton'

interface TokenAnalyticsSummaryProps {
  totalQuota: number
  totalRequests: number
  totalTokens: number
  loading?: boolean
}

const summaryItems = [
  {
    key: 'quota',
    titleKey: 'Total Usage',
    accent: 'bg-sky-500/70',
    icon: Coins,
  },
  {
    key: 'requests',
    titleKey: 'Total Requests',
    accent: 'bg-rose-500/65',
    icon: Activity,
  },
  {
    key: 'tokens',
    titleKey: 'Token Count',
    accent: 'bg-emerald-500/70',
    icon: KeyRound,
  },
] as const

export function TokenAnalyticsSummary({
  totalQuota,
  totalRequests,
  totalTokens,
  loading,
}: TokenAnalyticsSummaryProps) {
  const { t } = useTranslation()

  const values = {
    quota: formatLogQuota(totalQuota),
    requests: formatNumber(totalRequests),
    tokens: formatNumber(totalTokens),
  }

  return (
    <div className='overflow-hidden rounded-lg border'>
      <div className='divide-border/60 grid grid-cols-1 divide-y sm:grid-cols-3 sm:divide-x sm:divide-y-0'>
        {summaryItems.map((item) => {
          const Icon = item.icon
          return (
            <div key={item.key} className='px-4 py-3'>
              <div className='flex items-center gap-2'>
                <span className={`h-4 w-0.5 rounded-full ${item.accent}`} />
                <Icon className='text-muted-foreground/60 size-4 shrink-0' />
                <div className='text-muted-foreground truncate text-xs font-medium tracking-wider uppercase'>
                  {t(item.titleKey)}
                </div>
              </div>
              {loading ? (
                <Skeleton className='mt-2 h-7 w-24' />
              ) : (
                <div className='text-foreground mt-2 font-mono text-xl font-bold tracking-tight tabular-nums'>
                  {values[item.key]}
                </div>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}
