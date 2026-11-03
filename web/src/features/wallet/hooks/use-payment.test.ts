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
import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { PAYMENT_TYPES } from '../constants'
import { requestPaymentAmount, usePayment } from './use-payment'

describe('payment amount routing', () => {
  test('uses the dedicated Waffo amount calculator', async () => {
    const calls: string[] = []
    const amount = await requestPaymentAmount(120, PAYMENT_TYPES.WAFFO, {
      regular: async () => {
        calls.push('regular')
        return { success: true, data: '1' }
      },
      stripe: async () => {
        calls.push('stripe')
        return { success: true, data: '2' }
      },
      wooshpay: async () => {
        calls.push('wooshpay')
        return { success: true, data: '3' }
      },
      waffo: async (request) => {
        calls.push(`waffo:${request.amount}`)
        return { success: true, data: '18.75' }
      },
      waffoPancake: async () => {
        calls.push('pancake')
        return { success: true, data: '4' }
      },
    })

    expect(amount).toBe(18.75)
    expect(calls).toEqual(['waffo:120'])
  })

  test('uses the dedicated WooshPay amount calculator', async () => {
    const calls: string[] = []
    const amount = await requestPaymentAmount(10, PAYMENT_TYPES.WOOSHPAY, {
      regular: async () => {
        calls.push('regular')
        return { success: true, data: '1' }
      },
      stripe: async () => {
        calls.push('stripe')
        return { success: true, data: '2' }
      },
      wooshpay: async (request) => {
        calls.push(`wooshpay:${request.amount}`)
        return { success: true, data: '80.00' }
      },
      waffo: async () => {
        calls.push('waffo')
        return { success: true, data: '4' }
      },
      waffoPancake: async () => {
        calls.push('pancake')
        return { success: true, data: '5' }
      },
    })

    expect(amount).toBe(80)
    expect(calls).toEqual(['wooshpay:10'])
  })
})

describe('hosted checkout redirect', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    window.location.hash = ''
  })

  async function pay(paymentType: string, payLink: string) {
    vi.spyOn(api, 'post').mockResolvedValue({
      data: { message: 'success', data: { pay_link: payLink } },
    })
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const { result } = renderHook(() => usePayment())
    let succeeded = false
    await act(async () => {
      succeeded = await result.current.processPayment(10, paymentType)
    })
    return { succeeded, open }
  }

  test('WooshPay redirects in the same tab instead of opening a popup', async () => {
    // A fragment-only change keeps jsdom on the page while still recording
    // the navigation in location.href.
    const payLink = `${window.location.origin}${window.location.pathname}#wooshpay-checkout`
    const { succeeded, open } = await pay(PAYMENT_TYPES.WOOSHPAY, payLink)

    expect(succeeded).toBe(true)
    expect(open).not.toHaveBeenCalled()
    expect(window.location.href).toBe(payLink)
  })

  test('Stripe still opens its checkout in a new tab', async () => {
    const payLink = 'https://checkout.stripe.com/c/pay/cs_test'
    const { succeeded, open } = await pay(PAYMENT_TYPES.STRIPE, payLink)

    expect(succeeded).toBe(true)
    expect(open).toHaveBeenCalledWith(payLink, '_blank')
  })
})
