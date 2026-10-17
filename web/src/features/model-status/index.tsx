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
import { getRouteApi } from '@tanstack/react-router'
import { Activity } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { PublicLayout } from '@/components/layout'
import { LoadingState } from '@/components/loading-state'

import { useModelStatus } from './api'
import { BucketTooltip } from './components/bucket-tooltip'
import { GroupCard } from './components/group-card'
import { StatusBanner } from './components/status-banner'
import { StatusLegend } from './components/status-legend'
import { StatusMatrix } from './components/status-matrix'
import { StatusToolbar } from './components/status-toolbar'
import { ALL_GROUPS, type StatusView } from './constants'
import { hideBucketTip } from './lib/bucket-tip-store'
import type { StatusRange } from './types'

const routeApi = getRouteApi('/status/')

const GLOW_STYLE = {
  background: [
    'radial-gradient(ellipse 60% 50% at 20% 20%, oklch(0.72 0.18 250 / 80%) 0%, transparent 70%)',
    'radial-gradient(ellipse 50% 40% at 80% 15%, oklch(0.65 0.15 200 / 60%) 0%, transparent 70%)',
    'radial-gradient(ellipse 40% 35% at 50% 70%, oklch(0.70 0.12 280 / 40%) 0%, transparent 70%)',
  ].join(', '),
  maskImage: 'linear-gradient(to bottom, black 40%, transparent 100%)',
  WebkitMaskImage: 'linear-gradient(to bottom, black 40%, transparent 100%)',
}

export function ModelStatus() {
  const { t } = useTranslation()
  const search = routeApi.useSearch()
  const navigate = routeApi.useNavigate()
  const range: StatusRange = search.range ?? '1h'
  const view: StatusView = search.view ?? 'list'
  const [query, setQuery] = useState(search.q ?? '')
  const [expandedKey, setExpandedKey] = useState<string | null>(null)
  const status = useModelStatus(range)
  const data = status.data
  const groupKeys = data?.groups.map((g) => g.key) ?? []
  const group =
    search.group && groupKeys.includes(search.group) ? search.group : ALL_GROUPS
  const groups =
    data?.groups.filter((g) => group === ALL_GROUPS || g.key === group) ?? []

  const updateSearch = (patch: Record<string, string | undefined>) => {
    hideBucketTip()
    void navigate({
      search: (prev) => ({ ...prev, ...patch }),
      replace: true,
    })
  }

  // Data-bound views read data.range, not the URL range: while the next
  // range loads, the previous payload stays on screen and must keep its own
  // bucket labels and detail flag.
  const renderContent = () => {
    if (status.isLoading) {
      return <LoadingState className='py-24' />
    }
    if (status.isError || !data) {
      return (
        <ErrorState
          description={t('Failed to load model status')}
          onRetry={() => void status.refetch()}
        />
      )
    }
    if (data.groups.length === 0) {
      return (
        <EmptyState
          icon={Activity}
          title={t('No model status data yet')}
          bordered
          className='rounded-xl'
        />
      )
    }
    if (view === 'matrix') {
      return <StatusMatrix groups={groups} query={query} />
    }
    return (
      <div className='space-y-4'>
        {groups.map((g) => (
          <GroupCard
            key={g.key}
            group={g}
            range={data.range}
            hasDetail={data.has_detail}
            query={query}
            expandedKey={expandedKey}
            onExpandedChange={setExpandedKey}
          />
        ))}
      </div>
    )
  }

  return (
    <PublicLayout showMainContainer={false}>
      <div className='relative'>
        <div
          aria-hidden
          className='pointer-events-none absolute inset-x-0 top-0 h-[600px] opacity-20 dark:opacity-10'
          style={GLOW_STYLE}
        />
        <main className='relative mx-auto w-full max-w-7xl px-4 pt-20 pb-12 sm:px-6'>
          <header className='mx-auto mb-8 max-w-3xl pt-8 text-center'>
            {/* English eyebrow by design, shown above the localised title. */}
            <p
              aria-hidden='true'
              className='text-muted-foreground mb-3 text-xs font-medium tracking-widest uppercase'
            >
              Model Status
            </p>
            <h1 className='text-[clamp(2rem,5.5vw,3.5rem)] leading-[1.15] font-bold tracking-tight'>
              {t('Model Status')}
            </h1>
            <p className='text-muted-foreground/80 mt-4 text-base'>
              {t(
                'Availability and performance of every group and model, based on real user requests'
              )}
            </p>
          </header>
          {data && data.groups.length > 0 && (
            <StatusBanner
              groups={groups}
              range={data.range}
              updatedAt={data.updated_at}
            />
          )}
          <StatusToolbar
            range={range}
            onRangeChange={(next) => {
              setExpandedKey(null)
              updateSearch({ range: next === '1h' ? undefined : next })
            }}
            group={group}
            groupKeys={groupKeys}
            onGroupChange={(next) =>
              updateSearch({ group: next === ALL_GROUPS ? undefined : next })
            }
            query={query}
            onQueryChange={(next) => {
              setQuery(next)
              updateSearch({ q: next || undefined })
            }}
            view={view}
            onViewChange={(next) =>
              updateSearch({ view: next === 'list' ? undefined : next })
            }
            refreshing={status.isFetching}
            onRefresh={() => void status.refetch()}
          />
          {renderContent()}
          <StatusLegend />
          {data && (
            <BucketTooltip
              range={data.range}
              windowStart={data.window_start}
              bucketSeconds={data.bucket_seconds}
            />
          )}
        </main>
      </div>
    </PublicLayout>
  )
}
