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

import { cn } from '@/lib/utils'

import { STATUS_STYLES } from '../constants'

export function StatusLegend() {
  const { t } = useTranslation()
  const items = [
    { level: 'success', label: t('Healthy ≥ 95%') },
    { level: 'warning', label: t('Degraded 60% – 95%') },
    { level: 'danger', label: t('Failing < 60%') },
    { level: 'neutral', label: t('No requests') },
  ] as const

  return (
    <footer className='text-muted-foreground mt-5 flex flex-wrap items-center justify-between gap-3 px-1 text-xs'>
      <div className='flex flex-wrap items-center gap-4'>
        {items.map((item) => (
          <span key={item.level} className='inline-flex items-center gap-1.5'>
            <span
              aria-hidden='true'
              className={cn(
                'size-2.5 rounded-[2px]',
                STATUS_STYLES[item.level].bar
              )}
            />
            {item.label}
          </span>
        ))}
      </div>
      <span className='text-muted-foreground/70'>
        {t(
          'Status labels use the most recent buckets · TTFT counts streaming requests only'
        )}
      </span>
    </footer>
  )
}
