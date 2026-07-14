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
/* eslint-disable react/iframe-missing-sandbox */
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { Spinner } from '@/components/ui/spinner'
import { useStatus } from '@/hooks/use-status'

const SHOP_CODE = '2F7A86NF'
// Direct cross-site embed of https://9.plus/shop/2F7A86NF breaks the shop's
// captcha: its PHPSESSID cookie has no SameSite=None, so browsers drop it
// inside a cross-site iframe. The backend therefore reverse-proxies the shop
// (controller/shop_proxy.go) and the iframe loads it same-origin — or from
// the dedicated SHOP_PROXY_HOST when the operator configured one.

export function Shop() {
  const { t } = useTranslation()
  const [loading, setLoading] = useState(true)
  const { status, loading: statusLoading } = useStatus()

  const proxyHost = (status?.shop_proxy_host as string | undefined) || ''
  const shopUrl = proxyHost
    ? `${window.location.protocol}//${proxyHost}/shop/${SHOP_CODE}`
    : `/shop/${SHOP_CODE}`

  // Only mount the iframe once /api/status has resolved: the proxy host is
  // known only then, and mounting earlier would first navigate to the panel
  // origin (which serves the SPA index in dedicated-host mode) and dismiss
  // the spinner for the wrong document.
  const statusReady = !statusLoading || status != null

  return (
    <SectionPageLayout fixedContent>
      <SectionPageLayout.Title>{t('Recharge Center')}</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <div className='relative h-full w-full'>
          {(loading || !statusReady) && (
            <div className='pointer-events-none absolute inset-0 flex items-center justify-center'>
              <Spinner className='size-8' />
            </div>
          )}
          {/* allow-same-origin is required for the shop's localStorage-based
              visitor session and its proxied session cookie; top navigation
              stays gated on user activation */}
          {statusReady && (
            <iframe
              src={shopUrl}
              title={t('Recharge Center')}
              onLoad={() => setLoading(false)}
              className='h-full w-full rounded-xl border-0'
              allow='payment; clipboard-write'
              sandbox='allow-scripts allow-same-origin allow-forms allow-popups allow-popups-to-escape-sandbox allow-top-navigation-by-user-activation'
            />
          )}
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
