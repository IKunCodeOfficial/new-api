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
import { formatCnyAmount, type GatewayPresetPricing } from '../lib'

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

function renderDialog(paymentAmount: number, paymentInCny?: boolean) {
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
        paymentInCny={paymentInCny}
      />
    </I18nextProvider>
  )
}

it.each([
  { amount: 16, shown: /^CNY\s16\.00$/ },
  { amount: 73, shown: /^CNY\s73\.00$/ },
  { amount: 1.2, shown: /^CNY\s1\.20$/ },
])(
  'a WooshPay payment of $amount shows the CNY amount with 2 decimals',
  ({ amount, shown }) => {
    renderDialog(amount, true)
    expect(screen.getByText(shown)).toBeVisible()
  }
)

it('the WooshPay CNY amount follows the interface language', async () => {
  renderDialog(1234.5, true)
  expect(screen.getByText(/^CNY\s1,234\.50$/)).toBeVisible()
  await act(() => i18n.changeLanguage('zh'))
  expect(screen.getByText(/CNY\s?1,234\.50/)).toBeVisible()
})

it('a payment not quoted in CNY keeps the plain amount', () => {
  renderDialog(16)
  expect(screen.getByText('16')).toBeVisible()
  expect(screen.queryByText(/CNY/)).toBeNull()
})

function renderPresets(
  gatewayPricing?: GatewayPresetPricing,
  paymentInCny?: boolean
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
        paymentInCny={paymentInCny}
      />
    </I18nextProvider>
  )
}

it('WooshPay preset cards show the WooshPay price in CNY', () => {
  renderPresets({ pricePerAmount: 8 })
  expect(screen.getByText(/^Pay CNY\s16\.00$/)).toBeVisible()
  expect(screen.getByText(/^Pay CNY\s32\.00/)).toBeVisible()
  expect(screen.getByText(/Save CNY\s8\.00/)).toBeVisible()
})

it('preset cards without gateway pricing keep the regular price', () => {
  renderPresets()
  expect(screen.getByText('Pay 14.6')).toBeVisible()
  expect(screen.queryByText(/CNY/)).toBeNull()
})

it('the amount field shows the quoted WooshPay amount in CNY', () => {
  renderPresets({ pricePerAmount: 8 }, true)
  expect(screen.getByText(/^CNY\s16\.00$/)).toBeVisible()
})

// Each interface language goes through toIntlLocale before reaching Intl;
// the amount always has 2 decimals.
it.each([
  { language: 'zhCN', shown: /^CNY\s1,234\.50$/ },
  { language: 'zhTW', shown: /^CNY\s1,234\.50$/ },
  { language: 'en', shown: /^CNY\s1,234\.50$/ },
  { language: 'fr', shown: /^1\s234,50\sCNY$/ },
  { language: 'ru', shown: /^1\s234,50\sCNY$/ },
  { language: 'ja', shown: /^CNY\s1,234\.50$/ },
  { language: 'vi', shown: /^1\.234,50\sCNY$/ },
])(
  'formats a CNY amount for the $language interface language',
  ({ language, shown }) => {
    expect(formatCnyAmount(1234.5, toIntlLocale(language))).toMatch(shown)
  }
)

it('covers every interface language', () => {
  expect(
    INTERFACE_LANGUAGE_OPTIONS.map((option) => option.code).sort()
  ).toEqual(['en', 'fr', 'ja', 'ru', 'vi', 'zhCN', 'zhTW'])
})

it.each(['not a language!', 'zhCN'])(
  'an invalid locale %s falls back to the default locale with 2 decimals',
  (locale) => {
    expect(toIntlLocale('not a language!')).toBeUndefined()
    expect(formatCnyAmount(1234.5, locale)).toMatch(/1.?234[.,]50/)
    expect(formatCnyAmount(1234.5, locale)).toMatch(/CNY/)
  }
)

it('a non-finite amount is shown as a dash', () => {
  expect(formatCnyAmount(Number.NaN, 'en')).toBe('-')
})
