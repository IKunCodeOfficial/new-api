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
import { RechargeFormCard } from '../components/recharge-form-card'
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

function renderPresets(gatewayPricing?: {
  currency: string
  pricePerAmount: number
}) {
  render(
    <I18nextProvider i18n={i18n}>
      <RechargeFormCard
        topupInfo={{
          enable_online_topup: false,
          enable_stripe_topup: false,
          enable_wooshpay_topup: true,
          wooshpay_min_topup: 1,
          pay_methods: [{ name: 'WooshPay', type: PAYMENT_TYPES.WOOSHPAY }],
          min_topup: 1,
          stripe_min_topup: 1,
          amount_options: [],
          discount: {},
        }}
        presetAmounts={[{ value: 2 }, { value: 5, discount: 0.8 }]}
        selectedPreset={null}
        onSelectPreset={vi.fn()}
        topupAmount={2}
        onTopupAmountChange={vi.fn()}
        paymentAmount={16}
        calculating={false}
        onPaymentMethodSelect={vi.fn()}
        paymentLoading={null}
        redemptionCode=''
        onRedemptionCodeChange={vi.fn()}
        onRedeem={vi.fn()}
        redeeming={false}
        priceRatio={7.3}
        gatewayPricing={gatewayPricing}
      />
    </I18nextProvider>
  )
}

it('WooshPay preset cards show the WooshPay price in its currency', () => {
  renderPresets({ currency: 'USD', pricePerAmount: 8 })
  expect(screen.getByText(/^Pay USD\s16\.00$/)).toBeVisible()
  expect(screen.getByText(/^Pay USD\s32\.00/)).toBeVisible()
  expect(screen.getByText(/Save USD\s8\.00/)).toBeVisible()
})

it('preset cards without gateway pricing keep the regular price', () => {
  renderPresets()
  expect(screen.getByText('Pay 14.6')).toBeVisible()
  expect(screen.queryByText(/USD/)).toBeNull()
})
