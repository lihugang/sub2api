/**
 * 高峰时段倍率的共享展示逻辑。
 *
 * 后端新的 time_rate_rules 固定按 UTC+08:00 计费；这里仅把区间换算成
 * 浏览器本地时区供用户阅读，实际计费口径不变。旧的单窗口字段继续使用
 * 调用方传入的服务器时区标签。
 */

export interface PeakRateFields {
  peak_rate_enabled?: boolean
  peak_start?: string
  peak_end?: string
  peak_rate_multiplier?: number
  time_rate_rules?: Array<{ start: string; end: string; multiplier: number }>
}

export function hasPeakRate(fields?: PeakRateFields | null): boolean {
  return Boolean(fields?.time_rate_rules?.length || (fields?.peak_rate_enabled && fields.peak_start && fields.peak_end))
}

/** "+08:00" → "UTC+08:00"；旧缓存无该字段时返回空串，调用方降级为不带时区标注 */
export function serverTimezoneLabel(utcOffset?: string | null): string {
  return utcOffset ? `UTC${utcOffset}` : ''
}

const BILLING_TIMEZONE = 'Asia/Shanghai'

function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}

function sourceDateParts(now: Date): { year: number; month: number; day: number } {
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone: BILLING_TIMEZONE,
    year: 'numeric',
    month: 'numeric',
    day: 'numeric'
  }).formatToParts(now)
  const value = (type: string) => Number(parts.find((part) => part.type === type)?.value)
  return { year: value('year'), month: value('month'), day: value('day') }
}

function parseTime(value: string): { hour: number; minute: number } | null {
  const match = /^(\d{2}):(\d{2})$/.exec(value)
  if (!match) return null
  const hour = Number(match[1])
  const minute = Number(match[2])
  if (minute > 59 || hour > 24 || (hour === 24 && minute !== 0)) return null
  return { hour, minute }
}

function formatLocalTime(value: string, now: Date, targetTimezone: string): string {
  const parsed = parseTime(value)
  if (!parsed) return value
  const date = sourceDateParts(now)
  // Asia/Shanghai is UTC+08:00 and has no DST. 24:00 naturally rolls into the
  // next date when represented as a UTC instant.
  const instant = new Date(Date.UTC(date.year, date.month - 1, date.day, parsed.hour - 8, parsed.minute))
  const parts = new Intl.DateTimeFormat('en-GB', {
    timeZone: targetTimezone,
    hour: '2-digit',
    minute: '2-digit',
    hour12: false
  }).formatToParts(instant)
  const hour = parts.find((part) => part.type === 'hour')?.value
  const minute = parts.find((part) => part.type === 'minute')?.value
  return hour && minute ? `${hour}:${minute}` : value
}

function formatLocalTimeRateRules(
  rules: Array<{ start: string; end: string; multiplier: number }>
): string {
  const targetTimezone = browserTimezone()
  const now = new Date()
  const base = rules
    .map((rule) => `${formatLocalTime(rule.start, now, targetTimezone)}-${formatLocalTime(rule.end, now, targetTimezone)} ×${rule.multiplier}`)
    .join('；')
  return `${base} (${targetTimezone}; billing UTC+08:00)`
}

/** "14:00-18:00 ×2 (local timezone; billing UTC+08:00)" */
export function formatPeakRateWindow(
  fields: PeakRateFields | null | undefined,
  tzLabel?: string
): string {
  if (!hasPeakRate(fields) || !fields) return ''
  if (fields.time_rate_rules?.length) {
    return formatLocalTimeRateRules(fields.time_rate_rules)
  }
  const base = `${fields.peak_start}-${fields.peak_end} ×${fields.peak_rate_multiplier ?? 1}`
  return tzLabel ? `${base} (${tzLabel})` : base
}
