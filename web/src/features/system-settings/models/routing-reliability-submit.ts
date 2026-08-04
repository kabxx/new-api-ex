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

export type BulkUpdateOutcome =
  | { success: true }
  | { success: false; message: string }

export const ROUTING_RELIABILITY_OPTION_KEYS = [
  'RetryTimes',
  'ChannelDisableThreshold',
  'AutomaticDisableChannelEnabled',
  'AutoDisableTolerance',
  'AutomaticEnableChannelEnabled',
  'AutomaticDisableKeywords',
  'AutomaticDisableStatusCodes',
  'AutomaticRetryStatusCodes',
  'retry_setting.time_budget_seconds',
  'retry_setting.delay_strategy',
  'retry_setting.fixed_delay_milliseconds',
  'retry_setting.exponential_base_delay_milliseconds',
  'retry_setting.exponential_max_delay_milliseconds',
  'retry_setting.jitter_percent',
  'retry_setting.respect_retry_after',
  'retry_setting.channel_strategy',
  'retry_setting.same_priority_strategy',
  'retry_setting.exhausted_action',
  'retry_setting.try_other_keys',
  'monitor_setting.auto_test_channel_enabled',
  'monitor_setting.auto_test_channel_minutes',
  'monitor_setting.channel_test_mode',
  'monitor_setting.zero_token_as_failure',
  'monitor_setting.auto_disable_strategy',
  'monitor_setting.auto_disable_window_minutes',
  'monitor_setting.auto_disable_window_failures',
  'monitor_setting.auto_disable_rate_sample_size',
  'monitor_setting.auto_disable_rate_min_samples',
  'monitor_setting.auto_disable_rate_threshold_percent',
  'monitor_setting.channel_availability_notify_enabled',
  'monitor_setting.channel_availability_notify_recipients',
] as const

export type RoutingReliabilityOptionKey =
  (typeof ROUTING_RELIABILITY_OPTION_KEYS)[number]

type RoutingReliabilityOptionValues = Partial<
  Record<RoutingReliabilityOptionKey, unknown>
>

export type SubmissionGuard = { current: boolean }

export function acquireSubmissionGuard(guard: SubmissionGuard): boolean {
  if (guard.current) return false
  guard.current = true
  return true
}

export function releaseSubmissionGuard(guard: SubmissionGuard): void {
  guard.current = false
}

export function buildChangedOptionPayload(
  current: RoutingReliabilityOptionValues,
  baseline: RoutingReliabilityOptionValues
): Record<string, string> {
  const options: Record<string, string> = {}
  for (const key of ROUTING_RELIABILITY_OPTION_KEYS) {
    const value = current[key]
    if (value === undefined || value === baseline[key]) continue
    options[key] = String(value)
  }
  return options
}

export function getBulkUpdateOutcome(response: {
  success: boolean
  message: string
}): BulkUpdateOutcome {
  if (response.success) return { success: true }
  return { success: false, message: response.message }
}
