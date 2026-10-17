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
export type StatusRange = '5m' | '1h' | '24h' | '7d' | '30d'

export type ModelStatusBucket = {
  rate: number
  ttft_ms: number
}

export type ModelStatusDetail = {
  ttft_p50: number
  ttft_p95: number
  ttft_p99: number
  latency_p50: number
  latency_p95: number
  latency_p99: number
  fail_share?: Record<string, number> | null
}

export type ModelStatusModel = {
  name: string
  vendor_icon: string
  availability: number | null
  current: number | null
  ttft_avg_ms: number
  latency_avg_ms: number
  tps: number
  buckets: (ModelStatusBucket | null)[]
  detail?: ModelStatusDetail | null
}

export type ModelStatusGroup = {
  key: string
  desc: string
  ratio: number
  availability: number | null
  models: ModelStatusModel[]
}

export type ModelStatusResponse = {
  range: StatusRange
  bucket_seconds: number
  window_start: number
  updated_at: number
  has_detail: boolean
  groups: ModelStatusGroup[]
}

export type ModelStatusEnvelope = {
  success: boolean
  message?: string
  data: ModelStatusResponse
}
