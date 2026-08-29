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
import { useNavigate } from '@tanstack/react-router'
import { ShieldAlert } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import { useAuthStore } from '@/stores/auth-store'

// Snooze state lives entirely in the browser: the reminder is a per-device
// convenience, so losing it (new browser, cleared storage) just shows the
// dialog once more. Keyed by user id so shared browsers don't leak the
// snooze across accounts.
const SNOOZE_DAYS = 7

function snoozeStorageKey(userId: number) {
  return `email-bind-reminder-snooze-until:${userId}`
}

function isSnoozed(userId: number): boolean {
  try {
    const raw = localStorage.getItem(snoozeStorageKey(userId))
    if (!raw) return false
    const until = Number(raw)
    return Number.isFinite(until) && Date.now() < until
  } catch {
    return false
  }
}

function snooze(userId: number) {
  try {
    localStorage.setItem(
      snoozeStorageKey(userId),
      String(Date.now() + SNOOZE_DAYS * 24 * 60 * 60 * 1000)
    )
  } catch {
    // Storage unavailable (private mode, blocked site data): the user will
    // simply be reminded again next time, which is acceptable.
  }
}

export function EmailBindReminder() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const user = useAuthStore((state) => state.auth.user)
  const [open, setOpen] = useState(false)
  const [dontRemind, setDontRemind] = useState(false)

  const unboundUserId = user && !user.email ? user.id : null

  useEffect(() => {
    if (unboundUserId == null) {
      setOpen(false)
      return
    }
    if (!isSnoozed(unboundUserId)) {
      setOpen(true)
    }
  }, [unboundUserId])

  if (unboundUserId == null) return null

  const dismiss = () => {
    if (dontRemind) {
      snooze(unboundUserId)
    }
    setOpen(false)
  }

  const goToBind = () => {
    dismiss()
    navigate({ to: '/profile' })
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        if (!nextOpen) dismiss()
      }}
      title={
        <span className='flex items-center gap-2'>
          <ShieldAlert className='h-5 w-5 text-amber-500' />
          {t('Account Security Reminder')}
        </span>
      }
      description={t(
        'You have not bound an email address yet. For your account security, please bind an email as soon as possible.'
      )}
      contentClassName='sm:max-w-md'
      contentHeight='auto'
      footer={
        <>
          <Button type='button' variant='outline' onClick={dismiss}>
            {t('Not Now')}
          </Button>
          <Button type='button' onClick={goToBind}>
            {t('Go to Bind Email')}
          </Button>
        </>
      }
    >
      <div className='flex items-center gap-2 py-2'>
        <Checkbox
          id='email-bind-reminder-snooze'
          checked={dontRemind}
          onCheckedChange={(checked) => setDontRemind(checked === true)}
        />
        <Label
          htmlFor='email-bind-reminder-snooze'
          className='text-muted-foreground cursor-pointer text-sm font-normal'
        >
          {t('Do not remind me again for 7 days')}
        </Label>
      </div>
    </Dialog>
  )
}
