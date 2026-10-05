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
import {
  render,
  screen,
  waitFor,
  type RenderResult,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'

import type { ManageUserQuotaPayload } from '../../types'

const i18n = (await import('i18next')).default
const { I18nextProvider } = await import('react-i18next')
const { Toaster, toast } = await import('sonner')
const { api } = await import('@/lib/api')
const { useSystemConfigStore } = await import('@/stores/system-config-store')
const { UserQuotaDialog } = await import('../user-quota-dialog')

await i18n.init({
  lng: 'en',
  fallbackLng: 'en',
  resources: { en: { translation: {} } },
})

type ApiMethod = (url: string, data?: unknown) => Promise<{ data: unknown }>
type MockableApi = { post: ApiMethod }
type QuotaAdjustPayload = ManageUserQuotaPayload & { recharge?: boolean }

const apiClient = api as unknown as MockableApi
const originalPost = apiClient.post

const PAID_REJECTED =
  'Paid amount must be greater than 0 and not exceed the credited amount'

let rendered: RenderResult | null = null
const adjusted: QuotaAdjustPayload[] = []
const onOpenChangeCalls: boolean[] = []
const onSuccessCalls: number[] = []

function renderDialog(): void {
  rendered = render(
    <I18nextProvider i18n={i18n}>
      <UserQuotaDialog
        open
        onOpenChange={(open) => onOpenChangeCalls.push(open)}
        userId={7}
        currentQuota={0}
        onSuccess={() => onSuccessCalls.push(1)}
      />
      <Toaster duration={60_000} />
    </I18nextProvider>
  )
}

function creditedAmountInput(): HTMLInputElement {
  return screen.getByPlaceholderText('Enter amount in USD')
}

async function enterCreditedAmount(amount: string): Promise<void> {
  const user = userEvent.setup()
  await user.clear(creditedAmountInput())
  await user.type(creditedAmountInput(), amount)
}

async function enterPaidAmount(amount: string): Promise<void> {
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('Paid amount (USD)'), amount)
}

async function markAsRecharge(): Promise<void> {
  await userEvent.setup().click(screen.getByRole('checkbox'))
}

async function confirm(): Promise<void> {
  await userEvent.setup().click(screen.getByRole('button', { name: 'Confirm' }))
}

async function waitForAdjustment(): Promise<void> {
  await waitFor(() => expect(adjusted).toHaveLength(1))
}

async function waitForRejection(): Promise<void> {
  await waitFor(() => expect(screen.getByText(PAID_REJECTED)).not.toBeNull())
}

beforeEach(() => {
  useSystemConfigStore.getState().setConfig({
    currency: {
      displayInCurrency: true,
      quotaDisplayType: 'USD',
      quotaPerUnit: 500000,
      usdExchangeRate: 1,
      customCurrencySymbol: '¤',
      customCurrencyExchangeRate: 1,
    },
  })
  apiClient.post = async (_url, data) => {
    adjusted.push(data as QuotaAdjustPayload)
    return { data: { success: true } }
  }
})

afterEach(() => {
  apiClient.post = originalPost
  rendered?.unmount()
  rendered = null
  toast.dismiss()
  adjusted.length = 0
  onOpenChangeCalls.length = 0
  onSuccessCalls.length = 0
})

describe('user quota dialog recharge option', () => {
  test('only offers the recharge option while adding quota', async () => {
    renderDialog()

    expect(screen.getByRole('checkbox')).not.toBeNull()

    await userEvent
      .setup()
      .click(screen.getByRole('button', { name: 'Subtract' }))

    expect(screen.queryByRole('checkbox')).toBeNull()
  })

  test('shows the paid amount input only after marking the grant as recharge', async () => {
    renderDialog()

    expect(screen.queryByLabelText('Paid amount (USD)')).toBeNull()

    await markAsRecharge()

    expect(screen.getByLabelText('Paid amount (USD)')).not.toBeNull()
  })

  test('sends no recharge fields when the grant stays a plain top-up', async () => {
    renderDialog()
    await enterCreditedAmount('10')
    await confirm()
    await waitForAdjustment()

    expect(adjusted[0]?.recharge).toBeUndefined()
    expect(adjusted[0]?.paid_quota).toBeUndefined()
    expect(adjusted[0]?.value).toBe(5_000_000)
  })

  test('defaults the paid amount to the credited amount', async () => {
    renderDialog()
    await enterCreditedAmount('10')
    await markAsRecharge()
    await confirm()
    await waitForAdjustment()

    expect(adjusted[0]?.recharge).toBe(true)
    expect(adjusted[0]?.paid_quota).toBe(5_000_000)
  })

  test('sends the discounted paid amount', async () => {
    renderDialog()
    await enterCreditedAmount('10')
    await markAsRecharge()
    await enterPaidAmount('8')
    await confirm()
    await waitForAdjustment()

    expect(adjusted[0]?.paid_quota).toBe(4_000_000)
  })

  test('rejects a paid amount above the credited amount without sending a request', async () => {
    renderDialog()
    await enterCreditedAmount('10')
    await markAsRecharge()
    await enterPaidAmount('11')
    await confirm()

    await waitForRejection()
    expect(adjusted).toHaveLength(0)
  })

  test('rejects a negative paid amount without sending a request', async () => {
    renderDialog()
    await enterCreditedAmount('10')
    await markAsRecharge()
    await enterPaidAmount('-8')
    await confirm()

    await waitForRejection()
    expect(adjusted).toHaveLength(0)
  })

  test('sends no request for a zero credited amount', async () => {
    renderDialog()
    await markAsRecharge()
    await confirm()

    expect(adjusted).toHaveLength(0)
  })

  test('clears the recharge choice when the mode changes', async () => {
    renderDialog()
    await enterCreditedAmount('10')
    await markAsRecharge()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Subtract' }))
    await user.click(screen.getByRole('button', { name: 'Add' }))

    expect(screen.getByRole('checkbox').getAttribute('aria-checked')).toBe(
      'false'
    )
  })

  test('clears the recharge choice after a successful adjustment', async () => {
    renderDialog()
    await enterCreditedAmount('10')
    await markAsRecharge()
    await confirm()

    await waitFor(() => expect(onSuccessCalls).toHaveLength(1))
    expect(adjusted).toHaveLength(1)
    expect(onOpenChangeCalls).toContain(false)
    expect(screen.getByRole('checkbox').getAttribute('aria-checked')).toBe(
      'false'
    )
    expect(creditedAmountInput().value).toBe('')
  })
})
