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
import { act, render, screen } from '@testing-library/react'
import { createInstance } from 'i18next'
import { I18nextProvider, initReactI18next } from 'react-i18next'
import { beforeEach, expect, it, vi } from 'vitest'

import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'

import { PaymentConfirmDialog } from '../components/dialogs/payment-confirm-dialog'
import { PAYMENT_TYPES } from '../constants'

const i18n = createInstance()

beforeEach(async () => {
  await i18n.use(initReactI18next).init({
    lng: 'en',
    fallbackLng: false,
    nsSeparator: false,
    resources: { en, zh },
    interpolation: { escapeValue: false },
  })
})

function renderDialog(paymentAmount: number, paymentCurrency?: string) {
  render(
    <I18nextProvider i18n={i18n}>
      <PaymentConfirmDialog
        open
        onOpenChange={vi.fn()}
        onConfirm={vi.fn()}
        topupAmount={2}
        paymentAmount={paymentAmount}
        paymentMethod={{ name: 'WooshPay', type: PAYMENT_TYPES.WOOSHPAY }}
        calculating={false}
        processing={false}
        paymentCurrency={paymentCurrency}
      />
    </I18nextProvider>
  )
}

it.each([
  { currency: 'USD', amount: 16, shown: /^USD\s16\.00$/ },
  { currency: 'JPY', amount: 800, shown: /^JPY\s800$/ },
  { currency: 'KWD', amount: 1.234, shown: /^KWD\s1\.234$/ },
])(
  'a WooshPay payment in $currency shows the amount with its currency and minor-unit digits',
  ({ currency, amount, shown }) => {
    renderDialog(amount, currency)
    expect(screen.getByText(shown)).toBeVisible()
  }
)

it('the WooshPay currency amount follows the interface language', async () => {
  renderDialog(1234.5, 'USD')
  expect(screen.getByText(/^USD\s1,234\.50$/)).toBeVisible()
  await act(() => i18n.changeLanguage('zh'))
  expect(screen.getByText(/USD\s?1,234\.50/)).toBeVisible()
})

it('a payment without a gateway currency keeps the plain amount', () => {
  renderDialog(16)
  expect(screen.getByText('16')).toBeVisible()
  expect(screen.queryByText(/USD/)).toBeNull()
})
