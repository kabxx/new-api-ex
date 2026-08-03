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
import { zodResolver } from '@hookform/resolvers/zod'
import {
  MailSend01Icon,
  InformationCircleIcon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import type { AxiosError } from 'axios'
import { useMemo, useRef } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import * as z from 'zod'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import { Spinner } from '@/components/ui/spinner'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { parseHttpStatusCodeRules } from '@/lib/http-status-code-rules'

import {
  sendChannelAvailabilityNotificationTest,
  updateSystemOptionsBulk,
} from '../api'
import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useResetForm } from '../hooks/use-reset-form'
import { safeNumberFieldProps } from '../utils/numeric-field'
import {
  buildAvailabilityNotificationTestRequest,
  classifyAvailabilityTestResponse,
  formatAvailabilityRecipientInput,
  getAvailabilityRecipientInputState,
  normalizeAvailabilityNotificationFormValues,
  normalizeAvailabilityRecipientOption,
} from './channel-availability-notification'
import {
  createRetrySettingSchema,
  createSafeNonNegativeIntegerSchema,
  type RetryChannelStrategy,
  type RetryDelayStrategy,
  type RetryExhaustedAction,
} from './retry-setting-validation'
import {
  acquireSubmissionGuard,
  buildChangedOptionPayload,
  getBulkUpdateOutcome,
  releaseSubmissionGuard,
} from './routing-reliability-submit'

const numericString = z.string().refine((value) => {
  const trimmed = value.trim()
  if (!trimmed) return true
  return !Number.isNaN(Number(trimmed)) && Number(trimmed) >= 0
}, 'Enter a non-negative number or leave empty')

const channelTestModes = ['scheduled_all', 'passive_recovery'] as const
type ChannelTestMode = (typeof channelTestModes)[number]

const createRoutingReliabilitySchema = (
  translateValidationMessage: (key: string) => string
) => {
  const invalidRecipientMessage = translateValidationMessage(
    'Enter valid email addresses separated by commas, semicolons, or new lines'
  )
  const recipientInputSchema = z.string().superRefine((value, ctx) => {
    if (
      getAvailabilityRecipientInputState(value).invalidRecipients.length > 0
    ) {
      ctx.addIssue({
        code: 'custom',
        message: invalidRecipientMessage,
      })
    }
  })

  return z
    .object({
      RetryTimes: createSafeNonNegativeIntegerSchema(
        translateValidationMessage
      ),
      ChannelDisableThreshold: numericString,
      AutomaticDisableChannelEnabled: z.boolean(),
      AutoDisableTolerance: z.coerce.number().int().min(0).max(999),
      AutomaticEnableChannelEnabled: z.boolean(),
      AutomaticDisableKeywords: z.string(),
      AutomaticDisableStatusCodes: z.string(),
      AutomaticRetryStatusCodes: z.string(),
      retry_setting: createRetrySettingSchema(translateValidationMessage),
      monitor_setting: z
        .object({
          auto_test_channel_enabled: z.boolean(),
          auto_test_channel_minutes: z.coerce
            .number()
            .int()
            .min(1, 'Interval must be at least 1 minute'),
          channel_test_mode: z.enum(channelTestModes),
          zero_token_as_failure: z.boolean(),
          channel_availability_notify_enabled: z.boolean(),
          channel_availability_notify_recipients: recipientInputSchema,
        })
        .superRefine((values, ctx) => {
          if (
            values.channel_availability_notify_enabled &&
            getAvailabilityRecipientInputState(
              values.channel_availability_notify_recipients
            ).recipients.length === 0
          ) {
            ctx.addIssue({
              code: 'custom',
              path: ['channel_availability_notify_recipients'],
              message: translateValidationMessage(
                'Add at least one notification recipient'
              ),
            })
          }
        }),
    })
    .superRefine((values, ctx) => {
      const disableParsed = parseHttpStatusCodeRules(
        values.AutomaticDisableStatusCodes
      )
      if (!disableParsed.ok) {
        ctx.addIssue({
          code: 'custom',
          path: ['AutomaticDisableStatusCodes'],
          message: `Invalid status code rules: ${disableParsed.invalidTokens.join(
            ', '
          )}`,
        })
      }

      const retryParsed = parseHttpStatusCodeRules(
        values.AutomaticRetryStatusCodes
      )
      if (!retryParsed.ok) {
        ctx.addIssue({
          code: 'custom',
          path: ['AutomaticRetryStatusCodes'],
          message: `Invalid status code rules: ${retryParsed.invalidTokens.join(
            ', '
          )}`,
        })
      }
    })
}

const routingReliabilitySchema = createRoutingReliabilitySchema((key) => key)

type RoutingReliabilityFormValues = z.output<typeof routingReliabilitySchema>
type RoutingReliabilityFormInput = z.input<typeof routingReliabilitySchema>

type RoutingReliabilitySectionProps = {
  defaultValues: {
    RetryTimes: number
    ChannelDisableThreshold: string
    AutomaticDisableChannelEnabled: boolean
    AutoDisableTolerance: number
    AutomaticEnableChannelEnabled: boolean
    AutomaticDisableKeywords: string
    AutomaticDisableStatusCodes: string
    AutomaticRetryStatusCodes: string
    'retry_setting.unlimited': boolean
    'retry_setting.time_budget_seconds': number
    'retry_setting.delay_strategy': RetryDelayStrategy
    'retry_setting.fixed_delay_milliseconds': number
    'retry_setting.exponential_base_delay_milliseconds': number
    'retry_setting.exponential_max_delay_milliseconds': number
    'retry_setting.jitter_percent': number
    'retry_setting.respect_retry_after': boolean
    'retry_setting.channel_strategy': RetryChannelStrategy
    'retry_setting.exhausted_action': RetryExhaustedAction
    'retry_setting.try_other_keys': boolean
    'retry_setting.unlimited_task_retries': boolean
    'monitor_setting.auto_test_channel_enabled': boolean
    'monitor_setting.auto_test_channel_minutes': number
    'monitor_setting.channel_test_mode': ChannelTestMode
    'monitor_setting.zero_token_as_failure': boolean
    'monitor_setting.channel_availability_notify_enabled': boolean
    'monitor_setting.channel_availability_notify_recipients': unknown
  }
}

function normalizeLineEndings(value: string) {
  return value.replaceAll('\r\n', '\n')
}

type NormalizedRoutingReliabilityValues = {
  RetryTimes: number
  ChannelDisableThreshold: string
  AutomaticDisableChannelEnabled: boolean
  AutoDisableTolerance: number
  AutomaticEnableChannelEnabled: boolean
  AutomaticDisableKeywords: string
  AutomaticDisableStatusCodes: string
  AutomaticRetryStatusCodes: string
  'retry_setting.unlimited': boolean
  'retry_setting.time_budget_seconds': number
  'retry_setting.delay_strategy': RetryDelayStrategy
  'retry_setting.fixed_delay_milliseconds': number
  'retry_setting.exponential_base_delay_milliseconds': number
  'retry_setting.exponential_max_delay_milliseconds': number
  'retry_setting.jitter_percent': number
  'retry_setting.respect_retry_after': boolean
  'retry_setting.channel_strategy': RetryChannelStrategy
  'retry_setting.exhausted_action': RetryExhaustedAction
  'retry_setting.try_other_keys': boolean
  'retry_setting.unlimited_task_retries': boolean
  'monitor_setting.auto_test_channel_enabled': boolean
  'monitor_setting.auto_test_channel_minutes': number
  'monitor_setting.channel_test_mode': ChannelTestMode
  'monitor_setting.zero_token_as_failure': boolean
  'monitor_setting.channel_availability_notify_enabled': boolean
  'monitor_setting.channel_availability_notify_recipients': string
}

function normalizeChannelTestMode(value?: string): ChannelTestMode {
  return value === 'passive_recovery' ? 'passive_recovery' : 'scheduled_all'
}

function normalizeRetryDelayStrategy(value?: string): RetryDelayStrategy {
  return value === 'fixed' || value === 'exponential' ? value : 'immediate'
}

function normalizeRetryChannelStrategy(value?: string): RetryChannelStrategy {
  return value === 'same_priority' ? 'same_priority' : 'legacy'
}

function normalizeRetryExhaustedAction(value?: string): RetryExhaustedAction {
  return value === 'cycle' ? 'cycle' : 'stop'
}

const buildFormDefaults = (
  defaults: RoutingReliabilitySectionProps['defaultValues']
): RoutingReliabilityFormInput => ({
  RetryTimes: defaults.RetryTimes ?? 10,
  ChannelDisableThreshold: defaults.ChannelDisableThreshold ?? '',
  AutomaticDisableChannelEnabled: defaults.AutomaticDisableChannelEnabled,
  AutoDisableTolerance: defaults.AutoDisableTolerance ?? 0,
  AutomaticEnableChannelEnabled: defaults.AutomaticEnableChannelEnabled,
  AutomaticDisableKeywords: normalizeLineEndings(
    defaults.AutomaticDisableKeywords ?? ''
  ),
  AutomaticDisableStatusCodes: defaults.AutomaticDisableStatusCodes ?? '',
  AutomaticRetryStatusCodes: defaults.AutomaticRetryStatusCodes ?? '',
  retry_setting: {
    unlimited: defaults['retry_setting.unlimited'] ?? false,
    time_budget_seconds: defaults['retry_setting.time_budget_seconds'] ?? 0,
    delay_strategy: normalizeRetryDelayStrategy(
      defaults['retry_setting.delay_strategy']
    ),
    fixed_delay_milliseconds:
      defaults['retry_setting.fixed_delay_milliseconds'] ?? 0,
    exponential_base_delay_milliseconds:
      defaults['retry_setting.exponential_base_delay_milliseconds'] ?? 250,
    exponential_max_delay_milliseconds:
      defaults['retry_setting.exponential_max_delay_milliseconds'] ?? 10000,
    jitter_percent: defaults['retry_setting.jitter_percent'] ?? 20,
    respect_retry_after: defaults['retry_setting.respect_retry_after'] ?? false,
    channel_strategy: normalizeRetryChannelStrategy(
      defaults['retry_setting.channel_strategy']
    ),
    exhausted_action: normalizeRetryExhaustedAction(
      defaults['retry_setting.exhausted_action']
    ),
    try_other_keys: defaults['retry_setting.try_other_keys'] ?? false,
    unlimited_task_retries:
      defaults['retry_setting.unlimited_task_retries'] ?? false,
  },
  monitor_setting: {
    auto_test_channel_enabled:
      defaults['monitor_setting.auto_test_channel_enabled'],
    auto_test_channel_minutes:
      defaults['monitor_setting.auto_test_channel_minutes'],
    channel_test_mode: normalizeChannelTestMode(
      defaults['monitor_setting.channel_test_mode']
    ),
    zero_token_as_failure:
      defaults['monitor_setting.zero_token_as_failure'] ?? false,
    channel_availability_notify_enabled:
      defaults['monitor_setting.channel_availability_notify_enabled'] ?? false,
    channel_availability_notify_recipients: formatAvailabilityRecipientInput(
      defaults['monitor_setting.channel_availability_notify_recipients']
    ),
  },
})

const normalizeDefaults = (
  defaults: RoutingReliabilitySectionProps['defaultValues']
): NormalizedRoutingReliabilityValues => ({
  RetryTimes: defaults.RetryTimes ?? 10,
  ChannelDisableThreshold: (defaults.ChannelDisableThreshold ?? '').trim(),
  AutomaticDisableChannelEnabled: defaults.AutomaticDisableChannelEnabled,
  AutoDisableTolerance: defaults.AutoDisableTolerance ?? 0,
  AutomaticEnableChannelEnabled: defaults.AutomaticEnableChannelEnabled,
  AutomaticDisableKeywords: normalizeLineEndings(
    defaults.AutomaticDisableKeywords ?? ''
  ),
  AutomaticDisableStatusCodes: parseHttpStatusCodeRules(
    defaults.AutomaticDisableStatusCodes ?? ''
  ).normalized,
  AutomaticRetryStatusCodes: parseHttpStatusCodeRules(
    defaults.AutomaticRetryStatusCodes ?? ''
  ).normalized,
  'retry_setting.unlimited': defaults['retry_setting.unlimited'] ?? false,
  'retry_setting.time_budget_seconds':
    defaults['retry_setting.time_budget_seconds'] ?? 0,
  'retry_setting.delay_strategy': normalizeRetryDelayStrategy(
    defaults['retry_setting.delay_strategy']
  ),
  'retry_setting.fixed_delay_milliseconds':
    defaults['retry_setting.fixed_delay_milliseconds'] ?? 0,
  'retry_setting.exponential_base_delay_milliseconds':
    defaults['retry_setting.exponential_base_delay_milliseconds'] ?? 250,
  'retry_setting.exponential_max_delay_milliseconds':
    defaults['retry_setting.exponential_max_delay_milliseconds'] ?? 10000,
  'retry_setting.jitter_percent':
    defaults['retry_setting.jitter_percent'] ?? 20,
  'retry_setting.respect_retry_after':
    defaults['retry_setting.respect_retry_after'] ?? false,
  'retry_setting.channel_strategy': normalizeRetryChannelStrategy(
    defaults['retry_setting.channel_strategy']
  ),
  'retry_setting.exhausted_action': normalizeRetryExhaustedAction(
    defaults['retry_setting.exhausted_action']
  ),
  'retry_setting.try_other_keys':
    defaults['retry_setting.try_other_keys'] ?? false,
  'retry_setting.unlimited_task_retries':
    defaults['retry_setting.unlimited_task_retries'] ?? false,
  'monitor_setting.auto_test_channel_enabled':
    defaults['monitor_setting.auto_test_channel_enabled'],
  'monitor_setting.auto_test_channel_minutes':
    defaults['monitor_setting.auto_test_channel_minutes'],
  'monitor_setting.channel_test_mode': normalizeChannelTestMode(
    defaults['monitor_setting.channel_test_mode']
  ),
  'monitor_setting.zero_token_as_failure':
    defaults['monitor_setting.zero_token_as_failure'] ?? false,
  'monitor_setting.channel_availability_notify_enabled':
    defaults['monitor_setting.channel_availability_notify_enabled'] ?? false,
  'monitor_setting.channel_availability_notify_recipients': JSON.stringify(
    normalizeAvailabilityRecipientOption(
      defaults['monitor_setting.channel_availability_notify_recipients']
    )
  ),
})

const normalizeFormValues = (
  values: RoutingReliabilityFormValues
): NormalizedRoutingReliabilityValues => {
  const availabilityNotification = normalizeAvailabilityNotificationFormValues(
    values.monitor_setting.channel_availability_notify_enabled,
    values.monitor_setting.channel_availability_notify_recipients
  )

  return {
    RetryTimes: values.RetryTimes,
    ChannelDisableThreshold: values.ChannelDisableThreshold.trim(),
    AutomaticDisableChannelEnabled: values.AutomaticDisableChannelEnabled,
    AutoDisableTolerance: values.AutoDisableTolerance,
    AutomaticEnableChannelEnabled: values.AutomaticEnableChannelEnabled,
    AutomaticDisableKeywords: normalizeLineEndings(
      values.AutomaticDisableKeywords
    ),
    AutomaticDisableStatusCodes: parseHttpStatusCodeRules(
      values.AutomaticDisableStatusCodes
    ).normalized,
    AutomaticRetryStatusCodes: parseHttpStatusCodeRules(
      values.AutomaticRetryStatusCodes
    ).normalized,
    'retry_setting.unlimited': values.retry_setting.unlimited,
    'retry_setting.time_budget_seconds':
      values.retry_setting.time_budget_seconds,
    'retry_setting.delay_strategy': values.retry_setting.delay_strategy,
    'retry_setting.fixed_delay_milliseconds':
      values.retry_setting.fixed_delay_milliseconds,
    'retry_setting.exponential_base_delay_milliseconds':
      values.retry_setting.exponential_base_delay_milliseconds,
    'retry_setting.exponential_max_delay_milliseconds':
      values.retry_setting.exponential_max_delay_milliseconds,
    'retry_setting.jitter_percent': values.retry_setting.jitter_percent,
    'retry_setting.respect_retry_after':
      values.retry_setting.respect_retry_after,
    'retry_setting.channel_strategy': values.retry_setting.channel_strategy,
    'retry_setting.exhausted_action': values.retry_setting.exhausted_action,
    'retry_setting.try_other_keys': values.retry_setting.try_other_keys,
    'retry_setting.unlimited_task_retries':
      values.retry_setting.unlimited_task_retries,
    'monitor_setting.auto_test_channel_enabled':
      values.monitor_setting.auto_test_channel_enabled,
    'monitor_setting.auto_test_channel_minutes':
      values.monitor_setting.auto_test_channel_minutes,
    'monitor_setting.channel_test_mode':
      values.monitor_setting.channel_test_mode,
    'monitor_setting.zero_token_as_failure':
      values.monitor_setting.zero_token_as_failure,
    'monitor_setting.channel_availability_notify_enabled':
      availabilityNotification.enabled,
    'monitor_setting.channel_availability_notify_recipients': JSON.stringify(
      availabilityNotification.recipients
    ),
  }
}

export function RoutingReliabilitySection({
  defaultValues,
}: RoutingReliabilitySectionProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const updateOptionsBulk = useMutation({
    mutationFn: (options: Record<string, string>) =>
      updateSystemOptionsBulk({ options }),
  })
  const baselineRef = useRef<NormalizedRoutingReliabilityValues>(
    normalizeDefaults(defaultValues)
  )
  const submitGuardRef = useRef(false)

  const formDefaults = useMemo(
    () => buildFormDefaults(defaultValues),
    [defaultValues]
  )
  const localizedSchema = useMemo(
    () => createRoutingReliabilitySchema((key) => t(key)),
    [t]
  )

  const form = useForm<
    RoutingReliabilityFormInput,
    unknown,
    RoutingReliabilityFormValues
  >({
    resolver: zodResolver(localizedSchema),
    defaultValues: formDefaults,
  })

  useResetForm(form, formDefaults)

  const autoDisableStatusCodes = form.watch('AutomaticDisableStatusCodes')
  const autoRetryStatusCodes = form.watch('AutomaticRetryStatusCodes')
  const channelTestMode = form.watch('monitor_setting.channel_test_mode')
  const retryTimes = form.watch('RetryTimes')
  const retryUnlimited = form.watch('retry_setting.unlimited')
  const retryTimeBudget = form.watch('retry_setting.time_budget_seconds')
  const retryDelayStrategy = form.watch('retry_setting.delay_strategy')
  const retryChannelStrategy = form.watch('retry_setting.channel_strategy')
  const availabilityNotifyEnabled = form.watch(
    'monitor_setting.channel_availability_notify_enabled'
  )
  const availabilityRecipientInput = form.watch(
    'monitor_setting.channel_availability_notify_recipients'
  )
  const availabilityRecipientState = useMemo(
    () => getAvailabilityRecipientInputState(availabilityRecipientInput),
    [availabilityRecipientInput]
  )
  const availabilityTestRequest = useMemo(
    () => buildAvailabilityNotificationTestRequest(availabilityRecipientInput),
    [availabilityRecipientInput]
  )
  const autoDisableParsed = useMemo(
    () => parseHttpStatusCodeRules(autoDisableStatusCodes),
    [autoDisableStatusCodes]
  )
  const autoRetryParsed = useMemo(
    () => parseHttpStatusCodeRules(autoRetryStatusCodes),
    [autoRetryStatusCodes]
  )

  const testAvailabilityNotification = useMutation({
    mutationFn: ({ recipients }: { recipients: string[] }) =>
      sendChannelAvailabilityNotificationTest(recipients),
    onSuccess: (data) => {
      const feedback = classifyAvailabilityTestResponse(data)
      if (feedback.kind === 'warning') {
        toast.warning(
          t('Test email sent to {{sent}} recipients; {{failed}} failed', {
            sent: feedback.succeeded,
            failed: feedback.failed,
          })
        )
        return
      }
      if (feedback.kind === 'error') {
        toast.error(
          feedback.reason === 'all_failed'
            ? t(
                'No test emails were sent. Check SMTP configuration and server logs.'
              )
            : feedback.message || t('Failed to send test email')
        )
        return
      }
      toast.success(
        t('Test email sent to {{count}} recipients', {
          count: feedback.succeeded,
        })
      )
    },
    onError: (error: AxiosError<{ message?: string }>) => {
      toast.error(
        error.response?.data?.message || t('Failed to send test email')
      )
    },
  })

  const onSubmit = async (values: RoutingReliabilityFormValues) => {
    if (!acquireSubmissionGuard(submitGuardRef)) return

    try {
      const normalized = normalizeFormValues(values)
      const options = buildChangedOptionPayload(normalized, baselineRef.current)

      if (Object.keys(options).length === 0) {
        toast.info(t('No changes to save'))
        return
      }

      const response = await updateOptionsBulk.mutateAsync(options)
      const outcome = getBulkUpdateOutcome(response)
      if (!outcome.success) {
        toast.error(outcome.message || t('Failed to update settings'))
        return
      }
      baselineRef.current = normalized
      await queryClient.invalidateQueries({ queryKey: ['system-options'] })
      toast.success(t('Settings updated successfully'))
    } catch (error) {
      const requestError = error as AxiosError<{ message?: string }>
      toast.error(
        requestError.response?.data?.message || t('Failed to update settings')
      )
    } finally {
      releaseSubmissionGuard(submitGuardRef)
    }
  }

  return (
    <SettingsSection title={t('Routing Reliability')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={
              updateOptionsBulk.isPending || form.formState.isSubmitting
            }
          />

          <div className='flex min-w-0 flex-col gap-4'>
            <div className='flex flex-col gap-1'>
              <h4 className='text-sm font-medium'>{t('Request failover')}</h4>
            </div>
            <div className='grid min-w-0 gap-6 lg:grid-cols-2'>
              <FormField
                control={form.control}
                name='retry_setting.unlimited'
                render={({ field }) => (
                  <SettingsSwitchItem className='lg:col-span-2'>
                    <SettingsSwitchContent>
                      <FormLabel>{t('Unlimited retries')}</FormLabel>
                      <FormDescription>
                        {t(
                          'Keep retrying eligible requests until the time budget or candidate policy stops the chain.'
                        )}
                      </FormDescription>
                    </SettingsSwitchContent>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </SettingsSwitchItem>
                )}
              />

              {!retryUnlimited && (
                <FormField
                  control={form.control}
                  name='RetryTimes'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Retry Times')}</FormLabel>
                      <FormControl>
                        <Input
                          type='number'
                          min='0'
                          step='1'
                          {...safeNumberFieldProps(field)}
                        />
                      </FormControl>
                      <FormDescription>
                        {t('Extra attempts after the initial request')}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              )}

              <FormField
                control={form.control}
                name='retry_setting.time_budget_seconds'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Retry time budget (seconds)')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min='0'
                        step='1'
                        {...safeNumberFieldProps(field)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t('0 means no time limit')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='retry_setting.delay_strategy'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Retry delay')}</FormLabel>
                    <Select
                      items={[
                        { value: 'immediate', label: t('Immediate') },
                        { value: 'fixed', label: t('Fixed interval') },
                        {
                          value: 'exponential',
                          label: t('Exponential backoff'),
                        },
                      ]}
                      value={field.value}
                      onValueChange={field.onChange}
                    >
                      <FormControl>
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent alignItemWithTrigger={false}>
                        <SelectGroup>
                          <SelectItem value='immediate'>
                            {t('Immediate')}
                          </SelectItem>
                          <SelectItem value='fixed'>
                            {t('Fixed interval')}
                          </SelectItem>
                          <SelectItem value='exponential'>
                            {t('Exponential backoff')}
                          </SelectItem>
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                    <FormDescription>
                      {t('Delay applied before each eligible retry')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              {retryDelayStrategy === 'fixed' && (
                <FormField
                  control={form.control}
                  name='retry_setting.fixed_delay_milliseconds'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Fixed delay (milliseconds)')}</FormLabel>
                      <FormControl>
                        <Input
                          type='number'
                          min='0'
                          step='1'
                          {...safeNumberFieldProps(field)}
                        />
                      </FormControl>
                      <FormDescription>
                        {t('Wait this long before every retry')}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              )}

              {retryDelayStrategy === 'exponential' && (
                <>
                  <FormField
                    control={form.control}
                    name='retry_setting.exponential_base_delay_milliseconds'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Base delay (milliseconds)')}</FormLabel>
                        <FormControl>
                          <Input
                            type='number'
                            min='0'
                            step='1'
                            {...safeNumberFieldProps(field)}
                          />
                        </FormControl>
                        <FormDescription>
                          {t('Starting delay for exponential backoff')}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />

                  <FormField
                    control={form.control}
                    name='retry_setting.exponential_max_delay_milliseconds'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>
                          {t('Maximum delay (milliseconds)')}
                        </FormLabel>
                        <FormControl>
                          <Input
                            type='number'
                            min='0'
                            step='1'
                            {...safeNumberFieldProps(field)}
                          />
                        </FormControl>
                        <FormDescription>
                          {t('Caps each exponential delay; 0 means no cap')}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />

                  <FormField
                    control={form.control}
                    name='retry_setting.jitter_percent'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Jitter (percent)')}</FormLabel>
                        <FormControl>
                          <Input
                            type='number'
                            min='0'
                            step='any'
                            {...safeNumberFieldProps(field)}
                          />
                        </FormControl>
                        <FormDescription>
                          {t(
                            'Randomizes delays to spread simultaneous retries'
                          )}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                </>
              )}

              <FormField
                control={form.control}
                name='retry_setting.respect_retry_after'
                render={({ field }) => (
                  <SettingsSwitchItem className='lg:col-span-2'>
                    <SettingsSwitchContent>
                      <FormLabel>{t('Respect Retry-After')}</FormLabel>
                      <FormDescription>
                        {t(
                          'Use a longer upstream Retry-After delay when provided'
                        )}
                      </FormDescription>
                    </SettingsSwitchContent>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </SettingsSwitchItem>
                )}
              />

              <FormField
                control={form.control}
                name='retry_setting.channel_strategy'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Channel retry strategy')}</FormLabel>
                    <Select
                      items={[
                        { value: 'legacy', label: t('Priority fallback') },
                        {
                          value: 'same_priority',
                          label: t('Same-priority first'),
                        },
                      ]}
                      value={field.value}
                      onValueChange={field.onChange}
                    >
                      <FormControl>
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent alignItemWithTrigger={false}>
                        <SelectGroup>
                          <SelectItem value='legacy'>
                            {t('Priority fallback')}
                          </SelectItem>
                          <SelectItem value='same_priority'>
                            {t('Same-priority first')}
                          </SelectItem>
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                    <FormDescription>
                      {field.value === 'legacy'
                        ? t(
                            'Each retry moves to the next priority; the lowest priority continues weighted selection.'
                          )
                        : t(
                            'Try unused candidates at the current priority before moving lower.'
                          )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              {retryChannelStrategy === 'same_priority' && (
                <FormField
                  control={form.control}
                  name='retry_setting.exhausted_action'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>
                        {t('When candidates are exhausted')}
                      </FormLabel>
                      <Select
                        items={[
                          { value: 'stop', label: t('Stop retrying') },
                          { value: 'cycle', label: t('Start a new round') },
                        ]}
                        value={field.value}
                        onValueChange={field.onChange}
                      >
                        <FormControl>
                          <SelectTrigger>
                            <SelectValue />
                          </SelectTrigger>
                        </FormControl>
                        <SelectContent alignItemWithTrigger={false}>
                          <SelectGroup>
                            <SelectItem value='stop'>
                              {t('Stop retrying')}
                            </SelectItem>
                            <SelectItem value='cycle'>
                              {t('Start a new round')}
                            </SelectItem>
                          </SelectGroup>
                        </SelectContent>
                      </Select>
                      <FormDescription>
                        {t(
                          'New rounds keep the current retry-count mode and time budget; consumed attempts and elapsed budget are not reset.'
                        )}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              )}

              <FormField
                control={form.control}
                name='retry_setting.try_other_keys'
                render={({ field }) => (
                  <SettingsSwitchItem className='lg:col-span-2'>
                    <SettingsSwitchContent>
                      <FormLabel>
                        {t('Try other keys in the channel')}
                      </FormLabel>
                      <FormDescription>
                        {t(
                          'When disabled, each channel selection uses one key. When enabled, other available keys in that channel can be retried as channel and key-index candidates.'
                        )}
                      </FormDescription>
                    </SettingsSwitchContent>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </SettingsSwitchItem>
                )}
              />

              {retryUnlimited && (
                <FormField
                  control={form.control}
                  name='retry_setting.unlimited_task_retries'
                  render={({ field }) => (
                    <SettingsSwitchItem className='lg:col-span-2'>
                      <SettingsSwitchContent>
                        <FormLabel>
                          {t('Allow unlimited async task retries')}
                        </FormLabel>
                        <FormDescription>
                          {t(
                            'Advanced: repeated task submission can create duplicate work and charges.'
                          )}
                        </FormDescription>
                      </SettingsSwitchContent>
                      <FormControl>
                        <Switch
                          checked={field.value}
                          onCheckedChange={field.onChange}
                        />
                      </FormControl>
                    </SettingsSwitchItem>
                  )}
                />
              )}

              <FormField
                control={form.control}
                name='AutomaticRetryStatusCodes'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Auto-retry status codes')}</FormLabel>
                    <FormControl>
                      <Input
                        placeholder={t('e.g. 401, 403, 429, 500-599')}
                        value={field.value}
                        onChange={(event) => field.onChange(event.target.value)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Accepts comma-separated status codes and inclusive ranges.'
                      )}{' '}
                      {autoRetryParsed.ok &&
                        autoRetryParsed.normalized &&
                        autoRetryParsed.normalized !== field.value.trim() && (
                          <span className='text-muted-foreground'>
                            {t('Normalized:')} {autoRetryParsed.normalized}
                          </span>
                        )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              {retryUnlimited && retryTimeBudget === 0 && (
                <Alert className='lg:col-span-2'>
                  <AlertDescription>
                    {t(
                      'Unlimited retries without a time budget can keep requests open indefinitely.'
                    )}
                  </AlertDescription>
                </Alert>
              )}

              {retryDelayStrategy === 'immediate' &&
                (retryUnlimited || Number(retryTimes) > 10) && (
                  <Alert className='lg:col-span-2'>
                    <AlertDescription>
                      {t(
                        'Immediate high-volume retries can increase upstream load and duplicate charges.'
                      )}
                    </AlertDescription>
                  </Alert>
                )}
            </div>
          </div>

          <Separator />

          <div className='flex min-w-0 flex-col gap-4'>
            <div className='flex flex-col gap-1'>
              <h4 className='text-sm font-medium'>
                {t('Upstream anomaly detection')}
              </h4>
            </div>
            <FormField
              control={form.control}
              name='monitor_setting.zero_token_as_failure'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <div className='flex min-w-0 items-center gap-1.5'>
                      <FormLabel>{t('Anomalous response detection')}</FormLabel>
                      <TooltipProvider>
                        <Tooltip>
                          <TooltipTrigger
                            type='button'
                            aria-label={t('More information')}
                            className='text-muted-foreground hover:text-foreground inline-flex size-5 shrink-0 items-center justify-center'
                          >
                            <HugeiconsIcon
                              icon={InformationCircleIcon}
                              className='size-4'
                              aria-hidden='true'
                            />
                          </TooltipTrigger>
                          <TooltipContent>
                            <p>
                              {t(
                                'Empty direct OpenAI/Codex Responses streams enter retry before meaningful output only when a retry opportunity and an available candidate remain. Streams already sending output and client cancellations are not retried.'
                              )}
                            </p>
                          </TooltipContent>
                        </Tooltip>
                      </TooltipProvider>
                    </div>
                    <FormDescription>
                      {t(
                        'Missing usage or zero total tokens is treated as a channel failure.'
                      )}
                    </FormDescription>
                  </SettingsSwitchContent>
                  <FormControl>
                    <Switch
                      checked={field.value}
                      onCheckedChange={field.onChange}
                    />
                  </FormControl>
                </SettingsSwitchItem>
              )}
            />
          </div>

          <Separator />

          <div className='flex min-w-0 flex-col gap-4'>
            <div className='flex flex-col gap-1'>
              <h4 className='text-sm font-medium'>
                {t('Channel health checks')}
              </h4>
            </div>
            <div className='grid min-w-0 gap-6 lg:grid-cols-3'>
              <FormField
                control={form.control}
                name='monitor_setting.auto_test_channel_enabled'
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>{t('Scheduled channel tests')}</FormLabel>
                      <FormDescription>
                        {t(
                          'Automatically probe all channels in the background'
                        )}
                      </FormDescription>
                    </SettingsSwitchContent>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </SettingsSwitchItem>
                )}
              />

              <FormField
                control={form.control}
                name='monitor_setting.channel_test_mode'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Channel test mode')}</FormLabel>
                    <Select
                      items={[
                        {
                          value: 'scheduled_all',
                          label: t('Scheduled full test'),
                        },
                        {
                          value: 'passive_recovery',
                          label: t('Passive recovery only'),
                        },
                      ]}
                      value={field.value}
                      onValueChange={field.onChange}
                    >
                      <FormControl>
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent alignItemWithTrigger={false}>
                        <SelectGroup>
                          <SelectItem value='scheduled_all'>
                            {t('Scheduled full test')}
                          </SelectItem>
                          <SelectItem value='passive_recovery'>
                            {t('Passive recovery only')}
                          </SelectItem>
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                    <FormDescription>
                      {t(
                        'Scheduled full test probes non-manually-disabled channels; passive recovery only checks auto-disabled channels after real request failures.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='monitor_setting.auto_test_channel_minutes'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Test interval (minutes)')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={1}
                        step={1}
                        {...safeNumberFieldProps(field)}
                      />
                    </FormControl>
                    <FormDescription>
                      {channelTestMode === 'passive_recovery'
                        ? t(
                            'How frequently the system checks auto-disabled channels for recovery'
                          )
                        : t('How frequently the system tests all channels')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='AutomaticEnableChannelEnabled'
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>{t('Re-enable on success')}</FormLabel>
                      <FormDescription>
                        {t(
                          'Bring channels back online after successful checks'
                        )}
                      </FormDescription>
                    </SettingsSwitchContent>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </SettingsSwitchItem>
                )}
              />
            </div>

            <div className='flex min-w-0 flex-col gap-3'>
              <h5 className='text-sm font-medium'>
                {t('Availability email notifications')}
              </h5>
              <div className='grid min-w-0 gap-4 lg:grid-cols-3'>
                <FormField
                  control={form.control}
                  name='monitor_setting.channel_availability_notify_enabled'
                  render={({ field }) => (
                    <SettingsSwitchItem className='lg:col-span-3'>
                      <SettingsSwitchContent>
                        <FormLabel>
                          {t('Email on availability changes')}
                        </FormLabel>
                        <FormDescription>
                          {t(
                            'Notify when the system changes between having at least one enabled channel and having none. Health checks, automatic disable or recovery, and manual switches are included; unchanged states are not repeated.'
                          )}
                        </FormDescription>
                      </SettingsSwitchContent>
                      <FormControl>
                        <Switch
                          checked={field.value}
                          onCheckedChange={field.onChange}
                        />
                      </FormControl>
                    </SettingsSwitchItem>
                  )}
                />

                {availabilityNotifyEnabled ? (
                  <FormField
                    control={form.control}
                    name='monitor_setting.channel_availability_notify_recipients'
                    render={({ field }) => (
                      <FormItem className='lg:col-span-3'>
                        <FormLabel>{t('Notification recipients')}</FormLabel>
                        <FormControl>
                          <Textarea
                            {...field}
                            rows={4}
                            className='min-h-24 resize-y'
                            placeholder={
                              'alerts@example.com\noncall@example.com'
                            }
                          />
                        </FormControl>
                        <FormDescription>
                          {availabilityRecipientState.invalidRecipients.length >
                          0
                            ? t(
                                '{{valid}} valid recipients; {{invalid}} invalid. SMTP is configured under System settings -> Operations -> Email.',
                                {
                                  valid:
                                    availabilityRecipientState.validRecipients
                                      .length,
                                  invalid:
                                    availabilityRecipientState.invalidRecipients
                                      .length,
                                }
                              )
                            : t(
                                '{{count}} valid recipients. SMTP is configured under System settings -> Operations -> Email.',
                                {
                                  count:
                                    availabilityRecipientState.validRecipients
                                      .length,
                                }
                              )}
                        </FormDescription>
                        <FormMessage />
                        <div className='flex flex-wrap items-center gap-2'>
                          <Button
                            type='button'
                            variant='outline'
                            disabled={
                              availabilityTestRequest === null ||
                              testAvailabilityNotification.isPending
                            }
                            onClick={() => {
                              if (availabilityTestRequest) {
                                testAvailabilityNotification.mutate(
                                  availabilityTestRequest
                                )
                              }
                            }}
                          >
                            {testAvailabilityNotification.isPending ? (
                              <Spinner data-icon='inline-start' />
                            ) : (
                              <HugeiconsIcon
                                icon={MailSend01Icon}
                                data-icon='inline-start'
                                aria-hidden='true'
                              />
                            )}
                            {testAvailabilityNotification.isPending
                              ? t('Sending test email...')
                              : t('Send test email')}
                          </Button>
                        </div>
                      </FormItem>
                    )}
                  />
                ) : null}
              </div>
            </div>
          </div>

          <Separator />

          <div className='flex min-w-0 flex-col gap-4'>
            <div className='flex flex-col gap-1'>
              <h4 className='text-sm font-medium'>{t('Auto-disable rules')}</h4>
            </div>
            <div className='grid min-w-0 gap-6 lg:grid-cols-2'>
              <FormField
                control={form.control}
                name='AutomaticDisableChannelEnabled'
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>{t('Disable on failure')}</FormLabel>
                      <FormDescription>
                        {t('Automatically disable channels when tests fail')}
                      </FormDescription>
                    </SettingsSwitchContent>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </SettingsSwitchItem>
                )}
              />

              <FormField
                control={form.control}
                name='AutoDisableTolerance'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Failure tolerance')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={0}
                        max={999}
                        step={1}
                        {...safeNumberFieldProps(field)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Number of consecutive failures before disabling the channel (0 = disable immediately)'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='ChannelDisableThreshold'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Disable threshold (seconds)')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={0}
                        step={1}
                        value={field.value}
                        onChange={(event) => field.onChange(event.target.value)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Automatically disable channels exceeding this response time'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='AutomaticDisableStatusCodes'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Auto-disable status codes')}</FormLabel>
                    <FormControl>
                      <Input
                        placeholder={t('e.g. 401, 403, 429, 500-599')}
                        value={field.value}
                        onChange={(event) => field.onChange(event.target.value)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Accepts comma-separated status codes and inclusive ranges.'
                      )}{' '}
                      {autoDisableParsed.ok &&
                        autoDisableParsed.normalized &&
                        autoDisableParsed.normalized !== field.value.trim() && (
                          <span className='text-muted-foreground'>
                            {t('Normalized:')} {autoDisableParsed.normalized}
                          </span>
                        )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='AutomaticDisableKeywords'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Failure keywords')}</FormLabel>
                    <FormControl>
                      <Textarea
                        rows={6}
                        placeholder={t('one keyword per line')}
                        {...field}
                        onChange={(event) => field.onChange(event.target.value)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'If an upstream error contains any of these keywords (case insensitive), the channel will be disabled automatically.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>
          </div>
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
