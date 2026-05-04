import { useMemo } from 'react'
import {
  flexRender,
  getCoreRowModel,
  type ColumnDef,
  type OnChangeFn,
  type PaginationState,
  useReactTable,
} from '@tanstack/react-table'
import { useTranslation } from 'react-i18next'
import { formatLogQuota, formatNumber, formatTimestampToDate } from '@/lib/format'
import { Badge } from '@/components/ui/badge'
import {
  DataTablePagination,
  TableEmpty,
  TableSkeleton,
} from '@/components/data-table'
import { PageFooterPortal } from '@/components/layout'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import type { TokenAnalyticsItem } from '../../types'

interface TokenAnalyticsTableProps {
  items: TokenAnalyticsItem[]
  loading?: boolean
  total: number
  pagination: PaginationState
  onPaginationChange: OnChangeFn<PaginationState>
}

function TokenStatusBadge({
  status,
  t,
}: {
  status: number
  t: (key: string) => string
}) {
  const meta: Record<number, { labelKey: string; className: string }> = {
    1: {
      labelKey: 'Enabled',
      className:
        'border-emerald-500/20 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300',
    },
    2: {
      labelKey: 'Disabled',
      className:
        'border-destructive/20 bg-destructive/10 text-destructive dark:text-red-300',
    },
    3: {
      labelKey: 'Expired',
      className:
        'border-amber-500/20 bg-amber-500/10 text-amber-700 dark:text-amber-300',
    },
    4: {
      labelKey: 'Exhausted',
      className:
        'border-muted-foreground/20 bg-muted text-muted-foreground',
    },
  }
  const resolved = meta[status] ?? {
    labelKey: 'Unknown',
    className: 'border-muted-foreground/20 bg-muted text-muted-foreground',
  }

  return (
    <Badge variant='outline' className={resolved.className}>
      {t(resolved.labelKey)}
    </Badge>
  )
}

function formatTimestamp(timestamp: number, t: (key: string) => string) {
  if (timestamp === -1) return t('Never')
  return formatTimestampToDate(timestamp)
}

export function TokenAnalyticsTable({
  items,
  loading,
  total,
  pagination,
  onPaginationChange,
}: TokenAnalyticsTableProps) {
  const { t } = useTranslation()

  const columns = useMemo<ColumnDef<TokenAnalyticsItem>[]>(
    () => [
      {
        accessorKey: 'token_id',
        header: t('Token ID'),
        cell: ({ getValue }) => (
          <Badge variant='outline' className='font-mono'>
            #{getValue<number>()}
          </Badge>
        ),
      },
      {
        accessorKey: 'token_name',
        header: t('Token Name'),
        cell: ({ getValue }) => getValue<string>() || '-',
      },
      {
        accessorKey: 'token_status',
        header: t('Status'),
        cell: ({ getValue }) => <TokenStatusBadge status={getValue<number>()} t={t} />,
      },
      {
        accessorKey: 'token_group',
        header: t('Group'),
        cell: ({ getValue }) => getValue<string>() || '-',
      },
      {
        accessorKey: 'token_created_time',
        header: t('Created At'),
        cell: ({ getValue }) => formatTimestamp(getValue<number>(), t),
      },
      {
        accessorKey: 'token_expired_time',
        header: t('Expires At'),
        cell: ({ getValue }) => formatTimestamp(getValue<number>(), t),
      },
      {
        accessorKey: 'request_count',
        header: t('Requests'),
        cell: ({ getValue }) => formatNumber(getValue<number>()),
      },
      {
        accessorKey: 'quota_sum',
        header: t('Usage'),
        cell: ({ getValue }) => formatLogQuota(getValue<number>()),
      },
      {
        accessorKey: 'prompt_tokens_sum',
        header: t('Input Tokens'),
        cell: ({ getValue }) => formatNumber(getValue<number>()),
      },
      {
        accessorKey: 'completion_tokens_sum',
        header: t('Output Tokens'),
        cell: ({ getValue }) => formatNumber(getValue<number>()),
      },
      {
        accessorKey: 'last_used_at',
        header: t('Last Used At'),
        cell: ({ getValue }) => formatTimestamp(getValue<number>(), t),
      },
    ],
    [t]
  )

  const table = useReactTable({
    data: items,
    columns,
    state: { pagination },
    onPaginationChange,
    getCoreRowModel: getCoreRowModel(),
    manualPagination: true,
    pageCount: Math.ceil(total / pagination.pageSize),
  })

  return (
    <>
      <div className='overflow-hidden rounded-md border'>
        <div className='overflow-x-auto'>
          <Table>
            <TableHeader className='bg-muted/30 sticky top-0 z-10'>
              {table.getHeaderGroups().map((headerGroup) => (
                <TableRow key={headerGroup.id}>
                  {headerGroup.headers.map((header) => (
                    <TableHead key={header.id} colSpan={header.colSpan}>
                      {header.isPlaceholder
                        ? null
                        : flexRender(
                            header.column.columnDef.header,
                            header.getContext()
                          )}
                    </TableHead>
                  ))}
                </TableRow>
              ))}
            </TableHeader>
            <TableBody>
              {loading ? (
                <TableSkeleton table={table} keyPrefix='token-analytics-skeleton' />
              ) : table.getRowModel().rows.length === 0 ? (
                <TableEmpty
                  colSpan={columns.length}
                  title={t('No Token Analytics Found')}
                  description={t(
                    'Token analytics will appear once matching API calls are made.'
                  )}
                />
              ) : (
                table.getRowModel().rows.map((row) => (
                  <TableRow key={row.id}>
                    {row.getVisibleCells().map((cell) => (
                      <TableCell key={cell.id} className='py-2.5'>
                        {flexRender(cell.column.columnDef.cell, cell.getContext())}
                      </TableCell>
                    ))}
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </div>
      </div>
      <PageFooterPortal>
        <DataTablePagination table={table} />
      </PageFooterPortal>
    </>
  )
}
