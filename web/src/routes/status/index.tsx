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
import { createFileRoute, redirect } from '@tanstack/react-router'
import z from 'zod'

import { ModelStatus } from '@/features/model-status'
import { getModuleAccessForGuard } from '@/lib/nav-modules'
import { useAuthStore } from '@/stores/auth-store'

const statusSearchSchema = z.object({
  range: z.enum(['5m', '1h', '24h', '7d', '30d']).optional().catch(undefined),
  group: z.string().optional(),
  view: z.enum(['list', 'matrix']).optional().catch(undefined),
  q: z.string().optional(),
})

export const Route = createFileRoute('/status/')({
  validateSearch: statusSearchSchema,
  beforeLoad: async ({ context, location }) => {
    const access = await getModuleAccessForGuard(context.queryClient, 'status')
    if (!access.enabled) {
      throw redirect({ to: '/' })
    }
    if (access.requireAuth) {
      const { auth } = useAuthStore.getState()
      if (!auth.user) {
        throw redirect({
          to: '/sign-in',
          search: { redirect: location.href },
        })
      }
    }
  },
  component: ModelStatus,
})
