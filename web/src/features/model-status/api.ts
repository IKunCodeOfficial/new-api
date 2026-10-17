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
import { useQuery } from '@tanstack/react-query'

import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import { REFRESH_MS } from './constants'
import type {
  ModelStatusEnvelope,
  ModelStatusResponse,
  StatusRange,
} from './types'

export async function getModelStatus(
  range: StatusRange
): Promise<ModelStatusResponse> {
  const res = await api.get('/api/model_status', { params: { range } })
  return requireServerSuccess<ModelStatusEnvelope>(res.data).data
}

export function useModelStatus(range: StatusRange) {
  return useQuery({
    queryKey: ['model-status', range],
    queryFn: () => getModelStatus(range),
    refetchInterval: REFRESH_MS[range],
  })
}
