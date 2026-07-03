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

const SHOP_URL = 'https://9.plus/shop/2F7A86NF'

export function Shop() {
  const { t } = useTranslation()
  const [loading, setLoading] = useState(true)

  return (
    <SectionPageLayout fixedContent>
      <SectionPageLayout.Title>{t('Recharge Center')}</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <div className='relative h-full w-full'>
          {loading && (
            <div className='pointer-events-none absolute inset-0 flex items-center justify-center'>
              <Spinner className='size-8' />
            </div>
          )}
          {/* allow-same-origin is required for the shop's localStorage-based
              visitor session; top navigation stays gated on user activation */}
          <iframe
            src={SHOP_URL}
            title={t('Recharge Center')}
            onLoad={() => setLoading(false)}
            className='h-full w-full rounded-xl border-0'
            allow='payment; clipboard-write'
            sandbox='allow-scripts allow-same-origin allow-forms allow-popups allow-popups-to-escape-sandbox allow-top-navigation-by-user-activation'
          />
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
