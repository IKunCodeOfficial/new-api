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
import { describe, expect, test } from 'vitest'

import { PAYMENT_TYPES } from '../constants'
import type { TopupInfo } from '../types'
import {
  dispatchSelectedPayment,
  getDefaultPaymentType,
  getMinTopupAmount,
  getWooshpayAmountError,
  getWooshpayPresetPricing,
  isStripePayment,
  isWooshpayPayment,
  isWaffoPayment,
  isWaffoPancakePayment,
} from './payment'

describe('payment type classification', () => {
  test('keeps Waffo and Waffo Pancake on their dedicated flows', () => {
    expect(isWaffoPayment(PAYMENT_TYPES.WAFFO)).toBe(true)
    expect(isWaffoPayment(PAYMENT_TYPES.WAFFO_PANCAKE)).toBe(false)
    expect(isWaffoPancakePayment(PAYMENT_TYPES.WAFFO_PANCAKE)).toBe(true)
    expect(isWaffoPancakePayment(PAYMENT_TYPES.WAFFO)).toBe(false)
    expect(isStripePayment(PAYMENT_TYPES.STRIPE)).toBe(true)
  })

  test('recognises only the wooshpay type as WooshPay', () => {
    expect(isWooshpayPayment(PAYMENT_TYPES.WOOSHPAY)).toBe(true)
    expect(isWooshpayPayment(PAYMENT_TYPES.STRIPE)).toBe(false)
    expect(isStripePayment(PAYMENT_TYPES.WOOSHPAY)).toBe(false)
  })
})

describe('WooshPay top-up defaults', () => {
  const wooshpayOnlyTopupInfo: TopupInfo = {
    enable_online_topup: false,
    enable_stripe_topup: false,
    enable_wooshpay_topup: true,
    wooshpay_min_topup: 5,
    pay_methods: [],
    min_topup: 1,
    stripe_min_topup: 1,
    amount_options: [],
    discount: {},
  }

  test('selects WooshPay when it is the only enabled gateway without pay methods', () => {
    expect(getDefaultPaymentType(wooshpayOnlyTopupInfo)).toBe(
      PAYMENT_TYPES.WOOSHPAY
    )
  })

  test('uses the WooshPay minimum top-up when it is the only enabled gateway', () => {
    expect(getMinTopupAmount(wooshpayOnlyTopupInfo)).toBe(5)
  })

  test.each([
    ['Waffo', { enable_waffo_topup: true, waffo_min_topup: 2 }, 2],
    [
      'Waffo Pancake',
      { enable_waffo_pancake_topup: true, waffo_pancake_min_topup: 3 },
      3,
    ],
  ])(
    'keeps the %s minimum top-up when WooshPay is also enabled',
    (_gateway, gateway, expected) => {
      const topupInfo: TopupInfo = {
        ...wooshpayOnlyTopupInfo,
        wooshpay_min_topup: 50,
        ...gateway,
      }
      expect(getMinTopupAmount(topupInfo)).toBe(expected)
      expect(getDefaultPaymentType(topupInfo)).not.toBe(PAYMENT_TYPES.WOOSHPAY)
    }
  )
})

describe('payment dispatch', () => {
  test('keeps the selected Waffo method index through confirmation', async () => {
    const calls: string[] = []
    const success = await dispatchSelectedPayment(
      { name: 'Waffo Card', type: PAYMENT_TYPES.WAFFO },
      120,
      3,
      {
        regular: async () => {
          calls.push('regular')
          return false
        },
        waffo: async (amount, index) => {
          calls.push(`waffo:${amount}:${index}`)
          return true
        },
        waffoPancake: async () => {
          calls.push('pancake')
          return false
        },
      }
    )

    expect(success).toBe(true)
    expect(calls).toEqual(['waffo:120:3'])
  })

  test('does not create a Waffo order without a selected method index', async () => {
    let called = false
    const success = await dispatchSelectedPayment(
      { name: 'Waffo Card', type: PAYMENT_TYPES.WAFFO },
      120,
      null,
      {
        regular: async () => false,
        waffo: async () => {
          called = true
          return true
        },
        waffoPancake: async () => false,
      }
    )

    expect(success).toBe(false)
    expect(called).toBe(false)
  })
})

describe('WooshPay preset pricing', () => {
  // The top-up info carries this user's WooshPay CNY price (group ratio
  // included).
  const topupInfo: TopupInfo = {
    enable_online_topup: true,
    enable_stripe_topup: false,
    enable_wooshpay_topup: true,
    pay_methods: [
      { name: 'WooshPay', type: PAYMENT_TYPES.WOOSHPAY },
      { name: 'Alipay', type: 'alipay' },
    ],
    min_topup: 1,
    stripe_min_topup: 1,
    amount_options: [],
    discount: {},
    wooshpay_unit_price: 16,
  }

  test('prices presets with the per-user WooshPay price when WooshPay is the quoted type', () => {
    expect(getWooshpayPresetPricing(topupInfo, PAYMENT_TYPES.WOOSHPAY)).toEqual(
      { pricePerAmount: 16 }
    )
  })

  test('uses WooshPay pricing before any selection when WooshPay is the first pay method', () => {
    // The amount field is quoted for the default type, the first pay method,
    // so the cards must be priced for the same gateway.
    expect(
      getWooshpayPresetPricing(topupInfo, getDefaultPaymentType(topupInfo))
    ).toEqual({ pricePerAmount: 16 })
  })

  test('keeps the regular price while another method is quoted', () => {
    expect(getWooshpayPresetPricing(topupInfo, 'alipay')).toBeUndefined()
  })

  test('keeps the regular price without a usable WooshPay price', () => {
    for (const info of [
      { ...topupInfo, wooshpay_unit_price: undefined },
      { ...topupInfo, wooshpay_unit_price: 0 },
      null,
    ]) {
      expect(
        getWooshpayPresetPricing(info, PAYMENT_TYPES.WOOSHPAY)
      ).toBeUndefined()
    }
  })

  test('prices each token with the Tokens display', () => {
    expect(
      getWooshpayPresetPricing(topupInfo, PAYMENT_TYPES.WOOSHPAY, 500000)
    ).toEqual({ pricePerAmount: 16 / 500000 })
  })
})

describe('WooshPay amount checks', () => {
  test.each([1, 10000])('accepts %s units', (amount) => {
    expect(getWooshpayAmountError(amount)).toBeNull()
  })

  test('refuses more than 10000 units with the server message', () => {
    expect(getWooshpayAmountError(10001)).toEqual({
      key: 'Top-up amount cannot exceed {{max}}',
      params: { max: 10000 },
    })
  })

  test('refuses tokens that do not buy whole units with the server message', () => {
    expect(getWooshpayAmountError(1_250_000, 500000)).toEqual({
      key: 'With the Tokens display the top-up amount must be a multiple of {{unit}} tokens',
      params: { unit: 500000 },
    })
  })

  test('bounds the units bought with the Tokens display', () => {
    expect(getWooshpayAmountError(10000 * 500000, 500000)).toBeNull()
    expect(getWooshpayAmountError(10001 * 500000, 500000)).toEqual({
      key: 'Top-up amount cannot exceed {{max}}',
      params: { max: 10000 * 500000 },
    })
  })
})
