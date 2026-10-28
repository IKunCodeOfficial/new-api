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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'

import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import type { UsageLog } from '../../data/schema'
import type { LogOtherData } from '../../types'
import { DetailsDialog } from '../dialogs/details-dialog'

// The admin-only settlement notes: a failed settlement leaves part of the cost
// uncharged (positive) or a failed refund leaves the user overcharged
// (negative); a channel test records the cost nobody was charged.

const queryClients: QueryClient[] = []

function makeLog(other: LogOtherData): UsageLog {
  return {
    id: 1,
    user_id: 1,
    created_at: 1,
    type: 2,
    content: '',
    username: 'user',
    token_name: 'token',
    model_name: 'gpt-test',
    quota: 1_000_000,
    prompt_tokens: 0,
    completion_tokens: 0,
    use_time: 0,
    is_stream: false,
    channel: 1,
    channel_name: 'channel',
    token_id: 1,
    group: 'default',
    ip: '',
    other: JSON.stringify(other),
    request_id: 'req-1',
    upstream_request_id: '',
  }
}

function renderDetails(adminInfo: LogOtherData['admin_info'], isAdmin = true) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const freshAt = Date.now() + 60_000
  queryClient.setQueryData(['status'], {}, { updatedAt: freshAt })
  queryClients.push(queryClient)

  render(
    <QueryClientProvider client={queryClient}>
      <DetailsDialog
        log={makeLog({ admin_info: adminInfo })}
        isAdmin={isAdmin}
        isRoot={false}
        open
        onOpenChange={() => undefined}
      />
    </QueryClientProvider>
  )
  return within(screen.getByRole('dialog'))
}

beforeEach(() => {
  useSystemConfigStore.getState().setConfig({
    currency: { ...DEFAULT_CURRENCY_CONFIG },
  })
})

afterEach(() => {
  for (const queryClient of queryClients) {
    queryClient.clear()
  }
  queryClients.length = 0
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
})

describe('usage log settlement notes', () => {
  test('a positive unsettled quota is shown to admins as the uncharged amount', () => {
    const dialog = renderDetails({ unsettled_quota: 250_000 })

    expect(dialog.getByText('Settlement not committed')).toBeInTheDocument()
    expect(dialog.getByText('Uncharged amount')).toBeInTheDocument()
    expect(dialog.queryByText('Unrefunded amount')).toBeNull()
    expect(dialog.getByText('$0.5')).toBeInTheDocument()
  })

  test('a negative unsettled quota is shown to admins as the unrefunded amount without a sign', () => {
    const dialog = renderDetails({ unsettled_quota: -250_000 })

    expect(dialog.getByText('Settlement not committed')).toBeInTheDocument()
    expect(dialog.getByText('Unrefunded amount')).toBeInTheDocument()
    expect(dialog.queryByText('Uncharged amount')).toBeNull()
    expect(dialog.getByText('$0.5')).toBeInTheDocument()
    expect(dialog.queryByText('-$0.5')).toBeNull()
  })

  test('a channel test estimate is shown to admins as not charged', () => {
    const dialog = renderDetails({ channel_test_quota: 250_000 })

    expect(dialog.getByText('Channel test')).toBeInTheDocument()
    expect(dialog.getByText('Estimated cost (not charged)')).toBeInTheDocument()
    expect(dialog.getByText('$0.5')).toBeInTheDocument()
  })

  test('non-admin users see neither settlement nor channel test notes', () => {
    const dialog = renderDetails(
      { unsettled_quota: -250_000, channel_test_quota: 250_000 },
      false
    )

    expect(dialog.queryByText('Settlement not committed')).toBeNull()
    expect(dialog.queryByText('Unrefunded amount')).toBeNull()
    expect(dialog.queryByText('Channel test')).toBeNull()
    expect(dialog.queryByText('Estimated cost (not charged)')).toBeNull()
  })
})
