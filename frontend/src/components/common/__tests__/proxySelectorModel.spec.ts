import { beforeEach, describe, expect, it } from 'vitest'
import type { ClashExitOption } from '@/types'
import {
  CLASH_EXIT_FILTERS_STORAGE_KEY,
  buildClashExitRows,
  clashCountryOptions,
  clashProfileOptions,
  compareClashExitViews,
  computeDropdownPlacement,
  defaultClashExitFilters,
  effectiveClashExitFilters,
  exitLatencyTone,
  matchesSearch,
  naturalCompare,
  readClashExitFilters,
  searchTokens,
  writeClashExitFilters,
  type ClashExitFilters,
  type ClashExitView,
  type ProxySelectorRow
} from '../proxySelectorModel'

let nextId = 1
function exit(overrides: Partial<ClashExitOption> = {}): ClashExitOption {
  const id = overrides.proxy_id ?? nextId++
  return {
    proxy_id: id,
    node_id: id,
    profile_id: 1,
    profile_name: 'Airport A',
    node_name: `Node ${id}`,
    type: 'vmess',
    status: 'active',
    health_status: 'healthy',
    latency_ms: 100,
    exit_ip: `10.0.0.${id}`,
    exit_country: 'Japan',
    exit_country_code: 'JP',
    exit_city: 'Tokyo',
    exit_status: 'ok',
    exit_pending_ip: '',
    exit_key: `ip:10.0.0.${id}`,
    available: true,
    unavailable_reason: '',
    occupants: [],
    platform_checks: { checked_at: null, results: {} },
    ...overrides
  }
}

function view(exitOverrides: Partial<ClashExitOption> = {}, viewOverrides: Partial<ClashExitView> = {}): ClashExitView {
  return {
    exit: exit(exitOverrides),
    others: [],
    full: false,
    unavailableText: '',
    unprobedBlocked: false,
    blockReason: '',
    disabled: false,
    occupancyLabel: 'idle',
    platformWarning: false,
    location: '',
    ...viewOverrides
  }
}

const occupied = (count: number) =>
  Array.from({ length: count }, (_, index) => ({ id: 900 + index, name: `acc-${index}`, platform: 'openai', is_shadow: false }))

const rowsOf = (
  views: ClashExitView[],
  options: Partial<{ tokens: string[]; filters: Partial<ClashExitFilters>; platform: string; collapsed: number[]; expanded: boolean }> = {}
) =>
  buildClashExitRows({
    views,
    tokens: options.tokens ?? [],
    filters: { ...defaultClashExitFilters(), ...options.filters },
    platform: options.platform,
    collapsedProfiles: new Set(options.collapsed ?? []),
    unavailableExpanded: options.expanded ?? false
  })

const describeRows = (rows: ProxySelectorRow[]) =>
  rows.map((row) => {
    if (row.kind === 'group') return `[${row.group.profileName}]`
    if (row.kind === 'unavailable') return `<unavailable ${row.count}>`
    if (row.kind === 'exit') return row.view.exit.node_name
    return row.kind
  })

describe('proxySelectorModel — search', () => {
  it('trims the query and requires every word to match some field', () => {
    expect(searchTokens('  HK   01 ')).toEqual(['hk', '01'])
    expect(searchTokens('   ')).toEqual([])
    expect(matchesSearch(['hk', 'tokyo'], ['HK 01', 'Tokyo'])).toBe(true)
    expect(matchesSearch(['hk', 'osaka'], ['HK 01', 'Tokyo'])).toBe(false)
    expect(matchesSearch([], [null, undefined])).toBe(true)
  })

  it('matches exits on node, subscription, IP, country name/code, city and type', () => {
    const exits = [
      view({ proxy_id: 11, node_name: 'HK 01', profile_name: 'Airport A', exit_ip: '1.1.1.1', exit_country: 'Hong Kong', exit_country_code: 'HK', exit_city: 'Kowloon', type: 'trojan' }),
      view({ proxy_id: 12, node_name: 'US 01', profile_id: 2, profile_name: 'Backup B', exit_ip: '2.2.2.2', exit_country: 'United States', exit_country_code: 'US', exit_city: 'San Jose', type: 'vmess' })
    ]
    const names = (query: string) =>
      describeRows(rowsOf(exits, { tokens: searchTokens(query) }).rows).filter((name) => !name.startsWith('['))
    expect(names('backup us')).toEqual(['US 01'])
    expect(names('trojan kowloon')).toEqual(['HK 01'])
    expect(names('hk san')).toEqual([])
    expect(names('2.2.2 vmess')).toEqual(['US 01'])
  })
})

describe('proxySelectorModel — sorting', () => {
  it('orders names naturally', () => {
    expect(['HK 10', 'HK 2', 'HK 1'].sort(naturalCompare)).toEqual(['HK 1', 'HK 2', 'HK 10'])
  })

  it('sorts by latency: measured healthy exits first, then healthy, unchecked and unhealthy ones', () => {
    const views = [
      view({ node_name: 'down', health_status: 'unhealthy', latency_ms: 5 }),
      view({ node_name: 'unchecked', health_status: 'unknown', latency_ms: null }),
      view({ node_name: 'healthy-no-latency', latency_ms: null }),
      view({ node_name: 'slow', latency_ms: 900 }),
      view({ node_name: 'fast', latency_ms: 40 })
    ]
    expect(views.sort((a, b) => compareClashExitViews(a, b, 'latency')).map((item) => item.exit.node_name)).toEqual([
      'fast',
      'slow',
      'healthy-no-latency',
      'unchecked',
      'down'
    ])
  })

  it('sorts idle exits first and falls back to the natural name order', () => {
    const views = [
      view({ node_name: 'HK 10' }, { others: occupied(1) }),
      view({ node_name: 'HK 9' }),
      view({ node_name: 'HK 2' }, { others: occupied(2) }),
      view({ node_name: 'HK 1' })
    ]
    expect(views.sort((a, b) => compareClashExitViews(a, b, 'idle')).map((item) => item.exit.node_name)).toEqual([
      'HK 1',
      'HK 9',
      'HK 10',
      'HK 2'
    ])
  })

  it('colours latency with the Clash page thresholds', () => {
    expect(exitLatencyTone({ health_status: 'healthy', latency_ms: 299 })).toBe('good')
    expect(exitLatencyTone({ health_status: 'healthy', latency_ms: 300 })).toBe('fair')
    expect(exitLatencyTone({ health_status: 'healthy', latency_ms: 999 })).toBe('fair')
    expect(exitLatencyTone({ health_status: 'healthy', latency_ms: 1000 })).toBe('poor')
    expect(exitLatencyTone({ health_status: 'healthy', latency_ms: null })).toBe('good')
    expect(exitLatencyTone({ health_status: 'unhealthy', latency_ms: 20 })).toBe('down')
    expect(exitLatencyTone({ health_status: 'unknown', latency_ms: null })).toBe('unknown')
  })
})

describe('proxySelectorModel — rows', () => {
  it('groups pickable exits by subscription in natural order and folds blocked ones at the bottom', () => {
    const views = [
      view({ node_name: 'HK 10', profile_id: 2, profile_name: 'Backup 10' }),
      view({ node_name: 'HK 2', profile_id: 2, profile_name: 'Backup 10' }),
      view({ node_name: 'JP 1', profile_id: 3, profile_name: 'Backup 9' }),
      view({ node_name: 'JP 2', profile_id: 3, profile_name: 'Backup 9' }, { disabled: true, blockReason: 'full' }),
      view({ node_name: 'HK 1', profile_id: 2, profile_name: 'Backup 10' }, { disabled: true, blockReason: 'down' })
    ]
    const collapsed = rowsOf(views)
    expect(describeRows(collapsed.rows)).toEqual(['[Backup 9]', 'JP 1', '[Backup 10]', 'HK 2', 'HK 10', '<unavailable 2>'])
    expect(collapsed.matched).toBe(5)

    const expanded = rowsOf(views, { expanded: true })
    expect(describeRows(expanded.rows).slice(-3)).toEqual(['<unavailable 2>', 'JP 2', 'HK 1'])
    expect(expanded.rows.filter((row) => row.kind === 'exit' && row.unavailable)).toHaveLength(2)
  })

  it('counts usable, idle and total exits per subscription regardless of the filters', () => {
    const views = [
      view({ node_name: 'a' }),
      view({ node_name: 'b' }, { others: occupied(1) }),
      view({ node_name: 'c' }, { disabled: true, blockReason: 'down' })
    ]
    const header = rowsOf(views, { filters: { onlyIdle: true } }).rows[0]
    expect(header.kind === 'group' && header.group).toMatchObject({ usable: 2, idle: 1, total: 3 })
  })

  it('hides collapsed subscriptions but keeps their header', () => {
    const views = [view({ node_name: 'a' }), view({ node_name: 'b', profile_id: 2, profile_name: 'B' })]
    const { rows } = rowsOf(views, { collapsed: [1] })
    expect(describeRows(rows)).toEqual(['[Airport A]', '[B]', 'b'])
    expect(rows[0].kind === 'group' && rows[0].collapsed).toBe(true)
  })

  it('applies the quick filters', () => {
    const views = [
      view({ node_name: 'idle-jp' }),
      view({ node_name: 'busy-jp' }, { others: occupied(1) }),
      view({ node_name: 'us', exit_country: 'United States', exit_country_code: 'US' }),
      view({ node_name: 'blocked' }, { disabled: true, blockReason: 'down' }),
      view({ node_name: 'b-sub', profile_id: 2, profile_name: 'B' }),
      view({ node_name: 'anthropic-pass', platform_checks: { checked_at: null, results: { anthropic: 'pass' } } })
    ]
    const names = (filters: Partial<ClashExitFilters>, platform = 'anthropic') =>
      describeRows(rowsOf(views, { filters, platform }).rows).filter((name) => !name.startsWith('['))

    expect(names({ onlyAvailable: true })).not.toContain('<unavailable 1>')
    expect(names({ onlyAvailable: true })).not.toContain('blocked')
    expect(names({ onlyIdle: true })).not.toContain('busy-jp')
    expect(names({ country: 'US' })).toEqual(['us'])
    expect(names({ profileId: 2 })).toEqual(['b-sub'])
    expect(names({ platformPass: true })).toEqual(['anthropic-pass'])
  })

  it('builds country and subscription options from the exits present', () => {
    const exits = [
      exit({ exit_country: 'Japan', exit_country_code: 'jp' }),
      exit({ exit_country: '', exit_country_code: 'JP' }),
      exit({ exit_country: 'Hong Kong', exit_country_code: 'HK', profile_id: 2, profile_name: 'Backup 10' }),
      exit({ exit_country: '', exit_country_code: '', exit_ip: '' }),
      exit({ profile_id: 3, profile_name: 'Backup 9' })
    ]
    expect(clashCountryOptions(exits)).toEqual([
      { key: 'HK', code: 'HK', label: 'Hong Kong', count: 1 },
      { key: 'JP', code: 'JP', label: 'Japan', count: 3 }
    ])
    expect(clashProfileOptions(exits).map((profile) => profile.name)).toEqual(['Airport A', 'Backup 9', 'Backup 10'])
  })

  it('ignores saved filters the current exits cannot use', () => {
    const exits = [
      exit({ exit_country_code: 'JP' }),
      exit({ exit_country_code: 'HK', exit_country: 'Hong Kong', profile_id: 2, profile_name: 'B' })
    ]
    const context = { countries: clashCountryOptions(exits), profiles: clashProfileOptions(exits) }
    const saved = { ...defaultClashExitFilters(), country: 'US', profileId: 7, platformPass: true }
    expect(effectiveClashExitFilters(saved, { ...context, platform: 'sora' })).toMatchObject({
      country: '',
      profileId: null,
      platformPass: false
    })
    expect(effectiveClashExitFilters({ ...saved, country: 'HK', profileId: 2 }, { ...context, platform: 'antigravity' })).toMatchObject({
      country: 'HK',
      profileId: 2,
      platformPass: true
    })
  })
})

describe('proxySelectorModel — persistence', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('round-trips the filters and sort order', () => {
    const filters: ClashExitFilters = { onlyAvailable: true, onlyIdle: true, country: 'JP', profileId: 3, platformPass: true, sort: 'latency' }
    writeClashExitFilters(filters)
    expect(readClashExitFilters()).toEqual(filters)
  })

  it('falls back to the defaults for missing or malformed values', () => {
    expect(readClashExitFilters()).toEqual(defaultClashExitFilters())
    localStorage.setItem(CLASH_EXIT_FILTERS_STORAGE_KEY, '{not json')
    expect(readClashExitFilters()).toEqual(defaultClashExitFilters())
    localStorage.setItem(
      CLASH_EXIT_FILTERS_STORAGE_KEY,
      JSON.stringify({ onlyAvailable: 'yes', country: 5, profileId: -1, sort: 'random', onlyIdle: true })
    )
    expect(readClashExitFilters()).toEqual({ ...defaultClashExitFilters(), onlyIdle: true })
  })
})

describe('proxySelectorModel — dropdown placement', () => {
  const viewport = { width: 1440, height: 900 }
  const trigger = (top: number, left = 300, width = 800) => ({ top, bottom: top + 42, left, width })

  it('opens below the trigger when the panel fits', () => {
    expect(computeDropdownPlacement({ trigger: trigger(200), viewport, panelHeight: 560 })).toEqual({
      placement: 'bottom',
      top: 246,
      left: 300,
      width: 800,
      maxHeight: 646
    })
  })

  it('flips above the trigger when there is not enough room below', () => {
    expect(computeDropdownPlacement({ trigger: trigger(700), viewport, panelHeight: 560 })).toEqual({
      placement: 'top',
      bottom: 204,
      left: 300,
      width: 800,
      maxHeight: 688
    })
  })

  it('stays below and clamps its height when the room below is at least as large', () => {
    const placement = computeDropdownPlacement({ trigger: trigger(400), viewport, panelHeight: 800 })
    expect(placement.placement).toBe('bottom')
    expect(placement.maxHeight).toBe(446)
  })

  it('keeps the panel inside the viewport horizontally and honours the minimum width', () => {
    const narrow = computeDropdownPlacement({ trigger: trigger(100, 1300, 120), viewport, panelHeight: 200, minWidth: 360 })
    expect(narrow.width).toBe(360)
    expect(narrow.left).toBe(1440 - 8 - 360)
    const tiny = computeDropdownPlacement({ trigger: trigger(100, 0, 500), viewport: { width: 320, height: 600 }, panelHeight: 200 })
    expect(tiny).toMatchObject({ left: 8, width: 304 })
  })
})
