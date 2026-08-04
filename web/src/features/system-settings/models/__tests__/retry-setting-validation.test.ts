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
  createFiniteNonNegativeNumberSchema,
  createRetrySettingSchema,
  createRetryTimesSchema,
  createSafeNonNegativeIntegerSchema,
  SAME_PRIORITY_STRATEGY_DESCRIPTION_KEYS,
  samePriorityStrategies,
} from '../retry-setting-validation.ts'

const translate = (key: string) => `translated:${key}`

describe('retry setting validation', () => {
  test('uses a localized error for every invalid integer shape', () => {
    const schema = createSafeNonNegativeIntegerSchema(translate)

    for (const value of [-1, 1.5, Number.NaN, Number.POSITIVE_INFINITY]) {
      const result = schema.safeParse(value)
      assert.equal(result.success, false)
      if (!result.success) {
        assert.equal(
          result.error.issues[0]?.message,
          'translated:Enter a non-negative safe integer'
        )
      }
    }

    assert.equal(schema.safeParse(Number.MAX_SAFE_INTEGER).success, true)
    assert.equal(schema.safeParse(Number.MAX_SAFE_INTEGER + 1).success, false)
  })

  test('keeps inactive delay fields instead of discarding their values', () => {
    const result = createRetrySettingSchema(translate).safeParse({
      time_budget_seconds: 0,
      delay_strategy: 'immediate',
      fixed_delay_milliseconds: 750,
      exponential_base_delay_milliseconds: 125,
      exponential_max_delay_milliseconds: 5000,
      jitter_percent: 12.5,
      respect_retry_after: true,
      channel_strategy: 'legacy',
      same_priority_strategy: 'latency_first',
      exhausted_action: 'stop',
      try_other_keys: true,
    })

    assert.equal(result.success, true)
    if (result.success) {
      assert.equal(result.data.fixed_delay_milliseconds, 750)
      assert.equal(result.data.exponential_base_delay_milliseconds, 125)
      assert.equal(result.data.exponential_max_delay_milliseconds, 5000)
      assert.equal(result.data.jitter_percent, 12.5)
      assert.equal(result.data.same_priority_strategy, 'latency_first')
    }
  })

  test('supports exactly the three same-priority strategies and defines TTFT precisely', () => {
    assert.deepEqual(samePriorityStrategies, [
      'weighted_random',
      'stability_first',
      'latency_first',
    ])
    assert.match(SAME_PRIORITY_STRATEGY_DESCRIPTION_KEYS.latency_first, /TTFT/)
    assert.match(
      SAME_PRIORITY_STRATEGY_DESCRIPTION_KEYS.latency_first,
      /not total response time/
    )
  })

  test('accepts -1 as unlimited retry times and rejects other negative values', () => {
    const schema = createRetryTimesSchema(translate)

    assert.equal(schema.safeParse(-1).success, true)
    assert.equal(schema.safeParse(0).success, true)
    assert.equal(schema.safeParse(-2).success, false)
    assert.equal(schema.safeParse(Number.MAX_SAFE_INTEGER + 1).success, false)
  })

  test('accepts decimal jitter but rejects non-finite and negative values', () => {
    const schema = createFiniteNonNegativeNumberSchema(translate)

    assert.equal(schema.safeParse(12.5).success, true)
    for (const value of [-0.1, Number.NaN, Number.POSITIVE_INFINITY]) {
      const result = schema.safeParse(value)
      assert.equal(result.success, false)
      if (!result.success) {
        assert.equal(
          result.error.issues[0]?.message,
          'translated:Enter a non-negative finite number'
        )
      }
    }
  })
})
