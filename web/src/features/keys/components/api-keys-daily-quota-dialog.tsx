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
import { type Table } from '@tanstack/react-table'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { getCurrencyLabel } from '@/lib/currency'
import { parseQuotaFromDollars } from '@/lib/format'
import { cn } from '@/lib/utils'

import { batchSetApiKeysDailyQuota } from '../api'
import { ERROR_MESSAGES } from '../constants'
import { type ApiKey } from '../types'
import { useApiKeys } from './api-keys-provider'

type ApiKeysDailyQuotaDialogProps<TData> = {
  open: boolean
  onOpenChange: (open: boolean) => void
  table: Table<TData>
}

export function ApiKeysDailyQuotaDialog<TData>({
  open,
  onOpenChange,
  table,
}: ApiKeysDailyQuotaDialogProps<TData>) {
  const { t } = useTranslation()
  const { triggerRefresh } = useApiKeys()
  // Empty (not 0) is the default so an untouched dialog can never be applied: 0 means "clear the
  // limit for every selected key", which would silently wipe existing caps if it were the default.
  const [amount, setAmount] = useState<number | ''>('')
  const [isSubmitting, setIsSubmitting] = useState(false)
  const selectedRows = table.getFilteredSelectedRowModel().rows
  const currencyLabel = getCurrencyLabel()
  const willClearLimit = amount === 0

  // Reset the input each time the dialog opens.
  useEffect(() => {
    if (open) {
      setAmount('')
    }
  }, [open])

  const handleConfirm = async () => {
    if (selectedRows.length === 0) {
      onOpenChange(false)
      return
    }
    if (amount === '') {
      return
    }
    setIsSubmitting(true)
    try {
      const ids = selectedRows.map((row) => (row.original as ApiKey).id)
      const dailyQuotaLimit = parseQuotaFromDollars(amount || 0)
      const result = await batchSetApiKeysDailyQuota(ids, dailyQuotaLimit)

      if (result.success) {
        // The backend returns the number of owned tokens actually updated. Treat an
        // absent/non-numeric count as 0 (not ids.length) so we never falsely report success
        // when nothing was updated — matches the classic UI's SetDailyQuotaModal.
        const count = typeof result.data === 'number' ? result.data : 0
        if (count === 0) {
          // Ownership filter matched none of the selected keys — do not report success.
          toast.error(t('No API keys were updated'))
          return
        }
        toast.success(
          t('Updated daily quota limit for {{count}} API key(s)', { count })
        )
        table.resetRowSelection()
        triggerRefresh()
        onOpenChange(false)
      } else {
        toast.error(result.message || t(ERROR_MESSAGES.UNEXPECTED))
      }
    } catch (_error) {
      toast.error(t(ERROR_MESSAGES.UNEXPECTED))
    } finally {
      setIsSubmitting(false)
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('Set daily quota limit for {{count}} API key(s)', {
        count: selectedRows.length,
      })}
      contentClassName='sm:max-w-md'
      contentHeight='auto'
      footer={
        <>
          <Button variant='outline' onClick={() => onOpenChange(false)}>
            {t('Cancel')}
          </Button>
          <Button
            onClick={handleConfirm}
            disabled={isSubmitting || amount === ''}
          >
            {t('Apply')}
          </Button>
        </>
      }
    >
      <div className='space-y-2'>
        <Label htmlFor='batch-daily-quota-amount'>
          {t('Daily Quota Limit ({{currency}})', { currency: currencyLabel })}
        </Label>
        <Input
          id='batch-daily-quota-amount'
          type='number'
          min={0}
          value={amount}
          placeholder={t('0 = unlimited')}
          onChange={(e) => {
            const raw = e.target.value
            setAmount(raw === '' ? '' : parseFloat(raw) || 0)
          }}
        />
        <p
          className={cn(
            'text-xs',
            willClearLimit
              ? 'text-destructive font-medium'
              : 'text-muted-foreground'
          )}
        >
          {t(
            'Applies to all selected keys. Resets daily at midnight (00:00). 0 clears the daily limit.'
          )}
        </p>
      </div>
    </Dialog>
  )
}
