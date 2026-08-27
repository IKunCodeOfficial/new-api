import { describe, expect, it } from 'vitest'

import { formatAuditTargetUser } from './format'

describe('formatAuditTargetUser', () => {
  it('formats the target username and ID from new quota audit metadata', () => {
    expect(
      formatAuditTargetUser({
        op: {
          action: 'user.quota_add',
          params: { target_user_id: 42, username: 'target-user' },
        },
      })
    ).toBe('target-user (ID: 42)')
  })

  it('keeps ID-only management logs readable', () => {
    expect(
      formatAuditTargetUser({
        op: {
          action: 'user.quota_subtract',
          params: { target_user_id: 42 },
        },
      })
    ).toBe('ID: 42')
  })

  it('uses the legacy user action ID without treating resource IDs as users', () => {
    expect(
      formatAuditTargetUser({
        op: {
          action: 'user.update',
          params: { id: 42, username: 'target-user' },
        },
      })
    ).toBe('target-user (ID: 42)')

    expect(
      formatAuditTargetUser({
        op: {
          action: 'channel.update',
          params: { id: 42 },
        },
      })
    ).toBeNull()
  })
})
