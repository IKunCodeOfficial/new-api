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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import i18next from 'i18next'
import { toast } from 'sonner'

import { SYSTEM_BANNER_QUERY_KEY } from '@/components/system-banner/lib'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import { updateSystemBanner } from '../api'
import type { UpdateSystemBannerRequest } from '../types'

/** Save the whole system banner and refresh everything that shows it. */
export function useUpdateSystemBanner() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (request: UpdateSystemBannerRequest) =>
      requireServerSuccess(await updateSystemBanner(request)),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['system-options'] })
      queryClient.invalidateQueries({ queryKey: ['status'] })
      try {
        window.localStorage.removeItem('status')
      } catch {
        /* empty */
      }
      queryClient.invalidateQueries({ queryKey: SYSTEM_BANNER_QUERY_KEY })
      toast.success(i18next.t('Setting updated successfully'))
    },
    onError: (error: Error) => {
      handleServerError(error, i18next.t('Failed to update setting'))
    },
  })
}
