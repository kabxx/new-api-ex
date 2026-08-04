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

import {
  acquireSubmissionGuard,
  buildChangedOptionPayload,
  getBulkUpdateOutcome,
  releaseSubmissionGuard,
  ROUTING_RELIABILITY_OPTION_KEYS,
} from '../routing-reliability-submit.ts'

describe('routing reliability bulk submission', () => {
  test('submits first-time recipients and enabled state in one string map', () => {
    const baseline = {
      RetryTimes: 10,
      'monitor_setting.channel_availability_notify_enabled': false,
      'monitor_setting.channel_availability_notify_recipients': '[]',
    }
    const current = {
      RetryTimes: 10,
      'monitor_setting.channel_availability_notify_enabled': true,
      'monitor_setting.channel_availability_notify_recipients':
        '["First@example.com","second@example.com"]',
    }

    assert.deepEqual(buildChangedOptionPayload(current, baseline), {
      'monitor_setting.channel_availability_notify_enabled': 'true',
      'monitor_setting.channel_availability_notify_recipients':
        '["First@example.com","second@example.com"]',
    })
  })

  test('keeps a failed business response out of the committed path', () => {
    assert.deepEqual(
      getBulkUpdateOutcome({ success: false, message: 'invalid recipients' }),
      { success: false, message: 'invalid recipients' }
    )
    assert.deepEqual(getBulkUpdateOutcome({ success: true, message: '' }), {
      success: true,
    })
  })

  test('only submits the explicit routing reliability option whitelist', () => {
    const current = {
      RetryTimes: 20,
      unrelated_option: 'must-not-leak',
    }
    const baseline = { RetryTimes: 10 }

    assert.deepEqual(buildChangedOptionPayload(current, baseline), {
      RetryTimes: '20',
    })
    assert.equal(ROUTING_RELIABILITY_OPTION_KEYS.length, 31)
    for (const key of [
      'retry_setting.same_priority_strategy',
      'monitor_setting.auto_disable_strategy',
      'monitor_setting.auto_disable_window_minutes',
      'monitor_setting.auto_disable_window_failures',
      'monitor_setting.auto_disable_rate_sample_size',
      'monitor_setting.auto_disable_rate_min_samples',
      'monitor_setting.auto_disable_rate_threshold_percent',
    ] as const) {
      assert.equal(ROUTING_RELIABILITY_OPTION_KEYS.includes(key), true)
    }
    assert.equal(
      new Set(ROUTING_RELIABILITY_OPTION_KEYS).size,
      ROUTING_RELIABILITY_OPTION_KEYS.length
    )
  })

  test('synchronously rejects duplicate submissions until released', () => {
    const guard = { current: false }
    assert.equal(acquireSubmissionGuard(guard), true)
    assert.equal(acquireSubmissionGuard(guard), false)
    releaseSubmissionGuard(guard)
    assert.equal(acquireSubmissionGuard(guard), true)
  })
})
