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
import { parseQuotaFromDollars } from '@/lib/format'

/**
 * fork (ikun): paid amount of an admin recharge, in quota units.
 *
 * An empty input means the full price was paid, so the paid amount follows the
 * credited amount. Anything else is entered in the same display unit as the
 * credited amount and must stay a real payment: negatives are rejected by
 * {@link isPaidQuotaInvalid} instead of being silently made positive.
 */
export function parsePaidQuota(
  paidAmount: string,
  creditedQuota: number
): number {
  if (paidAmount.trim() === '') return creditedQuota
  return parseQuotaFromDollars(Number.parseFloat(paidAmount) || 0)
}

/**
 * fork (ikun): whether a paid amount violates `0 < paid_quota <= credited`.
 * The backend rule is the real guard; this keeps the dialog from sending a
 * request it already knows the server will reject.
 */
export function isPaidQuotaInvalid(
  creditedQuota: number,
  paidQuota: number
): boolean {
  return paidQuota <= 0 || paidQuota > creditedQuota
}
