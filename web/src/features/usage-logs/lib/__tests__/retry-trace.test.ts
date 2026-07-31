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
  normalizeRetryTraceSummary,
  normalizeUseChannelSummary,
} from '../retry-trace'

describe('retry log summaries', () => {
  test('sanitizes malformed trace entries and preserves bounded counts', () => {
    const summary = normalizeRetryTraceSummary({
      retry_trace: [
        null,
        'invalid',
        {
          attempt: 1,
          channel_id: 7,
          channel_name: 'primary',
          decision: 'retry',
          upstream_key: 'must-not-leak',
        },
        { attempt: 1, channel_id: 7, outcome: 'failed' },
      ],
      retry_trace_total: 9,
      retry_trace_omitted: 7,
    })

    assert.equal(summary.entries.length, 2)
    assert.equal(summary.total, 9)
    assert.equal(summary.omitted, 7)
    assert.notEqual(
      summary.entries[0]?.renderKey,
      summary.entries[1]?.renderKey
    )
    assert.equal(JSON.stringify(summary).includes('must-not-leak'), false)
  })

  test('normalizes mixed channel IDs and rejects invalid summary fields', () => {
    const summary = normalizeUseChannelSummary({
      use_channel: [7, ' 8 ', '', null, Number.POSITIVE_INFINITY],
      use_channel_total: 5,
      use_channel_omitted: 3,
    })

    assert.deepEqual(summary.channels, [7, '8'])
    assert.equal(summary.total, 5)
    assert.equal(summary.omitted, 3)

    assert.deepEqual(normalizeRetryTraceSummary({ retry_trace: 'invalid' }), {
      entries: [],
      total: 0,
      omitted: 0,
    })

    const contradictory = normalizeRetryTraceSummary({
      retry_trace: [{ attempt: 1 }, { attempt: 2 }],
      retry_trace_total: 2,
      retry_trace_omitted: 5,
    })
    assert.equal(contradictory.total, 7)
    assert.equal(contradictory.omitted, 5)
  })
})
