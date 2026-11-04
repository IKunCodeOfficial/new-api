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
import { act, renderHook } from '@testing-library/react'
import { createElement, type ReactNode } from 'react'
import { toast } from 'sonner'
import { afterEach, expect, test, vi } from 'vitest'

import { SYSTEM_BANNER_QUERY_KEY } from '@/components/system-banner/lib'
import { updateSystemOption } from '@/features/system-settings/api'
import { useUpdateOption } from '@/features/system-settings/hooks/use-update-option'

vi.mock('@/features/system-settings/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/system-settings/api')>()),
  updateSystemOption: vi.fn(),
}))

afterEach(() => {
  vi.restoreAllMocks()
})

async function saveOption(key: string) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  client.setQueryData(SYSTEM_BANNER_QUERY_KEY, null)
  vi.spyOn(toast, 'success').mockReturnValue('saved')
  vi.mocked(updateSystemOption).mockResolvedValue({ success: true, message: '' })
  const { result, unmount } = renderHook(() => useUpdateOption(), {
    wrapper: (props: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, props.children),
  })
  await act(async () => {
    await result.current.mutateAsync({ key, value: 'x' })
  })
  const invalidated = client.getQueryState(
    SYSTEM_BANNER_QUERY_KEY
  )?.isInvalidated
  unmount()
  client.clear()
  return invalidated
}

test('saving a system banner setting refreshes the banner right away', async () => {
  expect(await saveOption('system_banner.text_en')).toBe(true)
})

test('saving an unrelated setting leaves the banner query alone', async () => {
  expect(await saveOption('QuotaPerUnit')).toBe(false)
})
