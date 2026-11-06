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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState, type ComponentProps } from 'react'
import { toast } from 'sonner'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { PaymentSettingsSection } from '../payment-settings-section'

type SectionProps = ComponentProps<typeof PaymentSettingsSection>

const defaultValues: SectionProps['defaultValues'] = {
  PayAddress: '',
  EpayId: '',
  EpayKey: '',
  Price: 7.3,
  MinTopUp: 1,
  CustomCallbackAddress: '',
  PayMethods: '[]',
  AmountOptions: '[]',
  AmountDiscount: '{}',
  StripeApiSecret: '',
  StripeWebhookSecret: '',
  StripePriceId: '',
  StripeUnitPrice: 8,
  StripeMinTopUp: 1,
  StripePromotionCodesEnabled: false,
  WooshpayEnabled: false,
  WooshpaySecretKey: '',
  WooshpayWebhookSecret: '',
  WooshpayApiBase: 'https://api.wooshpay.com',
  WooshpayUnitPrice: 8,
  WooshpayMinTopUp: 1,
  CreemApiKey: '',
  CreemWebhookSecret: '',
  CreemTestMode: false,
  CreemProducts: '[]',
}

function PaymentFixture(props: { wooshpayTestMode?: boolean }) {
  const [actions, setActions] = useState<HTMLDivElement | null>(null)
  return (
    <SettingsPageProvider actionsContainer={actions}>
      <div ref={setActions} />
      <PaymentSettingsSection
        defaultValues={defaultValues}
        waffoDefaultValues={{
          WaffoEnabled: false,
          WaffoApiKey: '',
          WaffoPrivateKey: '',
          WaffoPublicCert: '',
          WaffoSandboxPublicCert: '',
          WaffoSandboxApiKey: '',
          WaffoSandboxPrivateKey: '',
          WaffoSandbox: false,
          WaffoMerchantId: '',
          WaffoCurrency: 'USD',
          WaffoUnitPrice: 1,
          WaffoMinTopUp: 1,
          WaffoNotifyUrl: '',
          WaffoReturnUrl: '',
          WaffoPayMethods: '[]',
        }}
        waffoPancakeDefaultValues={{
          WaffoPancakeMerchantID: '',
          WaffoPancakePrivateKey: '',
          WaffoPancakeReturnURL: '',
        }}
        waffoPancakeProvisionedStoreID='store_1'
        waffoPancakeProvisionedProductID='product_1'
        wooshpayTestMode={props.wooshpayTestMode}
        complianceDefaults={{
          confirmed: true,
          termsVersion: 'v1',
          confirmedAt: 1,
          confirmedBy: 1,
        }}
      />
    </SettingsPageProvider>
  )
}

function renderPayment(wooshpayTestMode = false) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <PaymentFixture wooshpayTestMode={wooshpayTestMode} />
    </QueryClientProvider>
  )
  return userEvent.setup()
}

async function editWooshpay(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole('tab', { name: 'WooshPay' }))
  await user.click(screen.getByRole('switch', { name: /Enable WooshPay/ }))
  const unitPrice = screen.getByRole('spinbutton', { name: 'Unit price' })
  await user.tripleClick(unitPrice)
  await user.keyboard('9.5')
}

beforeEach(() => {
  vi.spyOn(toast, 'success').mockReturnValue('saved')
  vi.spyOn(toast, 'error').mockReturnValue('failed')
})

afterEach(() => vi.restoreAllMocks())

describe('WooshPay settings', () => {
  it('the WooshPay tab has no currency field: checkouts are always in CNY', async () => {
    const user = renderPayment()
    await user.click(screen.getByRole('tab', { name: 'WooshPay' }))
    expect(screen.getByRole('spinbutton', { name: 'Unit price' })).toBeVisible()
    expect(screen.queryByRole('textbox', { name: 'Currency' })).toBeNull()
  })

  it('warns that test payments credit real quota when the saved key is a test key', async () => {
    const user = renderPayment(true)
    await user.click(screen.getByRole('tab', { name: 'WooshPay' }))
    expect(screen.getByText('WooshPay is in test mode')).toBeVisible()
  })

  it('shows no test-mode warning when the saved key is not a test key', async () => {
    const user = renderPayment(false)
    await user.click(screen.getByRole('tab', { name: 'WooshPay' }))
    expect(screen.queryByText('WooshPay is in test mode')).toBeNull()
  })

  it('saves every changed WooshPay setting in one request and none one by one', async () => {
    const put = vi.spyOn(api, 'put').mockResolvedValue({
      data: { success: true, message: '' },
    })
    const user = renderPayment()
    await editWooshpay(user)
    await user.click(screen.getByRole('button', { name: 'Save all settings' }))

    await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
    expect(put).toHaveBeenCalledWith('/api/option/wooshpay/environment', {
      enabled: true,
      unit_price: 9.5,
    })
  })

  it('a refused WooshPay save is reported and does not skip the Waffo Pancake binding', async () => {
    vi.spyOn(api, 'put').mockResolvedValue({
      data: {
        success: false,
        message: 'WooshPay can only be enabled once the keys are set',
      },
    })
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        message: 'success',
        data: { store_id: 'store_1', product_id: 'product_1' },
      },
    })
    const user = renderPayment()
    await editWooshpay(user)
    await user.click(screen.getByRole('tab', { name: 'Waffo Pancake' }))
    await user.type(screen.getByPlaceholderText('MER_xxx'), 'MER_1')
    await user.click(screen.getByRole('button', { name: 'Save all settings' }))

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith(
        '/api/option/waffo-pancake/save',
        expect.objectContaining({ merchant_id: 'MER_1' })
      )
    )
    expect(toast.error).toHaveBeenCalledWith(
      'WooshPay can only be enabled once the keys are set'
    )
  })
})
