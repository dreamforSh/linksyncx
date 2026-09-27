/**
 * Clash proxy pool helpers shared by the Clash page, the account proxy selector,
 * account status indicators and account creation guards.
 */

import type {
  ClashExitList,
  ClashExitOption,
  ClashNodeTraffic,
  ClashPlatform,
  ClashPlatformCheckResult
} from '@/types'
import { extractApiErrorCode, extractApiErrorMetadata } from '@/utils/apiError'

type TranslateFn = (key: string, params?: Record<string, unknown>) => string

/** Temp-unschedulable reasons owned by the Clash pool start with this prefix (auto-cleared on recovery). */
export const CLASH_EXIT_PAUSE_PREFIX = '[clash-exit]'

export function isClashExitPauseReason(reason: string | null | undefined): boolean {
  return typeof reason === 'string' && reason.trimStart().startsWith(CLASH_EXIT_PAUSE_PREFIX)
}

/** "[clash-exit] HK 01: health check failing" -> "HK 01: health check failing" */
export function stripClashExitPauseReason(reason: string | null | undefined): string {
  if (!isClashExitPauseReason(reason)) return reason?.trim() ?? ''
  return (reason as string).trimStart().slice(CLASH_EXIT_PAUSE_PREFIX.length).trim()
}

type ReasonMatch = { key: string; params?: Record<string, unknown> }

/**
 * Maps the server's reason vocabulary to i18n keys: unavailable_reason (see
 * ClashNodeView.unavailableReason) and node status_reason values.
 */
function matchClashUnavailableReason(text: string): ReasonMatch | null {
  switch (text) {
    case 'subscription deleted':
      return { key: 'admin.clash.reasons.subscriptionDeleted' }
    case 'subscription disabled':
      return { key: 'admin.clash.reasons.subscriptionDisabled' }
    case 'node removed from subscription':
    case 'removed from subscription':
      return { key: 'admin.clash.reasons.nodeMissing' }
    case 'node disabled':
      return { key: 'admin.clash.reasons.nodeDisabled' }
    case 'disabled by admin':
      return { key: 'admin.clash.reasons.disabledByAdmin' }
    case 'health check failing':
      return { key: 'admin.clash.reasons.healthFailing' }
    case 'server is a loopback or link-local address':
      return { key: 'admin.clash.reasons.loopbackServer' }
    case 'server is a private network address':
      return { key: 'admin.clash.reasons.privateServer' }
    case "exit IP equals this server's own IP":
      return { key: 'admin.clash.reasons.exitIsServer' }
  }
  const listener = /^listener unavailable:\s*(.*)$/is.exec(text)
  if (listener) return { key: 'admin.clash.reasons.listenerUnavailable', params: { detail: listener[1].trim() || '-' } }
  const invalid = /^node invalid:\s*(.*)$/is.exec(text)
  if (invalid) return { key: 'admin.clash.reasons.nodeInvalid', params: { detail: invalid[1].trim() || '-' } }
  const changed = /^exit IP changed to\s*(.*?),\s*awaiting confirmation$/is.exec(text)
  if (changed) return { key: 'admin.clash.reasons.exitChanged', params: { ip: changed[1].trim() || '-' } }
  return null
}

/** Localized unavailable reason; unknown server text is returned unchanged. */
export function localizeClashUnavailableReason(reason: string | null | undefined, t: TranslateFn): string {
  const text = (reason || '').trim()
  if (!text) return ''
  const match = matchClashUnavailableReason(text)
  return match ? t(match.key, match.params) : text
}

/**
 * Splits "[clash-exit] <node name>: <reason>" into its parts (the node name itself may
 * contain ": "). Returns null for pauses that are not owned by the Clash pool.
 */
export function describeClashExitPause(
  reason: string | null | undefined,
  t: TranslateFn
): { node: string; reason: string } | null {
  if (!isClashExitPauseReason(reason)) return null
  const body = stripClashExitPauseReason(reason)
  for (let index = body.indexOf(': '); index !== -1; index = body.indexOf(': ', index + 1)) {
    const match = matchClashUnavailableReason(body.slice(index + 2).trim())
    if (match) return { node: body.slice(0, index).trim(), reason: t(match.key, match.params) }
  }
  return { node: '', reason: body }
}

/** Account platform -> the platform reachability check that matters for it. */
export function clashCheckPlatformFor(platform: string | null | undefined): ClashPlatform | null {
  switch (platform) {
    case 'anthropic':
      return 'anthropic'
    case 'openai':
      return 'openai'
    case 'gemini':
    case 'antigravity':
      return 'gemini'
    case 'grok':
      return 'grok'
    default:
      return null
  }
}

export function isBlockingPlatformCheck(result: ClashPlatformCheckResult | '' | null | undefined): boolean {
  return result === 'fail' || result === 'challenge'
}

/** Platform check verdict for an exit ("" when the platform has no check or it was not run). */
export function exitPlatformCheck(
  exit: Pick<ClashExitOption, 'platform_checks'>,
  platform: string | null | undefined
): ClashPlatformCheckResult | '' {
  const target = clashCheckPlatformFor(platform)
  if (!target) return ''
  return exit.platform_checks?.results?.[target] ?? ''
}

export function findClashExit(list: ClashExitList | null | undefined, proxyId: number | null | undefined) {
  if (!list || proxyId == null) return undefined
  return list.exits.find((exit) => exit.proxy_id === proxyId)
}

// ==================== Node traffic ====================

const TRAFFIC_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']

/**
 * Compact 1024-based byte count for traffic figures ("0 B", "12.5 MB", "1.25 GB").
 * Kept free of '@/utils/format', which initializes i18n on import.
 */
export function formatTrafficBytes(bytes: number | null | undefined): string {
  const value = Math.round(Number(bytes))
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  let unit = 0
  let scaled = value
  while (scaled >= 1024 && unit < TRAFFIC_UNITS.length - 1) {
    scaled /= 1024
    unit++
  }
  return `${parseFloat(scaled.toFixed(unit >= 3 ? 2 : 1))} ${TRAFFIC_UNITS[unit]}`
}

export function formatTrafficRate(bytesPerSecond: number | null | undefined): string {
  return `${formatTrafficBytes(bytesPerSecond)}/s`
}

export function clashTrafficTotal(traffic: ClashNodeTraffic | null | undefined): number {
  return (traffic?.upload_bytes ?? 0) + (traffic?.download_bytes ?? 0)
}

export function clashTrafficToday(traffic: ClashNodeTraffic | null | undefined): number {
  return (traffic?.today_upload_bytes ?? 0) + (traffic?.today_download_bytes ?? 0)
}

/** Daily totals of the trend window, oldest first. */
export function clashTrafficTrend(traffic: ClashNodeTraffic | null | undefined): number[] {
  return (traffic?.daily ?? []).map((day) => day.upload_bytes + day.download_bytes)
}

export function countryFlagUrl(code: string | null | undefined): string {
  const normalized = (code || '').trim().toLowerCase()
  if (!/^[a-z]{2}$/.test(normalized)) return ''
  return `https://unpkg.com/flag-icons/flags/4x3/${normalized}.svg`
}

// ==================== API error messages ====================

const CLASH_ERROR_KEYS: Record<string, string> = {
  CLASH_EXIT_OCCUPIED: 'admin.clash.errors.exitOccupied',
  CLASH_EXIT_UNAVAILABLE: 'admin.clash.errors.exitUnavailable',
  CLASH_EXIT_UNPROBED: 'admin.clash.errors.exitUnprobed',
  CLASH_EXIT_RELAY_UNSUPPORTED: 'admin.clash.errors.relayUnsupported',
  CLASH_EXIT_BULK_UNSUPPORTED: 'admin.clash.errors.bulkUnsupported',
  CLASH_MANAGED_PROXY_READONLY: 'admin.clash.errors.managedReadonly',
  CLASH_PROXY_PORT_RESERVED: 'admin.clash.errors.portReserved',
  CLASH_ENCRYPTION_KEY_REQUIRED: 'admin.clash.errors.encryptionKeyRequired',
  CLASH_PROFILE_IN_USE: 'admin.clash.errors.profileInUse',
  CLASH_PROFILE_INVALID: 'admin.clash.errors.profileInvalid',
  CLASH_PROFILE_NOT_FOUND: 'admin.clash.errors.profileNotFound',
  CLASH_NODE_NOT_FOUND: 'admin.clash.errors.nodeNotFound',
  CLASH_FETCH_FAILED: 'admin.clash.errors.fetchFailed',
  CLASH_POOL_DISABLED: 'admin.clash.errors.poolDisabled',
  CLASH_RUNTIME_UNAVAILABLE: 'admin.clash.errors.runtimeUnavailable',
  CLASH_TOO_MANY_NODES: 'admin.clash.errors.tooManyNodes'
}

/** Error codes whose message includes the affected account names (metadata.accounts). */
const CLASH_ERRORS_WITH_ACCOUNTS = new Set(['CLASH_EXIT_OCCUPIED', 'CLASH_PROFILE_IN_USE'])

export function clashErrorCode(err: unknown): string | null {
  const code = extractApiErrorCode(err)
  return code && code.startsWith('CLASH_') ? code : null
}

/** Account names carried by CLASH_EXIT_OCCUPIED / CLASH_PROFILE_IN_USE ("a, b, c"). */
export function clashErrorAccounts(err: unknown): string {
  const raw = extractApiErrorMetadata(err)?.accounts
  return typeof raw === 'string' ? raw.trim() : ''
}

/**
 * Friendly, localized text for CLASH_* API errors; null for anything else so callers
 * keep their existing fallback chain.
 */
export function clashErrorMessage(err: unknown, t: TranslateFn): string | null {
  const code = clashErrorCode(err)
  if (!code) return null
  const key = CLASH_ERROR_KEYS[code]
  if (!key) return null
  const message = typeof (err as { message?: unknown })?.message === 'string'
    ? ((err as { message: string }).message).trim()
    : ''
  if (CLASH_ERRORS_WITH_ACCOUNTS.has(code)) {
    const accounts = clashErrorAccounts(err)
    return accounts ? t(`${key}WithAccounts`, { accounts }) : t(key)
  }
  return t(key, { message: message || '-' })
}

// ==================== Batch creation guard ====================

/**
 * Number of accounts a Codex session / access-token import would create. Mirrors the
 * server-side parser closely enough to tell "one" from "several": a JSON document
 * (arrays flattened), a stream of JSON values, or one token/JSON object per line.
 */
export function estimateCodexImportCount(content: string): number {
  const trimmed = content.trim()
  if (!trimmed) return 0
  const flatten = (value: unknown): number =>
    Array.isArray(value) ? value.reduce<number>((sum, item) => sum + flatten(item), 0) : 1
  if (trimmed.startsWith('{') || trimmed.startsWith('[')) {
    try {
      return flatten(JSON.parse(trimmed))
    } catch {
      const values = countTopLevelJsonValues(trimmed)
      if (values !== null) return values
    }
  }
  return trimmed.split('\n').filter((line) => line.trim()).length
}

function countTopLevelJsonValues(text: string): number | null {
  let depth = 0
  let count = 0
  let inString = false
  let escaped = false
  for (const ch of text) {
    if (inString) {
      if (escaped) escaped = false
      else if (ch === '\\') escaped = true
      else if (ch === '"') inString = false
      continue
    }
    if (ch === '"') {
      inString = true
    } else if (ch === '{' || ch === '[') {
      if (depth === 0) count++
      depth++
    } else if (ch === '}' || ch === ']') {
      depth--
      if (depth < 0) return null
    }
  }
  return depth === 0 && count > 0 ? count : null
}
