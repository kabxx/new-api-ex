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
  buildAvailabilityNotificationTestRequest,
  classifyAvailabilityTestResponse,
  formatAvailabilityRecipientInput,
  getAvailabilityRecipientInputState,
  normalizeAvailabilityNotificationFormValues,
  normalizeAvailabilityRecipientOption,
  parseAvailabilityRecipients,
} from '../channel-availability-notification.ts'

describe('channel availability notification recipients', () => {
  test('parses supported separators and deduplicates case-insensitively', () => {
    assert.deepEqual(
      parseAvailabilityRecipients(
        'Alpha@example.com, beta@example.com;alpha@EXAMPLE.com\ngamma@example.com'
      ),
      ['Alpha@example.com', 'beta@example.com', 'gamma@example.com']
    )
  })

  test('reports invalid recipients and blocks the test payload', () => {
    const state = getAvailabilityRecipientInputState(
      'valid@example.com\nnot-an-email'
    )
    assert.deepEqual(state.invalidRecipients, ['not-an-email'])
    assert.deepEqual(state.validRecipients, ['valid@example.com'])
    assert.equal(
      buildAvailabilityNotificationTestRequest(
        'valid@example.com\nnot-an-email'
      ),
      null
    )
    assert.equal(buildAvailabilityNotificationTestRequest(''), null)
  })

  test('classifies partial and failed test deliveries without treating them as success', () => {
    assert.deepEqual(
      classifyAvailabilityTestResponse({
        success: false,
        message: 'one delivery failed',
        data: {
          succeeded: 1,
          failed: 1,
          results: [
            { recipient: 'First@example.com', success: true },
            {
              recipient: 'second@example.com',
              success: false,
              error: 'email delivery failed',
            },
          ],
        },
      }),
      {
        kind: 'warning',
        reason: 'partial',
        succeeded: 1,
        failed: 1,
        message: 'one delivery failed',
      }
    )
    assert.equal(
      classifyAvailabilityTestResponse({
        success: false,
        message: 'SMTP is not configured',
        data: { succeeded: 0, failed: 1, results: [] },
      }).kind,
      'error'
    )
  })

  test('handles HTTP 200 business errors and malformed delivery counts safely', () => {
    assert.deepEqual(
      classifyAvailabilityTestResponse({
        success: false,
        message: 'SMTP is not configured',
      }),
      {
        kind: 'error',
        reason: 'business_error',
        succeeded: 0,
        failed: 0,
        message: 'SMTP is not configured',
      }
    )
    assert.equal(
      classifyAvailabilityTestResponse({
        success: true,
        message: '',
        data: {
          succeeded: Number.NaN,
          failed: 0,
          results: [],
        },
      }).reason,
      'business_error'
    )
  })

  test('distinguishes all failed deliveries from business errors', () => {
    const feedback = classifyAvailabilityTestResponse({
      success: false,
      message: '测试邮件发送完成：成功 0，失败 2',
      data: { succeeded: 0, failed: 2, results: [] },
    })
    assert.equal(feedback.kind, 'error')
    assert.equal(feedback.reason, 'all_failed')
    assert.equal(feedback.failed, 2)
  })

  test('builds a normalized test request for valid recipients', () => {
    assert.deepEqual(
      buildAvailabilityNotificationTestRequest(
        'first@example.com;SECOND@example.com;second@EXAMPLE.com'
      ),
      { recipients: ['first@example.com', 'SECOND@example.com'] }
    )
  })

  test('retains normalized recipients while notifications are disabled', () => {
    assert.deepEqual(
      normalizeAvailabilityNotificationFormValues(
        false,
        'Saved@example.com;saved@EXAMPLE.com;other@example.com'
      ),
      {
        enabled: false,
        recipients: ['Saved@example.com', 'other@example.com'],
      }
    )
  })

  test('normalizes arrays, JSON option strings, legacy text, and missing values', () => {
    assert.deepEqual(
      normalizeAvailabilityRecipientOption([
        'one@example.com',
        'TWO@example.com',
        'two@EXAMPLE.com',
      ]),
      ['one@example.com', 'TWO@example.com']
    )
    assert.deepEqual(
      normalizeAvailabilityRecipientOption(
        '["one@example.com","two@example.com"]'
      ),
      ['one@example.com', 'two@example.com']
    )
    assert.deepEqual(
      normalizeAvailabilityRecipientOption('one@example.com;two@example.com'),
      ['one@example.com', 'two@example.com']
    )
    assert.deepEqual(normalizeAvailabilityRecipientOption(undefined), [])
    assert.equal(
      formatAvailabilityRecipientInput(['one@example.com', 'two@example.com']),
      'one@example.com\ntwo@example.com'
    )
  })
})
