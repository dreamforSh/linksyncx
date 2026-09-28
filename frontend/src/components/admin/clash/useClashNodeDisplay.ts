/**
 * Labels and badge styles for Clash nodes, shared by the node table and the
 * node cards so both views read the same.
 */

import { useI18n } from 'vue-i18n'
import type { ClashNode, ClashNodeStatus, ClashPlatform, ClashPlatformCheckResult } from '@/types'
import { formatDateTime } from '@/utils/format'

export const CLASH_CHECK_PLATFORMS: ClashPlatform[] = ['openai', 'anthropic', 'gemini', 'grok']

export const CLASH_PLATFORM_LABELS: Record<ClashPlatform, string> = {
  openai: 'OpenAI',
  anthropic: 'Anthropic',
  gemini: 'Gemini',
  grok: 'Grok'
}

export const CLASH_TYPE_BADGE_CLASS =
  'inline-flex flex-shrink-0 items-center rounded px-1.5 py-px font-mono text-[10px] font-medium uppercase ' +
  'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300'

/** Marks hidden nodes (listed only by the "hidden" and "all" visibility filters). */
export const CLASH_HIDDEN_BADGE_CLASS =
  'inline-flex flex-shrink-0 items-center gap-0.5 rounded px-1.5 py-px text-[10px] font-medium ring-1 ring-inset ' +
  'bg-slate-100 text-slate-600 ring-slate-400/30 dark:bg-dark-700 dark:text-dark-300 dark:ring-dark-500/40'

const STATUS_PILL_BASE = 'inline-flex items-center gap-1.5 whitespace-nowrap rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset'
const PLATFORM_BADGE_BASE = 'inline-flex items-center rounded px-1.5 py-px text-[11px] font-medium ring-1 ring-inset'

const TONE = {
  success: 'bg-emerald-50 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-500/10 dark:text-emerald-300 dark:ring-emerald-400/20',
  warning: 'bg-amber-50 text-amber-700 ring-amber-600/20 dark:bg-amber-500/10 dark:text-amber-300 dark:ring-amber-400/20',
  danger: 'bg-red-50 text-red-700 ring-red-600/20 dark:bg-red-500/10 dark:text-red-300 dark:ring-red-400/20',
  muted: 'bg-gray-100 text-gray-600 ring-gray-500/20 dark:bg-dark-700 dark:text-dark-300 dark:ring-dark-500/30',
  unchecked: 'bg-gray-50 text-gray-400 ring-gray-400/20 dark:bg-dark-800 dark:text-dark-500 dark:ring-dark-600/40'
}

export function useClashNodeDisplay() {
  const { t } = useI18n()

  const statusLabel = (status: ClashNodeStatus) => {
    switch (status) {
      case 'active':
        return t('admin.clash.nodeStatus.active')
      case 'missing':
        return t('admin.clash.nodeStatus.missing')
      case 'disabled':
        return t('admin.clash.nodeStatus.disabled')
      default:
        return t('admin.clash.nodeStatus.invalid')
    }
  }

  const statusPillClass = (status: ClashNodeStatus) => {
    switch (status) {
      case 'active':
        return `${STATUS_PILL_BASE} ${TONE.success}`
      case 'disabled':
        return `${STATUS_PILL_BASE} ${TONE.warning}`
      case 'invalid':
        return `${STATUS_PILL_BASE} ${TONE.danger}`
      default:
        return `${STATUS_PILL_BASE} ${TONE.muted}`
    }
  }

  const statusDotClass = (status: ClashNodeStatus) => {
    switch (status) {
      case 'active':
        return 'bg-emerald-500'
      case 'disabled':
        return 'bg-amber-500'
      case 'invalid':
        return 'bg-red-500'
      default:
        return 'bg-gray-400'
    }
  }

  const healthLabel = (node: ClashNode) => {
    if (node.health_status === 'healthy') {
      return typeof node.latency_ms === 'number' ? `${node.latency_ms} ms` : t('admin.clash.health.healthy')
    }
    return node.health_status === 'unhealthy' ? t('admin.clash.health.unhealthy') : t('admin.clash.health.unknown')
  }

  const healthDotClass = (node: ClashNode) => {
    if (node.health_status === 'unhealthy') return 'bg-red-500'
    if (node.health_status !== 'healthy') return 'bg-gray-400'
    const latency = node.latency_ms ?? 0
    if (latency >= 1000) return 'bg-red-500'
    if (latency >= 300) return 'bg-amber-500'
    return 'bg-emerald-500'
  }

  const healthTextClass = (node: ClashNode) => {
    if (node.health_status === 'unhealthy') return 'font-medium text-red-600 dark:text-red-400'
    if (node.health_status === 'healthy') return 'font-medium tabular-nums text-gray-800 dark:text-gray-100'
    return 'text-gray-500 dark:text-dark-400'
  }

  const healthTitle = (node: ClashNode) => {
    const parts: string[] = []
    if (node.last_checked_at) parts.push(t('admin.clash.nodes.checkedAt', { time: formatDateTime(node.last_checked_at) }))
    if (node.last_check_error) parts.push(node.last_check_error)
    return parts.join('\n') || undefined
  }

  const exitLocation = (node: ClashNode) =>
    [node.exit_country, node.exit_region, node.exit_city]
      .filter(Boolean)
      .filter((value, index, all) => all.indexOf(value) === index)
      .join(' · ')

  const platformBadgeClass = (result: ClashPlatformCheckResult | undefined) => {
    switch (result) {
      case 'pass':
        return `${PLATFORM_BADGE_BASE} ${TONE.success}`
      case 'warn':
        return `${PLATFORM_BADGE_BASE} ${TONE.warning}`
      case 'fail':
      case 'challenge':
        return `${PLATFORM_BADGE_BASE} ${TONE.danger}`
      default:
        return `${PLATFORM_BADGE_BASE} ${TONE.unchecked}`
    }
  }

  const platformResultLabel = (result: ClashPlatformCheckResult | undefined) => {
    switch (result) {
      case 'pass':
        return t('admin.clash.platformResult.pass')
      case 'warn':
        return t('admin.clash.platformResult.warn')
      case 'fail':
        return t('admin.clash.platformResult.fail')
      case 'challenge':
        return t('admin.clash.platformResult.challenge')
      default:
        return t('admin.clash.platformResult.unchecked')
    }
  }

  const platformTitle = (node: ClashNode, platform: ClashPlatform) => {
    const label = `${CLASH_PLATFORM_LABELS[platform]}: ${platformResultLabel(node.platform_checks.results[platform])}`
    const checkedAt = node.platform_checks.checked_at
    return checkedAt ? `${label}\n${t('admin.clash.nodes.checkedAt', { time: formatDateTime(checkedAt) })}` : label
  }

  const accountNames = (node: ClashNode) => node.accounts.map((account) => account.name).join(', ')

  return {
    statusLabel,
    statusPillClass,
    statusDotClass,
    healthLabel,
    healthDotClass,
    healthTextClass,
    healthTitle,
    exitLocation,
    platformBadgeClass,
    platformResultLabel,
    platformTitle,
    accountNames
  }
}
