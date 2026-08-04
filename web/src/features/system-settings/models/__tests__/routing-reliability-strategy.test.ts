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
  autoDisableStrategies,
  createAutoDisableStrategySchema,
  DEFAULT_AUTO_DISABLE_POLICY,
  MAX_AUTO_DISABLE_OBSERVATIONS,
  normalizeAutoDisableStrategy,
} from '../routing-reliability-strategy.ts'

const translate = (key: string) => `translated:${key}`

const validPolicy = {
  auto_disable_strategy: 'consecutive' as const,
  auto_disable_window_minutes: 10,
  auto_disable_window_failures: 5,
  auto_disable_rate_sample_size: 20,
  auto_disable_rate_min_samples: 10,
  auto_disable_rate_threshold_percent: 60,
}

describe('auto-disable strategy settings', () => {
  test('keeps the three strategies and backward-compatible defaults', () => {
    assert.deepEqual(autoDisableStrategies, [
      'consecutive',
      'window',
      'failure_rate',
    ])
    assert.deepEqual(DEFAULT_AUTO_DISABLE_POLICY, {
      strategy: 'consecutive',
      windowMinutes: 10,
      windowFailures: 5,
      rateSampleSize: 20,
      rateMinSamples: 10,
      rateThresholdPercent: 60,
    })
    assert.equal(normalizeAutoDisableStrategy(undefined), 'consecutive')
    assert.equal(normalizeAutoDisableStrategy('unknown'), 'consecutive')
    assert.equal(normalizeAutoDisableStrategy('window'), 'window')
    assert.equal(normalizeAutoDisableStrategy('failure_rate'), 'failure_rate')
  })

  test('retains inactive child settings when the selected strategy changes', () => {
    const result = createAutoDisableStrategySchema(translate).safeParse({
      ...validPolicy,
      auto_disable_strategy: 'window',
    })

    assert.equal(result.success, true)
    if (result.success) {
      assert.equal(result.data.auto_disable_rate_sample_size, 20)
      assert.equal(result.data.auto_disable_rate_threshold_percent, 60)
    }
  })

  test('rejects unsafe or non-positive window and sample counts', () => {
    const schema = createAutoDisableStrategySchema(translate)
    for (const field of [
      'auto_disable_window_minutes',
      'auto_disable_window_failures',
      'auto_disable_rate_sample_size',
      'auto_disable_rate_min_samples',
    ] as const) {
      for (const value of [0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1]) {
        assert.equal(
          schema.safeParse({ ...validPolicy, [field]: value }).success,
          false
        )
      }
      assert.equal(
        schema.safeParse({
          ...validPolicy,
          [field]: MAX_AUTO_DISABLE_OBSERVATIONS + 1,
        }).success,
        false
      )
    }
  })

  test('enforces percentage and minimum-sample boundaries', () => {
    const schema = createAutoDisableStrategySchema(translate)

    for (const value of [0, 100.1, Number.NaN, Number.POSITIVE_INFINITY]) {
      assert.equal(
        schema.safeParse({
          ...validPolicy,
          auto_disable_rate_threshold_percent: value,
        }).success,
        false
      )
    }
    const minExceedsWindow = schema.safeParse({
      ...validPolicy,
      auto_disable_rate_sample_size: 10,
      auto_disable_rate_min_samples: 11,
    })
    assert.equal(minExceedsWindow.success, false)
    if (!minExceedsWindow.success) {
      assert.deepEqual(minExceedsWindow.error.issues[0]?.path, [
        'auto_disable_rate_min_samples',
      ])
      assert.equal(
        minExceedsWindow.error.issues[0]?.message,
        'translated:Minimum samples cannot exceed the recent sample size'
      )
    }
  })
})
