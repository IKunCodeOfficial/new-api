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
import i18next from 'i18next'
import { useState, useCallback } from 'react'
import { toast } from 'sonner'

import { handleServerError } from '@/lib/handle-server-error'
import { createServerError } from '@/lib/server-error-message'

import {
  calculateAmount,
  calculateStripeAmount,
  calculateWaffoAmount,
  calculateWaffoPancakeAmount,
  calculateWooshpayAmount,
  requestPayment,
  requestStripePayment,
  requestWooshpayPayment,
  isApiSuccess,
} from '../api'
import {
  getWooshpayAmountError,
  isStripePayment,
  isWaffoPayment,
  isWaffoPancakePayment,
  isWooshpayPayment,
  submitPaymentForm,
} from '../lib'
import type {
  AmountRequest,
  AmountResponse,
  PaymentQuote,
  WooshpayAmountResponse,
} from '../types'

// ============================================================================
// Payment Hook
// ============================================================================

type AmountCalculator = (request: AmountRequest) => Promise<AmountResponse>

export interface PaymentAmountCalculators {
  regular: AmountCalculator
  stripe: AmountCalculator
  wooshpay: (request: AmountRequest) => Promise<WooshpayAmountResponse>
  waffo: AmountCalculator
  waffoPancake: AmountCalculator
}

const EMPTY_QUOTE: PaymentQuote = { amount: 0 }

const defaultPaymentAmountCalculators: PaymentAmountCalculators = {
  regular: calculateAmount,
  stripe: calculateStripeAmount,
  wooshpay: calculateWooshpayAmount,
  waffo: calculateWaffoAmount,
  waffoPancake: calculateWaffoPancakeAmount,
}

/**
 * Asks the server what paymentType charges for topupAmount. WooshPay answers
 * with the amount in its own currency and that currency's minor digits. A
 * failed request throws the server's error; it is never a zero amount.
 */
export async function requestPaymentAmount(
  topupAmount: number,
  paymentType: string,
  calculators: PaymentAmountCalculators = defaultPaymentAmountCalculators
): Promise<PaymentQuote> {
  const fallback = i18next.t('Failed to calculate the payment amount')
  if (isWooshpayPayment(paymentType)) {
    const response = await calculators.wooshpay({ amount: topupAmount })
    const amount = Number.parseFloat(response.data?.amount ?? '')
    if (!isApiSuccess(response) || !response.data || !Number.isFinite(amount)) {
      throw createServerError(response, fallback)
    }
    return {
      amount,
      currency: {
        currency: response.data.currency,
        minorDigits: response.data.minor_digits,
      },
    }
  }

  let calculator = calculators.regular
  if (isStripePayment(paymentType)) {
    calculator = calculators.stripe
  } else if (isWaffoPayment(paymentType)) {
    calculator = calculators.waffo
  } else if (isWaffoPancakePayment(paymentType)) {
    calculator = calculators.waffoPancake
  }

  const response = await calculator({ amount: topupAmount })
  if (!isApiSuccess(response) || !response.data) {
    throw createServerError(response, fallback)
  }
  return { amount: Number.parseFloat(response.data) }
}

export interface CalculatePaymentAmountOptions {
  /** Units of the Tokens display, which WooshPay amounts must be multiples of */
  tokensPerUnit?: number
  /** Show why the amount cannot be paid (on choosing a payment method) */
  notifyFailure?: boolean
}

export function usePayment() {
  const [quote, setQuote] = useState<PaymentQuote>(EMPTY_QUOTE)
  const [calculating, setCalculating] = useState(false)
  const [processing, setProcessing] = useState(false)

  // Calculate payment amount. Resolves to null when the amount cannot be
  // paid with paymentType; the shown amount then falls back to 0.
  const calculatePaymentAmount = useCallback(
    async (
      topupAmount: number,
      paymentType: string,
      options: CalculatePaymentAmountOptions = {}
    ): Promise<PaymentQuote | null> => {
      if (isWooshpayPayment(paymentType)) {
        // The server gives the same answers; checking first spares the
        // request and explains the refusal right away.
        const amountError = getWooshpayAmountError(
          topupAmount,
          options.tokensPerUnit
        )
        if (amountError) {
          setQuote(EMPTY_QUOTE)
          if (options.notifyFailure) {
            toast.error(i18next.t(amountError.key, amountError.params))
          }
          return null
        }
      }
      try {
        setCalculating(true)
        const calculated = await requestPaymentAmount(topupAmount, paymentType)
        setQuote(calculated)
        return calculated
      } catch (error) {
        setQuote(EMPTY_QUOTE)
        if (options.notifyFailure) {
          handleServerError(
            error,
            i18next.t('Failed to calculate the payment amount')
          )
        }
        return null
      } finally {
        setCalculating(false)
      }
    },
    []
  )

  // Process payment
  const processPayment = useCallback(
    async (topupAmount: number, paymentType: string) => {
      try {
        setProcessing(true)

        const isStripe = isStripePayment(paymentType)
        const isWooshpay = isWooshpayPayment(paymentType)
        const isPayLinkGateway = isStripe || isWooshpay
        const amount = Math.floor(topupAmount)

        let response
        if (isStripe) {
          response = await requestStripePayment({
            amount,
            payment_method: 'stripe',
          })
        } else if (isWooshpay) {
          response = await requestWooshpayPayment({
            amount,
            payment_method: 'wooshpay',
          })
        } else {
          response = await requestPayment({
            amount,
            payment_method: paymentType,
          })
        }

        if (!isApiSuccess(response)) {
          handleServerError(response, i18next.t('Payment request failed'))
          return false
        }

        // Stripe and WooshPay return a hosted checkout link. WooshPay
        // redirects in the same tab like Waffo Pancake: the user gesture is
        // lost across the await, so a new window would be popup-blocked.
        if (isPayLinkGateway && response.data?.pay_link) {
          toast.success(i18next.t('Redirecting to payment page...'))
          if (isWooshpay) {
            window.location.href = response.data.pay_link as string
          } else {
            window.open(response.data.pay_link as string, '_blank')
          }
          return true
        }

        // Handle epay form payment
        if (!isPayLinkGateway && response.data) {
          const url = (response as unknown as { url?: string }).url
          if (url) {
            submitPaymentForm(url, response.data)
            toast.success(i18next.t('Redirecting to payment page...'))
            return true
          }
        }

        return false
      } catch (error) {
        handleServerError(error, i18next.t('Payment request failed'))
        return false
      } finally {
        setProcessing(false)
      }
    },
    []
  )

  return {
    amount: quote.amount,
    paymentCurrency: quote.currency,
    calculating,
    processing,
    calculatePaymentAmount,
    processPayment,
  }
}
