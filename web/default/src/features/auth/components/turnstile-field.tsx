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
import { useState } from 'react'
import { ShieldAlert, ShieldCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { Turnstile } from '@/components/turnstile'

interface TurnstileFieldProps {
  siteKey: string
  token: string
  onVerify: (token: string) => void
  onExpire?: () => void
  className?: string
}

/**
 * Wraps the Cloudflare Turnstile widget with a persistent, always-visible
 * status label and guidance. This avoids the confusion where the challenge
 * silently fails (or never appears) and the user only sees a transient toast
 * after the submit / send-code button refuses to work.
 */
export function TurnstileField({
  siteKey,
  token,
  onVerify,
  onExpire,
  className,
}: TurnstileFieldProps) {
  const { t } = useTranslation()
  const [failed, setFailed] = useState(false)
  const verified = Boolean(token)

  return (
    <div
      className={cn(
        'flex flex-col gap-2 rounded-md border p-3',
        verified
          ? 'border-emerald-500/40 bg-emerald-500/5'
          : 'border-border/60 bg-muted/40',
        className
      )}
    >
      <div className='flex items-center gap-2 text-sm font-medium'>
        {verified ? (
          <ShieldCheck className='h-4 w-4 text-emerald-600' />
        ) : (
          <ShieldAlert className='h-4 w-4 text-amber-500' />
        )}
        <span>
          {verified
            ? t('Human verification passed')
            : t('Human verification required')}
        </span>
      </div>

      {!verified && (
        <p className='text-muted-foreground text-xs leading-5'>
          {failed
            ? t(
                'Human verification failed or expired. Please retry below, or switch to a different network environment and try again.'
              )
            : t(
                'Please complete the human verification below before continuing. If it does not appear, switch to a different network environment and try again.'
              )}
        </p>
      )}

      <div className={cn(verified && 'opacity-80')}>
        <Turnstile
          siteKey={siteKey}
          onVerify={(value) => {
            setFailed(false)
            onVerify(value)
          }}
          onExpire={() => {
            setFailed(true)
            onVerify('')
            onExpire?.()
          }}
        />
      </div>
    </div>
  )
}
