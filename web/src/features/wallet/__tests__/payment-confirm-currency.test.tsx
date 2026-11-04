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

import { INTERFACE_LANGUAGE_OPTIONS, toIntlLocale } from '@/i18n/languages'
import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'

import { PaymentConfirmDialog } from '../components/dialogs/payment-confirm-dialog'
import { RechargeFormCard } from '../components/recharge-form-card'
import { PAYMENT_TYPES } from '../constants'
import { formatGatewayAmount, type GatewayPresetPricing } from '../lib'
import type { GatewayCurrency } from '../types'

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

function renderDialog(
  paymentAmount: number,
  paymentCurrency?: GatewayCurrency
) {
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
  { currency: 'USD', minorDigits: 2, amount: 16, shown: /^USD\s16\.00$/ },
  { currency: 'JPY', minorDigits: 0, amount: 800, shown: /^JPY\s800$/ },
  { currency: 'KWD', minorDigits: 3, amount: 1.234, shown: /^KWD\s1\.234$/ },
  { currency: 'KWD', minorDigits: 3, amount: 16, shown: /^KWD\s16\.000$/ },
  // WooshPay's minor unit for IQD has 3 digits; Intl's own default has 0.
  { currency: 'IQD', minorDigits: 3, amount: 16, shown: /^IQD\s16\.000$/ },
])(
  'a WooshPay payment of $amount $currency shows exactly the quoted minor-unit digits',
  ({ currency, minorDigits, amount, shown }) => {
    renderDialog(amount, { currency, minorDigits })
    expect(screen.getByText(shown)).toBeVisible()
  }
)

it('the WooshPay currency amount follows the interface language', async () => {
  renderDialog(1234.5, { currency: 'USD', minorDigits: 2 })
  expect(screen.getByText(/^USD\s1,234\.50$/)).toBeVisible()
  await act(() => i18n.changeLanguage('zh'))
  expect(screen.getByText(/USD\s?1,234\.50/)).toBeVisible()
})

it('a payment without a gateway currency keeps the plain amount', () => {
  renderDialog(16)
  expect(screen.getByText('16')).toBeVisible()
  expect(screen.queryByText(/USD/)).toBeNull()
})

function renderPresets(
  gatewayPricing?: GatewayPresetPricing,
  paymentCurrency?: GatewayCurrency
) {
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
        paymentCurrency={paymentCurrency}
      />
    </I18nextProvider>
  )
}

it('WooshPay preset cards show the WooshPay price in its currency', () => {
  renderPresets({ currency: 'USD', minorDigits: 2, pricePerAmount: 8 })
  expect(screen.getByText(/^Pay USD\s16\.00$/)).toBeVisible()
  expect(screen.getByText(/^Pay USD\s32\.00/)).toBeVisible()
  expect(screen.getByText(/Save USD\s8\.00/)).toBeVisible()
})

it('preset cards without gateway pricing keep the regular price', () => {
  renderPresets()
  expect(screen.getByText('Pay 14.6')).toBeVisible()
  expect(screen.queryByText(/USD/)).toBeNull()
})

it('the amount field shows the quoted WooshPay amount in the quoted currency', () => {
  renderPresets(
    { currency: 'JPY', minorDigits: 0, pricePerAmount: 800 },
    { currency: 'JPY', minorDigits: 0 }
  )
  expect(screen.getByText(/^JPY\s16$/)).toBeVisible()
})

// Each interface language goes through toIntlLocale before reaching Intl;
// the digits are always the currency's minor-unit digits.
it.each([
  { language: 'zhCN', shown: /^KWD\s1,234\.500$/ },
  { language: 'zhTW', shown: /^KWD\s1,234\.500$/ },
  { language: 'en', shown: /^KWD\s1,234\.500$/ },
  { language: 'fr', shown: /^1\s234,500\sKWD$/ },
  { language: 'ru', shown: /^1\s234,500\sKWD$/ },
  { language: 'ja', shown: /^KWD\s1,234\.500$/ },
  { language: 'vi', shown: /^1\.234,500\sKWD$/ },
])(
  'formats a gateway amount for the $language interface language',
  ({ language, shown }) => {
    expect(
      formatGatewayAmount(1234.5, 'KWD', 3, toIntlLocale(language))
    ).toMatch(shown)
  }
)

it('covers every interface language', () => {
  expect(
    INTERFACE_LANGUAGE_OPTIONS.map((option) => option.code).sort()
  ).toEqual(['en', 'fr', 'ja', 'ru', 'vi', 'zhCN', 'zhTW'])
})

it.each(['not a language!', 'zhCN'])(
  'an invalid locale %s falls back to the default locale with the same digits',
  (locale) => {
    expect(toIntlLocale('not a language!')).toBeUndefined()
    expect(formatGatewayAmount(1234.5, 'JPY', 0, locale)).toMatch(/1.?235/)
    expect(formatGatewayAmount(1234.5, 'JPY', 0, locale)).toMatch(/JPY/)
  }
)

it('an unknown currency code falls back to the plain amount and the code', () => {
  expect(formatGatewayAmount(16, 'NOT-A-CODE', 2, 'en')).toBe(
    '16.00 NOT-A-CODE'
  )
})
