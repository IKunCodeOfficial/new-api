import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { formatAuditTargetUser } from './format'

describe('formatAuditTargetUser', () => {
  test('formats the target username and ID from new quota audit metadata', () => {
    assert.equal(
      formatAuditTargetUser({
        op: {
          action: 'user.quota_add',
          params: { target_user_id: 42, username: 'target-user' },
        },
      }),
      'target-user (ID: 42)'
    )
  })

  test('keeps ID-only management logs readable', () => {
    assert.equal(
      formatAuditTargetUser({
        op: {
          action: 'user.quota_subtract',
          params: { target_user_id: 42 },
        },
      }),
      'ID: 42'
    )
  })

  test('uses the legacy user action ID without treating resource IDs as users', () => {
    assert.equal(
      formatAuditTargetUser({
        op: {
          action: 'user.update',
          params: { id: 42, username: 'target-user' },
        },
      }),
      'target-user (ID: 42)'
    )

    assert.equal(
      formatAuditTargetUser({
        op: {
          action: 'channel.update',
          params: { id: 42 },
        },
      }),
      null
    )
  })
})
