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
import * as z from 'zod'

import type { ChannelAvailabilityNotificationTestResponse } from '../types'

const emailSchema = z.string().email()

export type AvailabilityRecipientInputState = {
  recipients: string[]
  validRecipients: string[]
  invalidRecipients: string[]
}

export type AvailabilityTestFeedback = {
  kind: 'success' | 'warning' | 'error'
  reason: 'sent' | 'partial' | 'all_failed' | 'business_error'
  succeeded: number
  failed: number
  message: string
}

export function parseAvailabilityRecipients(input: string): string[] {
  const recipients: string[] = []
  const seen = new Set<string>()

  for (const value of input.split(/[,;\n]/)) {
    const recipient = value.trim()
    if (!recipient) continue

    const identity = recipient.toLowerCase()
    if (seen.has(identity)) continue

    seen.add(identity)
    recipients.push(recipient)
  }

  return recipients
}

export function getAvailabilityRecipientInputState(
  input: string
): AvailabilityRecipientInputState {
  const recipients = parseAvailabilityRecipients(input)
  const validRecipients = recipients.filter(
    (recipient) => emailSchema.safeParse(recipient).success
  )
  return {
    recipients,
    validRecipients,
    invalidRecipients: recipients.filter(
      (recipient) => !emailSchema.safeParse(recipient).success
    ),
  }
}

export function normalizeAvailabilityRecipientOption(value: unknown): string[] {
  if (Array.isArray(value)) {
    return parseAvailabilityRecipients(
      value
        .filter((item): item is string => typeof item === 'string')
        .join('\n')
    )
  }

  if (typeof value !== 'string') return []

  const trimmed = value.trim()
  if (!trimmed) return []

  try {
    const parsed: unknown = JSON.parse(trimmed)
    if (Array.isArray(parsed)) {
      return normalizeAvailabilityRecipientOption(parsed)
    }
  } catch {
    // Legacy plain-text values are parsed below.
  }

  return parseAvailabilityRecipients(trimmed)
}

export function formatAvailabilityRecipientInput(value: unknown): string {
  return normalizeAvailabilityRecipientOption(value).join('\n')
}

export function normalizeAvailabilityNotificationFormValues(
  enabled: boolean,
  recipientInput: string
): { enabled: boolean; recipients: string[] } {
  return {
    enabled,
    recipients: normalizeAvailabilityRecipientOption(recipientInput),
  }
}

export function buildAvailabilityNotificationTestRequest(
  input: string
): { recipients: string[] } | null {
  const state = getAvailabilityRecipientInputState(input)
  if (state.recipients.length === 0 || state.invalidRecipients.length > 0) {
    return null
  }
  return { recipients: state.recipients }
}

export function classifyAvailabilityTestResponse(
  response: ChannelAvailabilityNotificationTestResponse | null | undefined
): AvailabilityTestFeedback {
  const data = response?.data
  if (
    !data ||
    !Number.isSafeInteger(data.succeeded) ||
    data.succeeded < 0 ||
    !Number.isSafeInteger(data.failed) ||
    data.failed < 0
  ) {
    return {
      kind: 'error',
      reason: 'business_error',
      succeeded: 0,
      failed: 0,
      message: response?.message ?? '',
    }
  }

  const { succeeded, failed } = data
  if (succeeded > 0 && failed > 0) {
    return {
      kind: 'warning',
      reason: 'partial',
      succeeded,
      failed,
      message: response.message,
    }
  }
  if (failed > 0) {
    return {
      kind: 'error',
      reason: 'all_failed',
      succeeded,
      failed,
      message: response.message,
    }
  }
  if (!response.success || succeeded === 0) {
    return {
      kind: 'error',
      reason: 'business_error',
      succeeded,
      failed,
      message: response.message,
    }
  }
  return {
    kind: 'success',
    reason: 'sent',
    succeeded,
    failed,
    message: response.message,
  }
}
