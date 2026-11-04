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
import { getServerMessage } from '@/lib/server-error-message'

import {
  PAYMENT_TYPES,
  DEFAULT_PRESET_MULTIPLIERS,
  DEFAULT_PAYMENT_TYPE,
  DEFAULT_MIN_TOPUP,
} from '../constants'
import type { PaymentMethod, PresetAmount, TopupInfo } from '../types'

// ============================================================================
// Payment Processing Functions
// ============================================================================

/**
 * Payment endpoints answer errors as {"message": "error", "data": text}. When
 * the response also carries a message_key, this is its translation; callers
 * fall back to the text they already read.
 */
export function getPaymentErrorTranslation(
  response: unknown
): string | undefined {
  if (!response || typeof response !== 'object') return undefined
  const body = response as Record<string, unknown>
  if (typeof body.message_key !== 'string' || !body.message_key) {
    return undefined
  }
  return getServerMessage(body)
}

/**
 * Check if browser is Safari
 */
function isSafariBrowser(): boolean {
  return (
    navigator.userAgent.includes('Safari') &&
    !navigator.userAgent.includes('Chrome')
  )
}

/**
 * Submit payment form (for non-Stripe payments)
 */
export function submitPaymentForm(
  url: string,
  params: Record<string, unknown>
): void {
  const form = document.createElement('form')
  form.action = url
  form.method = 'POST'

  // Don't open in new tab for Safari
  if (!isSafariBrowser()) {
    form.target = '_blank'
  }

  // Add form parameters
  Object.entries(params).forEach(([key, value]) => {
    const input = document.createElement('input')
    input.type = 'hidden'
    input.name = key
    input.value = String(value)
    form.appendChild(input)
  })

  document.body.appendChild(form)
  form.submit()
  document.body.removeChild(form)
}

/**
 * Check if payment method is Stripe
 */
export function isStripePayment(paymentType: string): boolean {
  return paymentType === PAYMENT_TYPES.STRIPE
}

/**
 * Check if payment method is WooshPay
 */
export function isWooshpayPayment(paymentType: string): boolean {
  return paymentType === PAYMENT_TYPES.WOOSHPAY
}

/**
 * Check if payment method is Waffo
 */
export function isWaffoPayment(paymentType: string): boolean {
  return paymentType === PAYMENT_TYPES.WAFFO
}

/**
 * Check if payment method is Waffo Pancake
 *
 * Pancake is a metered-style payment that goes through a dedicated checkout
 * URL flow rather than the generic epay form submission, so it must be
 * special-cased in payment dispatch logic.
 */
export function isWaffoPancakePayment(paymentType: string): boolean {
  return paymentType === PAYMENT_TYPES.WAFFO_PANCAKE
}

export interface PaymentProcessors {
  regular: (topupAmount: number, paymentType: string) => Promise<boolean>
  waffo: (topupAmount: number, payMethodIndex: number) => Promise<boolean>
  waffoPancake: (topupAmount: number) => Promise<boolean>
}

export async function dispatchSelectedPayment(
  paymentMethod: PaymentMethod,
  topupAmount: number,
  waffoMethodIndex: number | null,
  processors: PaymentProcessors
): Promise<boolean> {
  if (isWaffoPayment(paymentMethod.type)) {
    if (waffoMethodIndex === null) {
      return false
    }
    return processors.waffo(topupAmount, waffoMethodIndex)
  }

  if (isWaffoPancakePayment(paymentMethod.type)) {
    return processors.waffoPancake(topupAmount)
  }

  return processors.regular(topupAmount, paymentMethod.type)
}

/**
 * Get default payment type from topup info
 */
export function getDefaultPaymentType(topupInfo: TopupInfo | null): string {
  if (!topupInfo) {
    return DEFAULT_PAYMENT_TYPE
  }

  // Return first available payment method or default
  if (topupInfo.pay_methods?.length > 0) {
    return topupInfo.pay_methods[0].type
  }

  if (topupInfo.enable_stripe_topup) {
    return PAYMENT_TYPES.STRIPE
  }

  if (topupInfo.enable_waffo_topup) {
    return PAYMENT_TYPES.WAFFO
  }

  if (topupInfo.enable_waffo_pancake_topup) {
    return PAYMENT_TYPES.WAFFO_PANCAKE
  }

  if (topupInfo.enable_wooshpay_topup) {
    return PAYMENT_TYPES.WOOSHPAY
  }

  return DEFAULT_PAYMENT_TYPE
}

/**
 * Get minimum topup amount from topup info
 *
 * WooshPay comes last: its own minimum only applies when no earlier gateway
 * is enabled, and its pay method button carries its minimum (min_topup) so
 * it never raises the minimum of the other methods.
 */
export function getMinTopupAmount(topupInfo: TopupInfo | null): number {
  if (!topupInfo) {
    return DEFAULT_MIN_TOPUP
  }

  if (topupInfo.enable_online_topup) {
    return topupInfo.min_topup
  }

  if (topupInfo.enable_stripe_topup) {
    return topupInfo.stripe_min_topup
  }

  if (topupInfo.enable_waffo_topup) {
    return topupInfo.waffo_min_topup || DEFAULT_MIN_TOPUP
  }

  if (topupInfo.enable_waffo_pancake_topup) {
    return topupInfo.waffo_pancake_min_topup || DEFAULT_MIN_TOPUP
  }

  if (topupInfo.enable_wooshpay_topup) {
    return topupInfo.wooshpay_min_topup || DEFAULT_MIN_TOPUP
  }

  return DEFAULT_MIN_TOPUP
}

/**
 * CNY price of the preset amount cards for a gateway that charges in CNY
 * (WooshPay). pricePerAmount is the price of one unit of the entered amount
 * (one token with the Tokens display).
 */
export interface GatewayPresetPricing {
  pricePerAmount: number
}

/**
 * WooshPay charges its unit price, scaled by the user's top-up group ratio,
 * in CNY; the top-up info states it for the current user. The
 * preset cards use that price whenever WooshPay is the payment type the
 * amount field is quoted for: the selected method, or the default one (the
 * first pay method) before any is selected.
 */
export function getWooshpayPresetPricing(
  topupInfo: TopupInfo | null,
  paymentType: string,
  tokensPerUnit?: number
): GatewayPresetPricing | undefined {
  const unitPrice = Number(topupInfo?.wooshpay_unit_price)
  if (!isWooshpayPayment(paymentType) || !(unitPrice > 0)) {
    return undefined
  }
  const perUnit = tokensPerUnit && tokensPerUnit > 0 ? tokensPerUnit : 1
  return { pricePerAmount: unitPrice / perUnit }
}

/** Most units one WooshPay order may buy (setting.WooshpayMaxTopUpUnits). */
export const WOOSHPAY_MAX_TOPUP_UNITS = 10000

/**
 * Checks a WooshPay amount before asking the server, with the server's own
 * messages: with the Tokens display (tokensPerUnit set) the amount must buy
 * whole units, and an order buys at most 10000 units. Returns the message key
 * and its parameters, or null when the amount is acceptable.
 */
export function getWooshpayAmountError(
  amount: number,
  tokensPerUnit?: number
): { key: string; params: Record<string, number> } | null {
  const perUnit = tokensPerUnit && tokensPerUnit > 0 ? tokensPerUnit : 0
  if (perUnit > 0 && amount % perUnit !== 0) {
    return {
      key: 'With the Tokens display the top-up amount must be a multiple of {{unit}} tokens',
      params: { unit: perUnit },
    }
  }
  const units = perUnit > 0 ? amount / perUnit : amount
  if (units > WOOSHPAY_MAX_TOPUP_UNITS) {
    return {
      key: 'Top-up amount cannot exceed {{max}}',
      params: {
        max:
          perUnit > 0
            ? Math.floor(WOOSHPAY_MAX_TOPUP_UNITS * perUnit)
            : WOOSHPAY_MAX_TOPUP_UNITS,
      },
    }
  }
  return null
}

/**
 * Generate preset amounts based on minimum topup
 */
export function generatePresetAmounts(minAmount: number): PresetAmount[] {
  return DEFAULT_PRESET_MULTIPLIERS.map((multiplier) => ({
    value: minAmount * multiplier,
  }))
}

/**
 * Merge custom preset amounts with discounts
 */
export function mergePresetAmounts(
  amountOptions: number[],
  discounts: Record<number, number>
): PresetAmount[] {
  if (!amountOptions || amountOptions.length === 0) {
    return []
  }

  return amountOptions.map((amount) => ({
    value: amount,
    discount: discounts[amount] || 1.0,
  }))
}
