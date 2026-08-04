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

export const retryDelayStrategies = [
  'immediate',
  'fixed',
  'exponential',
] as const
export type RetryDelayStrategy = (typeof retryDelayStrategies)[number]

export const retryChannelStrategies = ['legacy', 'same_priority'] as const
export type RetryChannelStrategy = (typeof retryChannelStrategies)[number]

export const retryExhaustedActions = ['stop', 'cycle'] as const
export type RetryExhaustedAction = (typeof retryExhaustedActions)[number]

type TranslateValidationMessage = (key: string) => string

export function createSafeNonNegativeIntegerSchema(
  translate: TranslateValidationMessage
) {
  const message = translate('Enter a non-negative safe integer')
  return z.coerce
    .number({ error: message })
    .refine(
      (value) =>
        Number.isFinite(value) && Number.isSafeInteger(value) && value >= 0,
      message
    )
}

export function createRetryTimesSchema(translate: TranslateValidationMessage) {
  const message = translate('Enter -1 or a non-negative safe integer')
  return z.coerce
    .number({ error: message })
    .refine(
      (value) =>
        Number.isFinite(value) && Number.isSafeInteger(value) && value >= -1,
      message
    )
}

export function createFiniteNonNegativeNumberSchema(
  translate: TranslateValidationMessage
) {
  const message = translate('Enter a non-negative finite number')
  return z.coerce
    .number({ error: message })
    .refine((value) => Number.isFinite(value) && value >= 0, message)
}

export function createRetrySettingSchema(
  translate: TranslateValidationMessage
) {
  const safeInteger = createSafeNonNegativeIntegerSchema(translate)
  return z.object({
    time_budget_seconds: safeInteger,
    delay_strategy: z.enum(retryDelayStrategies),
    fixed_delay_milliseconds: safeInteger,
    exponential_base_delay_milliseconds: safeInteger,
    exponential_max_delay_milliseconds: safeInteger,
    jitter_percent: createFiniteNonNegativeNumberSchema(translate),
    respect_retry_after: z.boolean(),
    channel_strategy: z.enum(retryChannelStrategies),
    exhausted_action: z.enum(retryExhaustedActions),
    try_other_keys: z.boolean(),
  })
}
