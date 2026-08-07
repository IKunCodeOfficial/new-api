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
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { routingReliabilitySchema } from '../routing-reliability-schema'

const validSettings = {
  RetryTimes: 0,
  ChannelDisableThreshold: '',
  AutomaticDisableChannelEnabled: true,
  AutomaticDisableConsecutiveThreshold: 1,
  AutomaticEnableChannelEnabled: true,
  AutomaticDisableKeywords: '',
  AutomaticDisableStatusCodes: '401',
  AutomaticRetryStatusCodes: '500-599',
  monitor_setting: {
    auto_test_channel_enabled: true,
    auto_test_channel_minutes: 10,
    channel_test_mode: 'scheduled_all',
  },
}

describe('routing reliability validation', () => {
  test('accepts positive integer auto-disable thresholds', () => {
    for (const threshold of [1, 3, 100]) {
      const result = routingReliabilitySchema.safeParse({
        ...validSettings,
        AutomaticDisableConsecutiveThreshold: threshold,
      })
      assert.equal(result.success, true)
    }
  })

  test('rejects non-positive and fractional auto-disable thresholds', () => {
    for (const threshold of [0, -1, 1.5]) {
      const result = routingReliabilitySchema.safeParse({
        ...validSettings,
        AutomaticDisableConsecutiveThreshold: threshold,
      })
      assert.equal(result.success, false)
    }
  })
})
