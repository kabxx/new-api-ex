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

export const autoDisableStrategies = [
  'consecutive',
  'window',
  'failure_rate',
] as const
export type AutoDisableStrategy = (typeof autoDisableStrategies)[number]

export const DEFAULT_AUTO_DISABLE_POLICY = {
  strategy: 'consecutive' as AutoDisableStrategy,
  windowMinutes: 10,
  windowFailures: 5,
  rateSampleSize: 20,
  rateMinSamples: 10,
  rateThresholdPercent: 60,
}

export const MAX_AUTO_DISABLE_OBSERVATIONS = 10000

type TranslateValidationMessage = (key: string) => string

function createPositiveSafeIntegerSchema(
  translate: TranslateValidationMessage
) {
  const message = translate('Enter a positive safe integer')
  return z.coerce
    .number({ error: message })
    .refine(
      (value) =>
        Number.isFinite(value) &&
        Number.isSafeInteger(value) &&
        value >= 1 &&
        value <= MAX_AUTO_DISABLE_OBSERVATIONS,
      message
    )
}

function createFailureRateThresholdSchema(
  translate: TranslateValidationMessage
) {
  const message = translate(
    'Enter a finite percentage greater than 0 and at most 100'
  )
  return z.coerce
    .number({ error: message })
    .refine(
      (value) => Number.isFinite(value) && value > 0 && value <= 100,
      message
    )
}

export function createAutoDisableStrategySchema(
  translate: TranslateValidationMessage
) {
  return z
    .object({
      auto_disable_strategy: z.enum(autoDisableStrategies),
      auto_disable_window_minutes: createPositiveSafeIntegerSchema(translate),
      auto_disable_window_failures: createPositiveSafeIntegerSchema(translate),
      auto_disable_rate_sample_size: createPositiveSafeIntegerSchema(translate),
      auto_disable_rate_min_samples: createPositiveSafeIntegerSchema(translate),
      auto_disable_rate_threshold_percent:
        createFailureRateThresholdSchema(translate),
    })
    .superRefine((values, ctx) => {
      if (
        values.auto_disable_rate_min_samples >
        values.auto_disable_rate_sample_size
      ) {
        ctx.addIssue({
          code: 'custom',
          path: ['auto_disable_rate_min_samples'],
          message: translate(
            'Minimum samples cannot exceed the recent sample size'
          ),
        })
      }
    })
}

export function normalizeAutoDisableStrategy(
  value?: string
): AutoDisableStrategy {
  if (value === 'window' || value === 'failure_rate') return value
  return 'consecutive'
}
