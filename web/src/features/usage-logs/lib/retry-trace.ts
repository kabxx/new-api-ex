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
import type { RetryTraceEntry } from '../types'

export type RetryTraceViewEntry = RetryTraceEntry & {
  renderKey: string
}

export type RetryTraceSummary = {
  entries: RetryTraceViewEntry[]
  total: number
  omitted: number
}

export type UseChannelSummary = {
  channels: Array<number | string>
  total: number
  omitted: number
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  if (value == null || typeof value !== 'object' || Array.isArray(value)) {
    return undefined
  }
  return value as Record<string, unknown>
}

function asNonNegativeInteger(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
    ? value
    : undefined
}

function asFiniteNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function asTrimmedString(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const trimmed = value.trim()
  return trimmed || undefined
}

function sanitizeRetryTraceEntry(value: unknown): RetryTraceEntry | undefined {
  const entry = asRecord(value)
  if (!entry) return undefined

  return {
    attempt: asNonNegativeInteger(entry.attempt),
    channel_id: asNonNegativeInteger(entry.channel_id),
    channel_name: asTrimmedString(entry.channel_name),
    priority: asFiniteNumber(entry.priority),
    multi_key_index: asNonNegativeInteger(entry.multi_key_index),
    duration_ms: asFiniteNumber(entry.duration_ms),
    status_code: asNonNegativeInteger(entry.status_code),
    error_code: asTrimmedString(entry.error_code),
    delay_ms: asFiniteNumber(entry.delay_ms),
    decision: asTrimmedString(entry.decision),
    outcome: asTrimmedString(entry.outcome),
  }
}

function normalizeCounts(
  retained: number,
  rawTotal: unknown,
  rawOmitted: unknown
) {
  const statedTotal = asNonNegativeInteger(rawTotal)
  const statedOmitted = asNonNegativeInteger(rawOmitted)
  const total = Math.max(
    retained,
    retained + (statedOmitted ?? 0),
    statedTotal ?? retained + (statedOmitted ?? 0)
  )
  const omitted = Math.max(statedOmitted ?? 0, total - retained)
  return { total, omitted }
}

export function normalizeRetryTraceSummary(
  adminInfo: unknown
): RetryTraceSummary {
  const info = asRecord(adminInfo)
  const rawEntries = Array.isArray(info?.retry_trace) ? info.retry_trace : []
  const entries = rawEntries.flatMap((value, index) => {
    const entry = sanitizeRetryTraceEntry(value)
    if (!entry) return []
    return [
      {
        ...entry,
        renderKey: `${entry.attempt ?? 'unknown'}:${entry.channel_id ?? 'unknown'}:${index}`,
      },
    ]
  })
  const counts = normalizeCounts(
    entries.length,
    info?.retry_trace_total,
    info?.retry_trace_omitted
  )
  return { entries, ...counts }
}

export function normalizeUseChannelSummary(
  adminInfo: unknown
): UseChannelSummary {
  const info = asRecord(adminInfo)
  const channels = Array.isArray(info?.use_channel)
    ? info.use_channel.reduce<Array<number | string>>((result, value) => {
        if (typeof value === 'number' && Number.isFinite(value)) {
          result.push(value)
          return result
        }
        const text = asTrimmedString(value)
        if (text) result.push(text)
        return result
      }, [])
    : []
  const counts = normalizeCounts(
    channels.length,
    info?.use_channel_total,
    info?.use_channel_omitted
  )
  return { channels, ...counts }
}
