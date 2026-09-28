/**
 * Filtering, sorting and placement logic of the account proxy selector (ProxySelector.vue),
 * kept free of Vue so it can be unit tested.
 */

import type { ClashBoundAccount, ClashExitOption, ClashPlatform, Proxy } from '@/types'
import { clashCheckPlatformFor, exitPlatformCheck } from '@/utils/clash'

/** A Clash exit as the selector presents it for the account being edited. */
export interface ClashExitView {
  exit: ClashExitOption
  /** Accounts other than the edited one sharing this exit IP. */
  others: ClashBoundAccount[]
  full: boolean
  /** Unavailable / unprobed explanation (shown under the exit). */
  unavailableText: string
  /** Blocked only because the exit IP was never probed. */
  unprobedBlocked: boolean
  /** Why the exit cannot be picked (unavailable, unprobed or full). */
  blockReason: string
  disabled: boolean
  occupancyLabel: string
  platformWarning: boolean
  location: string
}

/** Names of the platforms with a reachability check (brand names, not translated). */
export const CLASH_CHECK_PLATFORM_LABELS: Record<ClashPlatform, string> = {
  openai: 'OpenAI',
  anthropic: 'Anthropic',
  gemini: 'Gemini',
  grok: 'Grok'
}

// ==================== Search ====================

/** Lower-cased words of a search query. */
export function searchTokens(query: string): string[] {
  return query.trim().toLowerCase().split(/\s+/).filter(Boolean)
}

/** True when every word is found in at least one of the fields. */
export function matchesSearch(tokens: string[], fields: Array<string | null | undefined>): boolean {
  if (tokens.length === 0) return true
  const values = fields.map((field) => (field || '').toLowerCase())
  return tokens.every((token) => values.some((value) => value.includes(token)))
}

export const proxySearchFields = (proxy: Proxy) => [proxy.name, proxy.host, proxy.protocol]

export const exitSearchFields = (exit: ClashExitOption) => [
  exit.node_name,
  exit.profile_name,
  exit.exit_ip,
  exit.exit_country,
  exit.exit_country_code,
  exit.exit_city,
  exit.type
]

// ==================== Sorting ====================

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' })

/** Natural order ("HK 2" before "HK 10") instead of the server's plain string order. */
export function naturalCompare(a: string | null | undefined, b: string | null | undefined): number {
  return collator.compare(a || '', b || '')
}

export type ClashExitSort = 'default' | 'latency' | 'idle'

export const CLASH_EXIT_SORTS: ClashExitSort[] = ['default', 'latency', 'idle']

/** Latency order buckets: healthy with a latency, healthy without one, not checked, unhealthy. */
export function exitHealthRank(exit: Pick<ClashExitOption, 'health_status' | 'latency_ms'>): number {
  if (exit.health_status === 'healthy') return typeof exit.latency_ms === 'number' ? 0 : 1
  return exit.health_status === 'unhealthy' ? 3 : 2
}

export type ClashLatencyTone = 'good' | 'fair' | 'poor' | 'down' | 'unknown'

/** Health chip tone with the Clash page thresholds: amber from 300 ms, red from 1000 ms. */
export function exitLatencyTone(exit: Pick<ClashExitOption, 'health_status' | 'latency_ms'>): ClashLatencyTone {
  if (exit.health_status === 'unhealthy') return 'down'
  if (exit.health_status !== 'healthy') return 'unknown'
  const latency = exit.latency_ms ?? 0
  if (latency >= 1000) return 'poor'
  if (latency >= 300) return 'fair'
  return 'good'
}

/** Order of the exits inside one subscription; ties fall back to the natural node name. */
export function compareClashExitViews(a: ClashExitView, b: ClashExitView, sort: ClashExitSort): number {
  if (sort === 'latency') {
    const rankA = exitHealthRank(a.exit)
    const byRank = rankA - exitHealthRank(b.exit)
    if (byRank !== 0) return byRank
    if (rankA === 0) {
      const byLatency = (a.exit.latency_ms ?? 0) - (b.exit.latency_ms ?? 0)
      if (byLatency !== 0) return byLatency
    }
  } else if (sort === 'idle') {
    const byOccupancy = a.others.length - b.others.length
    if (byOccupancy !== 0) return byOccupancy
  }
  return naturalCompare(a.exit.node_name, b.exit.node_name) || a.exit.proxy_id - b.exit.proxy_id
}

// ==================== Filters ====================

export interface ClashExitFilters {
  /** Hide exits that cannot be picked (the current and the original binding never are). */
  onlyAvailable: boolean
  /** Hide exits used by other accounts. */
  onlyIdle: boolean
  /** Country key (see exitCountryKey); '' = every country. */
  country: string
  /** Subscription id; null = every subscription. */
  profileId: number | null
  /** Only exits that passed the reachability check of the account's platform. */
  platformPass: boolean
  sort: ClashExitSort
}

export function defaultClashExitFilters(): ClashExitFilters {
  return { onlyAvailable: false, onlyIdle: false, country: '', profileId: null, platformPass: false, sort: 'default' }
}

/** Whether any filter narrows the list (the sort order is not a filter). */
export function hasActiveClashExitFilters(filters: ClashExitFilters): boolean {
  return filters.onlyAvailable || filters.onlyIdle || filters.country !== '' || filters.profileId !== null || filters.platformPass
}

/** Country filter key: the upper-case country code, else the country name; '' before the exit is probed. */
export function exitCountryKey(exit: Pick<ClashExitOption, 'exit_country' | 'exit_country_code'>): string {
  return (exit.exit_country_code || '').trim().toUpperCase() || (exit.exit_country || '').trim()
}

export interface ClashCountryOption {
  key: string
  /** Country code for the flag ('' when only the name is known). */
  code: string
  label: string
  count: number
}

export function clashCountryOptions(exits: ClashExitOption[]): ClashCountryOption[] {
  const byKey = new Map<string, ClashCountryOption>()
  for (const exit of exits) {
    const key = exitCountryKey(exit)
    if (!key) continue
    const name = (exit.exit_country || '').trim()
    const option = byKey.get(key)
    if (option) {
      option.count++
      if (option.label === key && name) option.label = name
    } else {
      byKey.set(key, { key, code: (exit.exit_country_code || '').trim().toUpperCase(), label: name || key, count: 1 })
    }
  }
  return [...byKey.values()].sort((a, b) => naturalCompare(a.label, b.label))
}

export interface ClashProfileOption {
  id: number
  name: string
  count: number
}

export function clashProfileOptions(exits: ClashExitOption[]): ClashProfileOption[] {
  const byId = new Map<number, ClashProfileOption>()
  for (const exit of exits) {
    const option = byId.get(exit.profile_id)
    if (option) option.count++
    else byId.set(exit.profile_id, { id: exit.profile_id, name: exit.profile_name || `#${exit.profile_id}`, count: 1 })
  }
  return [...byId.values()].sort((a, b) => naturalCompare(a.name, b.name) || a.id - b.id)
}

/**
 * The saved filters narrowed to what the current exit list supports: a country or subscription
 * that is gone (or the only one left) and a platform without a reachability check are ignored.
 */
export function effectiveClashExitFilters(
  filters: ClashExitFilters,
  context: { countries: ClashCountryOption[]; profiles: ClashProfileOption[]; platform?: string | null }
): ClashExitFilters {
  const { countries, profiles, platform } = context
  return {
    ...filters,
    country: countries.length > 1 && countries.some((country) => country.key === filters.country) ? filters.country : '',
    profileId: profiles.length > 1 && profiles.some((profile) => profile.id === filters.profileId) ? filters.profileId : null,
    platformPass: filters.platformPass && clashCheckPlatformFor(platform) !== null
  }
}

export function clashExitPassesFilters(view: ClashExitView, filters: ClashExitFilters, platform?: string | null): boolean {
  const { exit } = view
  if (filters.onlyAvailable && view.disabled) return false
  if (filters.onlyIdle && view.others.length > 0) return false
  if (filters.country && exitCountryKey(exit) !== filters.country) return false
  if (filters.profileId !== null && exit.profile_id !== filters.profileId) return false
  if (filters.platformPass && exitPlatformCheck(exit, platform) !== 'pass') return false
  return true
}

/** localStorage key of the filter/sort choices, shared by every account editor. */
export const CLASH_EXIT_FILTERS_STORAGE_KEY = 'proxy-selector-clash-filters'

export function readClashExitFilters(): ClashExitFilters {
  const filters = defaultClashExitFilters()
  try {
    const raw = localStorage.getItem(CLASH_EXIT_FILTERS_STORAGE_KEY)
    const saved = raw ? (JSON.parse(raw) as Partial<Record<keyof ClashExitFilters, unknown>> | null) : null
    if (!saved || typeof saved !== 'object') return filters
    if (typeof saved.onlyAvailable === 'boolean') filters.onlyAvailable = saved.onlyAvailable
    if (typeof saved.onlyIdle === 'boolean') filters.onlyIdle = saved.onlyIdle
    if (typeof saved.platformPass === 'boolean') filters.platformPass = saved.platformPass
    if (typeof saved.country === 'string') filters.country = saved.country
    if (typeof saved.profileId === 'number' && Number.isInteger(saved.profileId) && saved.profileId > 0) {
      filters.profileId = saved.profileId
    }
    if (CLASH_EXIT_SORTS.includes(saved.sort as ClashExitSort)) filters.sort = saved.sort as ClashExitSort
  } catch {
    // Unreadable or blocked storage: start from the defaults.
  }
  return filters
}

export function writeClashExitFilters(filters: ClashExitFilters): void {
  try {
    localStorage.setItem(CLASH_EXIT_FILTERS_STORAGE_KEY, JSON.stringify(filters))
  } catch {
    // Private mode: the choice just isn't remembered.
  }
}

// ==================== Option rows ====================

export interface ClashExitGroup {
  profileId: number
  profileName: string
  /** Exits the account can pick. */
  usable: number
  /** Pickable exits no other account uses. */
  idle: number
  total: number
}

/** One line of the (virtualized) option list. */
export type ProxySelectorRow =
  | { kind: 'none'; key: string }
  | { kind: 'proxy'; key: string; proxy: Proxy }
  | { kind: 'group'; key: string; group: ClashExitGroup; collapsed: boolean }
  | { kind: 'exit'; key: string; view: ClashExitView; unavailable: boolean }
  | { kind: 'unavailable'; key: string; count: number; expanded: boolean }

export interface ClashExitRowsInput {
  views: ClashExitView[]
  tokens: string[]
  filters: ClashExitFilters
  platform?: string | null
  collapsedProfiles: ReadonlySet<number>
  unavailableExpanded: boolean
}

/**
 * Pickable exits grouped by subscription (natural subscription order, then the chosen sort),
 * followed by one collapsible section holding the exits that cannot be picked.
 */
export function buildClashExitRows(input: ClashExitRowsInput): { rows: ProxySelectorRow[]; matched: number } {
  const { views, tokens, filters, platform, collapsedProfiles, unavailableExpanded } = input
  // Header counts cover the whole subscription, so they do not change while filtering.
  const groups = new Map<number, ClashExitGroup>()
  for (const view of views) {
    const { profile_id: profileId, profile_name: profileName } = view.exit
    let group = groups.get(profileId)
    if (!group) {
      group = { profileId, profileName: profileName || `#${profileId}`, usable: 0, idle: 0, total: 0 }
      groups.set(profileId, group)
    }
    group.total++
    if (!view.disabled) {
      group.usable++
      if (view.others.length === 0) group.idle++
    }
  }
  const compare = (a: ClashExitView, b: ClashExitView) => {
    if (a.exit.profile_id !== b.exit.profile_id) {
      const groupA = groups.get(a.exit.profile_id)!
      const groupB = groups.get(b.exit.profile_id)!
      return naturalCompare(groupA.profileName, groupB.profileName) || groupA.profileId - groupB.profileId
    }
    return compareClashExitViews(a, b, filters.sort)
  }

  const matching = views.filter(
    (view) => matchesSearch(tokens, exitSearchFields(view.exit)) && clashExitPassesFilters(view, filters, platform)
  )
  const usable = matching.filter((view) => !view.disabled).sort(compare)
  const blocked = matching.filter((view) => view.disabled).sort(compare)

  const rows: ProxySelectorRow[] = []
  let currentProfile: number | null = null
  for (const view of usable) {
    const profileId = view.exit.profile_id
    const collapsed = collapsedProfiles.has(profileId)
    if (profileId !== currentProfile) {
      currentProfile = profileId
      rows.push({ kind: 'group', key: `group-${profileId}`, group: groups.get(profileId)!, collapsed })
    }
    if (!collapsed) rows.push({ kind: 'exit', key: `exit-${view.exit.proxy_id}`, view, unavailable: false })
  }
  if (blocked.length > 0) {
    rows.push({ kind: 'unavailable', key: 'unavailable', count: blocked.length, expanded: unavailableExpanded })
    if (unavailableExpanded) {
      for (const view of blocked) rows.push({ kind: 'exit', key: `exit-${view.exit.proxy_id}`, view, unavailable: true })
    }
  }
  return { rows, matched: matching.length }
}

// ==================== Placement ====================

export interface DropdownPlacementInput {
  trigger: { top: number; bottom: number; left: number; width: number }
  viewport: { width: number; height: number }
  /** Height the panel needs, measured before any clamping. */
  panelHeight: number
  minWidth?: number
}

export interface DropdownPlacement {
  placement: 'top' | 'bottom'
  /** Distance from the viewport top (bottom placement). */
  top?: number
  /** Distance from the viewport bottom (top placement). */
  bottom?: number
  left: number
  width: number
  /** Room on the chosen side; the option list scrolls within it. */
  maxHeight: number
}

const DROPDOWN_GAP = 4
const VIEWPORT_MARGIN = 8
const MIN_PANEL_HEIGHT = 160

/**
 * Places the panel teleported to <body> under the trigger and flips it above when it does not
 * fit below and there is more room above (Select.vue's rule), clamping it to the room it gets.
 */
export function computeDropdownPlacement({ trigger, viewport, panelHeight, minWidth = 200 }: DropdownPlacementInput): DropdownPlacement {
  const spaceBelow = viewport.height - trigger.bottom - DROPDOWN_GAP - VIEWPORT_MARGIN
  const spaceAbove = trigger.top - DROPDOWN_GAP - VIEWPORT_MARGIN
  const placement = panelHeight > spaceBelow && spaceAbove > spaceBelow ? 'top' : 'bottom'
  const maxHeight = Math.max(MIN_PANEL_HEIGHT, Math.floor(placement === 'top' ? spaceAbove : spaceBelow))
  const width = Math.min(Math.max(trigger.width, minWidth), Math.max(0, viewport.width - VIEWPORT_MARGIN * 2))
  const left = Math.max(VIEWPORT_MARGIN, Math.min(trigger.left, viewport.width - VIEWPORT_MARGIN - width))
  return placement === 'top'
    ? { placement, bottom: viewport.height - trigger.top + DROPDOWN_GAP, left, width, maxHeight }
    : { placement, top: trigger.bottom + DROPDOWN_GAP, left, width, maxHeight }
}
