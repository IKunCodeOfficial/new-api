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
  const [amount, setAmount] = useState<number>(0)
  const [isSubmitting, setIsSubmitting] = useState(false)
  const selectedRows = table.getFilteredSelectedRowModel().rows
  const currencyLabel = getCurrencyLabel()

  // Reset the input each time the dialog opens.
  useEffect(() => {
    if (open) {
      setAmount(0)
    }
  }, [open])

  const handleConfirm = async () => {
    if (selectedRows.length === 0) {
      onOpenChange(false)
      return
    }
    setIsSubmitting(true)
    try {
      const ids = selectedRows.map((row) => (row.original as ApiKey).id)
      const dailyQuotaLimit = parseQuotaFromDollars(amount || 0)
      const result = await batchSetApiKeysDailyQuota(ids, dailyQuotaLimit)

      if (result.success) {
        const count = result.data ?? ids.length
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
          <Button onClick={handleConfirm} disabled={isSubmitting}>
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
          onChange={(e) => setAmount(parseFloat(e.target.value) || 0)}
        />
        <p className='text-muted-foreground text-xs'>
          {t(
            'Applies to all selected keys. Resets daily at midnight (00:00). 0 clears the daily limit.'
          )}
        </p>
      </div>
    </Dialog>
  )
}
