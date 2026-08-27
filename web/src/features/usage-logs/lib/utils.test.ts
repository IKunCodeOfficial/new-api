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
import { describe, expect, it } from 'vitest'

import { buildApiParams } from './utils'

describe('usage log target user filters', () => {
  it('sends a positive target user ID for an admin view', () => {
    const params = buildApiParams({
      page: 1,
      pageSize: 20,
      searchParams: { type: ['3'], targetUserId: '38703' },
      isAdmin: true,
    })

    expect(params.target_user_id).toBe(38703)
  })

  it('does not send a target user ID for a self view', () => {
    const params = buildApiParams({
      page: 1,
      pageSize: 20,
      searchParams: { type: ['3'], targetUserId: '38703' },
      isAdmin: false,
    })

    expect(params.target_user_id).toBeUndefined()
  })

  it('does not send invalid target user IDs', () => {
    for (const targetUserId of ['0', '-1', '1.5', 'not-a-user']) {
      const params = buildApiParams({
        page: 1,
        pageSize: 20,
        searchParams: { type: ['3'], targetUserId },
        isAdmin: true,
      })

      expect(params.target_user_id).toBeUndefined()
    }
  })

  it('does not send a target user ID for non-management log types', () => {
    for (const type of ['2', '0']) {
      const params = buildApiParams({
        page: 1,
        pageSize: 20,
        searchParams: { type: [type], targetUserId: '38703' },
        isAdmin: true,
      })

      expect(params.target_user_id).toBeUndefined()
    }
  })
})
