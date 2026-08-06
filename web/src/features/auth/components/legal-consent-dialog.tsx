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
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'

import type { SystemStatus } from '../types'

type LegalConsentDialogProps = {
  status: SystemStatus | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onConfirm: () => void
}

export function LegalConsentDialog(props: LegalConsentDialogProps) {
  const { t } = useTranslation()
  const hasUserAgreement = Boolean(props.status?.user_agreement_enabled)
  const hasPrivacyPolicy = Boolean(props.status?.privacy_policy_enabled)

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Review and accept the terms')}
      description={t(
        'Please accept the following terms before continuing with third-party sign-in.'
      )}
      contentClassName='max-w-md'
      contentHeight='auto'
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => props.onOpenChange(false)}
          >
            {t('Cancel')}
          </Button>
          <Button type='button' onClick={props.onConfirm}>
            {t('Agree and continue')}
          </Button>
        </>
      }
    >
      <div className='grid gap-2 text-sm'>
        {hasUserAgreement && (
          <a
            href='/user-agreement'
            target='_blank'
            rel='noopener noreferrer'
            className='border-border hover:bg-muted rounded-md border px-3 py-2 font-medium transition-colors'
          >
            {t('User Agreement')}
          </a>
        )}
        {hasPrivacyPolicy && (
          <a
            href='/privacy-policy'
            target='_blank'
            rel='noopener noreferrer'
            className='border-border hover:bg-muted rounded-md border px-3 py-2 font-medium transition-colors'
          >
            {t('Privacy Policy')}
          </a>
        )}
      </div>
    </Dialog>
  )
}
