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
import { beforeEach, describe, expect, test } from 'vitest'

import { useSystemConfigStore } from '@/stores/system-config-store'

import { isPaidQuotaInvalid, parsePaidQuota } from '../rebate-recharge'

type CurrencyFixture = {
  quotaDisplayType: 'USD' | 'CNY' | 'TOKENS'
  quotaPerUnit: number
  usdExchangeRate: number
}

function useCurrency(currency: CurrencyFixture): void {
  useSystemConfigStore.getState().setConfig({
    currency: {
      displayInCurrency: true,
      quotaDisplayType: currency.quotaDisplayType,
      quotaPerUnit: currency.quotaPerUnit,
      usdExchangeRate: currency.usdExchangeRate,
      customCurrencySymbol: '¤',
      customCurrencyExchangeRate: 1,
    },
  })
}

/** Credited quota for an amount entered in the same display unit as the dialog. */
const CREDITED = 5_000_000 // $10 at 500000 quota/unit

beforeEach(() => {
  useCurrency({
    quotaDisplayType: 'USD',
    quotaPerUnit: 500000,
    usdExchangeRate: 1,
  })
})

describe('parsePaidQuota', () => {
  test('defaults an empty input to the credited amount', () => {
    expect(parsePaidQuota('', CREDITED)).toBe(CREDITED)
    expect(parsePaidQuota('   ', CREDITED)).toBe(CREDITED)
  })

  test('keeps the credited amount when the full price was paid', () => {
    expect(parsePaidQuota('10', CREDITED)).toBe(CREDITED)
  })

  test('reports the discounted amount when less was paid', () => {
    expect(parsePaidQuota('8', CREDITED)).toBe(4_000_000)
  })

  test('converts through the configured display currency', () => {
    useCurrency({
      quotaDisplayType: 'CNY',
      quotaPerUnit: 500000,
      usdExchangeRate: 7.2,
    })

    expect(parsePaidQuota('20', CREDITED)).toBe(1_388_889)
    expect(parsePaidQuota('10', CREDITED)).toBe(694_444)
  })

  test('treats the display amount as quota units in token mode', () => {
    useCurrency({
      quotaDisplayType: 'TOKENS',
      quotaPerUnit: 500000,
      usdExchangeRate: 1,
    })

    expect(parsePaidQuota('4000000', CREDITED)).toBe(4_000_000)
  })

  test.each(['8。5', '８．５', ' 8.5 ', '8,5', '8，5', '8,50'])(
    'reads %s as the decimal amount 8.5',
    (entered) => {
      expect(parsePaidQuota(entered, CREDITED)).toBe(4_250_000)
    }
  )

  test.each(['abc', '1,000', '8,500', '1,000.5', '8,5,1', '$8', '-8', '.5'])(
    'reports %s as malformed instead of falling back to the full price',
    (entered) => {
      expect(parsePaidQuota(entered, CREDITED)).toBeNull()
    }
  )

  test.each(['8.', '8,', '8，'])(
    'reads %s, an amount still being typed, as the whole number',
    (entered) => {
      expect(parsePaidQuota(entered, CREDITED)).toBe(4_000_000)
    }
  )

  test('keeps a zero input as zero so the range check rejects it', () => {
    expect(parsePaidQuota('0', CREDITED)).toBe(0)
  })
})

describe('isPaidQuotaInvalid', () => {
  test('rejects a paid amount that is not positive', () => {
    expect(isPaidQuotaInvalid(CREDITED, 0)).toBe(true)
    expect(isPaidQuotaInvalid(CREDITED, -1)).toBe(true)
  })

  test('rejects a paid amount above the credited amount', () => {
    expect(isPaidQuotaInvalid(CREDITED, CREDITED + 1)).toBe(true)
  })

  test('accepts a paid amount between zero and the credited amount', () => {
    expect(isPaidQuotaInvalid(CREDITED, 1)).toBe(false)
    expect(isPaidQuotaInvalid(CREDITED, CREDITED)).toBe(false)
  })
})
