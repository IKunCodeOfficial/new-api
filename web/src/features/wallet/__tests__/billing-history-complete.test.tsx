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
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { I18nextProvider, initReactI18next } from 'react-i18next'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { BillingHistoryDialog } from '@/features/wallet/components/dialogs/billing-history-dialog'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'
import { useSystemConfigStore } from '@/stores/system-config-store'

// Fork-only (ikun): completing an order by hand may state what the gateway
// collected, in the currency of the order's list price. The server compares it
// with the list price, so the amount is sent exactly as entered, whatever the
// display currency.

const i18n = createInstance()

beforeEach(async () => {
  await i18n.use(initReactI18next).init({
    lng: 'en',
    fallbackLng: 'en',
    resources: { en: { translation: {} } },
  })
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  // A display currency other than USD: a converted amount would differ.
  useSystemConfigStore.getState().setConfig({
    currency: {
      displayInCurrency: true,
      quotaDisplayType: 'CNY',
      quotaPerUnit: 500000,
      usdExchangeRate: 7.3,
      customCurrencySymbol: '¤',
      customCurrencyExchangeRate: 1,
    },
  })
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        total: 1,
        items: [
          {
            id: 1,
            user_id: 7,
            status: 'pending',
            amount: 10,
            money: 73,
            trade_no: 'ref_pending_stripe',
            payment_method: 'stripe',
            create_time: 1,
          },
        ],
      },
    },
  })
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
})

async function openConfirm() {
  render(
    <I18nextProvider i18n={i18n}>
      <BillingHistoryDialog open onOpenChange={vi.fn()} />
    </I18nextProvider>
  )
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: 'Complete Order' })
  )
  return user
}

test('sends the amount collected as entered when the display currency differs', async () => {
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  const user = await openConfirm()
  await user.type(screen.getByLabelText('Amount collected'), '40')
  await user.click(screen.getByRole('button', { name: 'Confirm' }))

  await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
  expect(post.mock.calls[0][1]).toEqual({
    trade_no: 'ref_pending_stripe',
    paid_money: 40,
  })
  await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
})

test('leaves paid_money out when the amount collected is empty', async () => {
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  const user = await openConfirm()
  await user.click(screen.getByRole('button', { name: 'Confirm' }))

  await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
  expect(post.mock.calls[0][1]).toEqual({ trade_no: 'ref_pending_stripe' })
})

test('the amount collected field is described by the gateway currency hint', async () => {
  await openConfirm()

  expect(screen.getByLabelText('Amount collected')).toHaveAccessibleDescription(
    /in the currency of its list price/
  )
})

test.each(['40。5', '４０.5', '40,5'])(
  'an amount collected typed as %s is sent as 40.5',
  async (entered) => {
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValue({ data: { success: true } })
    const user = await openConfirm()
    await user.type(screen.getByLabelText('Amount collected'), entered)
    await user.click(screen.getByRole('button', { name: 'Confirm' }))

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post.mock.calls[0][1]).toEqual({
      trade_no: 'ref_pending_stripe',
      paid_money: 40.5,
    })
  }
)

test('an invalid amount collected shows an error the field is described by', async () => {
  const user = await openConfirm()
  const input = screen.getByLabelText('Amount collected')
  await user.type(input, 'abc')

  expect(
    screen.getByText('Enter an amount such as 40 or 40.5')
  ).toBeInTheDocument()
  expect(input).toHaveAccessibleDescription(
    /Enter an amount such as 40 or 40\.5/
  )
})

test.each(['-1', 'abc', '1,000', '40,500'])(
  'an amount collected of %s is marked invalid and cannot be submitted',
  async (entered) => {
    const post = vi.spyOn(api, 'post')
    const user = await openConfirm()
    const input = screen.getByLabelText('Amount collected')
    await user.type(input, entered)

    expect(input).toHaveAttribute('aria-invalid', 'true')
    const confirm = screen.getByRole('button', { name: 'Confirm' })
    expect(confirm).toBeDisabled()
    await user.click(confirm)
    expect(post).not.toHaveBeenCalled()
  }
)

test('cancelling clears the amount collected before the next confirmation', async () => {
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  const user = await openConfirm()
  await user.type(screen.getByLabelText('Amount collected'), '8')
  await user.click(screen.getByRole('button', { name: 'Cancel' }))
  await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())

  await user.click(screen.getByRole('button', { name: 'Complete Order' }))
  expect(screen.getByLabelText('Amount collected')).toHaveValue('')
  await user.click(screen.getByRole('button', { name: 'Confirm' }))

  await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
  expect(post.mock.calls[0][1]).toEqual({ trade_no: 'ref_pending_stripe' })
})

test('a failed completion keeps the confirmation open with the amount', async () => {
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: false, message: 'Order is not pending' },
  })
  const user = await openConfirm()
  await user.type(screen.getByLabelText('Amount collected'), '40')
  await user.click(screen.getByRole('button', { name: 'Confirm' }))

  await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Confirm' })).toBeEnabled()
  )
  expect(screen.getByRole('alertdialog')).toBeInTheDocument()
  expect(screen.getByLabelText('Amount collected')).toHaveValue('40')
})
