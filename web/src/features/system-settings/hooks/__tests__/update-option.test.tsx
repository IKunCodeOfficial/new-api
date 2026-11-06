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
import {
  updateSystemBanner,
  updateSystemOption,
} from '@/features/system-settings/api'
import { useUpdateOption } from '@/features/system-settings/hooks/use-update-option'
import { useUpdateSystemBanner } from '@/features/system-settings/hooks/use-update-system-banner'

vi.mock('@/features/system-settings/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/system-settings/api')>()),
  updateSystemOption: vi.fn(),
  updateSystemBanner: vi.fn(),
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
  vi.mocked(updateSystemOption).mockResolvedValue({
    success: true,
    message: '',
  })
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

const banner = {
  enabled: true,
  level: 'normal',
  text_zh: '维护',
  text_en: 'Maintenance',
} as const

function renderBannerSave(client: QueryClient) {
  return renderHook(() => useUpdateSystemBanner(), {
    wrapper: (props: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, props.children),
  })
}

test('saving the whole banner sends one request, refreshes every view of it and confirms once', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const views = [SYSTEM_BANNER_QUERY_KEY, ['status'], ['system-options']]
  for (const queryKey of views) client.setQueryData(queryKey, null)
  window.localStorage.setItem('status', '{"stale":true}')
  const success = vi.spyOn(toast, 'success').mockReturnValue('saved')
  vi.mocked(updateSystemBanner).mockResolvedValue({
    success: true,
    message: '',
  })
  const { result, unmount } = renderBannerSave(client)

  await act(async () => {
    await result.current.mutateAsync(banner)
  })

  expect(updateSystemBanner).toHaveBeenCalledTimes(1)
  expect(updateSystemBanner).toHaveBeenCalledWith(banner)
  for (const queryKey of views) {
    expect(client.getQueryState(queryKey)?.isInvalidated).toBe(true)
  }
  expect(window.localStorage.getItem('status')).toBeNull()
  expect(success).toHaveBeenCalledTimes(1)
  unmount()
  client.clear()
})

test('a rejected banner save reports the server reason and confirms nothing', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const success = vi.spyOn(toast, 'success').mockReturnValue('saved')
  const failure = vi.spyOn(toast, 'error').mockReturnValue('failed')
  vi.mocked(updateSystemBanner).mockResolvedValue({
    success: false,
    message: 'System banner needs both Chinese and English text while enabled',
  })
  const { result, unmount } = renderBannerSave(client)

  await act(async () => {
    await expect(result.current.mutateAsync(banner)).rejects.toThrow(
      'System banner needs both Chinese and English text while enabled'
    )
  })

  expect(success).not.toHaveBeenCalled()
  expect(failure).toHaveBeenCalledTimes(1)
  unmount()
  client.clear()
})
