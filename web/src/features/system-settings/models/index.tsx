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
import { SettingsPage } from '../components/settings-page'
import type { ModelSettings } from '../types'
import { DEFAULT_SAME_PRIORITY_STRATEGY } from './retry-setting-validation'
import { DEFAULT_AUTO_DISABLE_POLICY } from './routing-reliability-strategy'
import {
  MODELS_DEFAULT_SECTION,
  getModelsSectionContent,
  getModelsSectionMeta,
} from './section-registry.tsx'

const defaultModelSettings: ModelSettings = {
  'global.pass_through_request_enabled': false,
  'global.thinking_model_blacklist': '[]',
  'global.chat_completions_to_responses_policy': '{}',
  'general_setting.ping_interval_enabled': false,
  'general_setting.ping_interval_seconds': 60,
  'gemini.safety_settings': '',
  'gemini.version_settings': '',
  'gemini.supported_imagine_models': '',
  'gemini.thinking_adapter_enabled': false,
  'gemini.thinking_adapter_budget_tokens_percentage': 0.6,
  'gemini.function_call_thought_signature_enabled': true,
  'gemini.remove_function_response_id_enabled': true,
  'claude.model_headers_settings': '',
  'claude.default_max_tokens': '',
  'claude.thinking_adapter_enabled': true,
  'claude.thinking_adapter_budget_tokens_percentage': 0.8,
  'grok.violation_deduction_enabled': true,
  'grok.violation_deduction_amount': 0.05,
  ModelPrice: '',
  ModelRatio: '',
  CacheRatio: '',
  CreateCacheRatio: '',
  CompletionRatio: '',
  ImageRatio: '',
  AudioRatio: '',
  AudioCompletionRatio: '',
  ExposeRatioEnabled: false,
  'billing_setting.billing_mode': '{}',
  'billing_setting.billing_expr': '{}',
  'tool_price_setting.prices': '{}',
  TopupGroupRatio: '',
  GroupRatio: '',
  UserUsableGroups: '',
  GroupGroupRatio: '',
  AutoGroups: '',
  DefaultUseAutoGroup: false,
  'group_ratio_setting.group_special_usable_group': '{}',
  RetryTimes: 10,
  ChannelDisableThreshold: '',
  AutomaticDisableChannelEnabled: false,
  AutoDisableTolerance: 0,
  AutomaticEnableChannelEnabled: false,
  AutomaticDisableKeywords: '',
  AutomaticDisableStatusCodes: '401',
  AutomaticRetryStatusCodes:
    '100-199,300-399,401-407,409-499,500-503,505-523,525-599',
  'retry_setting.time_budget_seconds': 0,
  'retry_setting.delay_strategy': 'immediate',
  'retry_setting.fixed_delay_milliseconds': 0,
  'retry_setting.exponential_base_delay_milliseconds': 250,
  'retry_setting.exponential_max_delay_milliseconds': 10000,
  'retry_setting.jitter_percent': 20,
  'retry_setting.respect_retry_after': false,
  'retry_setting.channel_strategy': 'legacy',
  'retry_setting.same_priority_strategy': DEFAULT_SAME_PRIORITY_STRATEGY,
  'retry_setting.exhausted_action': 'stop',
  'retry_setting.try_other_keys': false,
  'monitor_setting.auto_test_channel_enabled': false,
  'monitor_setting.auto_test_channel_minutes': 10,
  'monitor_setting.channel_test_mode': 'scheduled_all',
  'monitor_setting.zero_token_as_failure': false,
  'monitor_setting.first_token_timeout_seconds': 0,
  'monitor_setting.auto_disable_strategy': DEFAULT_AUTO_DISABLE_POLICY.strategy,
  'monitor_setting.auto_disable_window_minutes':
    DEFAULT_AUTO_DISABLE_POLICY.windowMinutes,
  'monitor_setting.auto_disable_window_failures':
    DEFAULT_AUTO_DISABLE_POLICY.windowFailures,
  'monitor_setting.auto_disable_rate_sample_size':
    DEFAULT_AUTO_DISABLE_POLICY.rateSampleSize,
  'monitor_setting.auto_disable_rate_min_samples':
    DEFAULT_AUTO_DISABLE_POLICY.rateMinSamples,
  'monitor_setting.auto_disable_rate_threshold_percent':
    DEFAULT_AUTO_DISABLE_POLICY.rateThresholdPercent,
  'monitor_setting.channel_availability_notify_enabled': false,
  'monitor_setting.channel_availability_notify_recipients': [],
  'channel_affinity_setting.enabled': false,
  'channel_affinity_setting.switch_on_success': true,
  'channel_affinity_setting.keep_on_channel_disabled': false,
  'channel_affinity_setting.max_entries': 100000,
  'channel_affinity_setting.default_ttl_seconds': 3600,
  'channel_affinity_setting.rules': '[]',
  'model_deployment.ionet.api_key': '',
  'model_deployment.ionet.enabled': false,
}

export function ModelSettings() {
  return (
    <SettingsPage
      routePath='/_authenticated/system-settings/models/$section'
      defaultSettings={defaultModelSettings}
      defaultSection={MODELS_DEFAULT_SECTION}
      getSectionContent={getModelsSectionContent}
      getSectionMeta={getModelsSectionMeta}
    />
  )
}
