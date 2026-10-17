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
import type { ModelStatusBucket } from '../types'

// Viewport anchor (top-centre of the hovered bar) plus the bucket it shows.
export type BucketTip = {
  x: number
  y: number
  index: number
  bucket: ModelStatusBucket | null
}

// Hover state for the shared bar tooltip. It lives outside React state so a
// mouse move re-renders only the tooltip, never the page or the model rows.
let current: BucketTip | null = null
const listeners = new Set<() => void>()

function emit(next: BucketTip | null) {
  if (current === next) return
  current = next
  for (const listener of listeners) listener()
}

export function showBucketTip(tip: BucketTip) {
  emit(tip)
}

export function hideBucketTip() {
  emit(null)
}

export function subscribeBucketTip(listener: () => void) {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

export function getBucketTip(): BucketTip | null {
  return current
}
