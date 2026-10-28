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
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { createInstance } from 'i18next'
import { useState } from 'react'
import { I18nextProvider, initReactI18next } from 'react-i18next'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { RebateSettingsSection } from '../rebate-settings-section'

// Fork-only (ikun): the settler refuses rates outside (0, 1) or with more than
// four decimals, so the form must reject them with a message in the
// administrator's language before anything is saved.

const i18n = createInstance()

function Fixture() {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        <RebateSettingsSection defaultValues={{ enabled: false, rate: 0.05 }} />
      </SettingsPageProvider>
    </>
  )
}

async function renderRate() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const router = createRouter({
    routeTree: createRootRoute({ component: Fixture }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </I18nextProvider>
  )
  return screen.findByRole('spinbutton', { name: '返利比例（%）' })
}

beforeEach(async () => {
  await i18n.use(initReactI18next).init({
    lng: 'zh',
    fallbackLng: false,
    nsSeparator: false,
    resources: { en, zh },
    interpolation: { escapeValue: false },
  })
  vi.spyOn(api, 'get').mockImplementation(async (url: string) =>
    url === '/api/rebate/exclusions'
      ? { data: { success: true, data: [] } }
      : { data: { enabled: false } }
  )
  vi.spyOn(api, 'put').mockResolvedValue({ data: { success: true } })
})

afterEach(() => {
  vi.restoreAllMocks()
})

test.each([
  { rate: '0', message: '比例必须不小于 0.01 且小于 100' },
  { rate: '100', message: '比例必须不小于 0.01 且小于 100' },
  { rate: '1.234', message: '比例最多保留 2 位小数' },
])(
  'a rate of $rate is rejected with a translated message and not saved',
  async ({ rate, message }) => {
    const input = await renderRate()
    fireEvent.change(input, { target: { value: rate } })
    fireEvent.click(screen.getByRole('button', { name: '保存邀请返利设置' }))

    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'))
    expect(screen.getByText(message)).toBeVisible()
    expect(api.put).not.toHaveBeenCalled()
  }
)

test('a valid rate is saved as a decimal fraction', async () => {
  const input = await renderRate()
  fireEvent.change(input, { target: { value: '12.5' } })
  fireEvent.click(screen.getByRole('button', { name: '保存邀请返利设置' }))

  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'IkunRebateRate',
      value: '0.125',
    })
  )
})
