import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import ProxySelector from '../ProxySelector.vue'
import type { ClashExitList, ClashExitOption, Proxy } from '@/types'

const testProxy = vi.hoisted(() => vi.fn())
vi.mock('@/api/admin', () => ({ adminAPI: { proxies: { testProxy } } }))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key} ${JSON.stringify(params)}` : key)
  })
}))
enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
})

const manualProxy = (id: number): Proxy => ({
  id,
  name: `Proxy ${id}`,
  host: `proxy-${id}.example`,
  port: 8080,
  protocol: 'http'
} as Proxy)

let nextProxyId = 100
function exit(overrides: Partial<ClashExitOption> = {}): ClashExitOption {
  const proxyId = overrides.proxy_id ?? nextProxyId++
  return {
    proxy_id: proxyId,
    node_id: proxyId,
    profile_id: 1,
    profile_name: 'Airport A',
    node_name: `Node ${proxyId}`,
    type: 'vmess',
    status: 'active',
    health_status: 'healthy',
    latency_ms: 42,
    exit_ip: `10.0.0.${proxyId % 250}`,
    exit_country: 'Japan',
    exit_country_code: 'JP',
    exit_city: 'Tokyo',
    exit_status: 'ok',
    exit_pending_ip: '',
    exit_key: `ip:10.0.0.${proxyId % 250}`,
    available: true,
    unavailable_reason: '',
    occupants: [],
    platform_checks: { checked_at: null, results: {} },
    ...overrides
  }
}

const list = (exits: ClashExitOption[], overrides: Partial<ClashExitList> = {}): ClashExitList => ({
  max_accounts_per_exit: 1,
  allow_unprobed_exit_binding: false,
  exits,
  ...overrides
})

// The dropdown is teleported to <body>; render it in place so the wrapper can query it.
const stubs = { Icon: true, CountryFlag: true, teleport: true }

async function openSelector(props: Record<string, unknown>) {
  const wrapper = mount(ProxySelector, {
    props: { modelValue: null, proxies: [manualProxy(1)], ...props },
    global: { stubs }
  })
  await wrapper.get('.select-trigger').trigger('click')
  return wrapper
}

/** Blocked exits are folded into a collapsed section at the bottom. */
const expandUnavailable = (wrapper: Awaited<ReturnType<typeof openSelector>>) =>
  wrapper.get('[data-testid="clash-exit-unavailable-toggle"]').trigger('click')

const optionFor = (wrapper: Awaited<ReturnType<typeof openSelector>>, proxyId: number) =>
  wrapper.get(`[data-testid="clash-exit-${proxyId}"]`)

describe('ProxySelector — Clash exits', () => {
  it('keeps the manual-only selector when no exits are passed', async () => {
    const wrapper = await openSelector({})
    expect(wrapper.find('[data-testid="clash-exit-group"]').exists()).toBe(false)
    expect(wrapper.find('.select-options-tall').exists()).toBe(false)
    expect(wrapper.get('.select-search-input').attributes('placeholder')).toBe('admin.proxies.searchProxies')
    expect(wrapper.findAll('.test-btn')).toHaveLength(1)
  })

  it('renders exits grouped by subscription after the manual proxies', async () => {
    const exits = [
      exit({ proxy_id: 201, profile_id: 1, profile_name: 'Airport A', node_name: 'HK 01' }),
      exit({ proxy_id: 202, profile_id: 1, profile_name: 'Airport A', node_name: 'HK 02' }),
      exit({ proxy_id: 203, profile_id: 2, profile_name: 'Backup B', node_name: 'US 01' })
    ]
    const wrapper = await openSelector({ clashExits: list(exits) })

    const groups = wrapper.findAll('[data-testid="clash-exit-group"]')
    expect(groups.map((group) => group.text())).toEqual([
      expect.stringContaining('Airport A'),
      expect.stringContaining('Backup B')
    ])
    // The option list only: the subscription filter above it also names the subscriptions.
    const html = wrapper.get('[role="listbox"]').html()
    expect(html.indexOf('Proxy 1')).toBeLessThan(html.indexOf('HK 01'))
    expect(html.indexOf('HK 02')).toBeLessThan(html.indexOf('Backup B'))
    // Clash exits are never connectivity-tested from the selector.
    expect(wrapper.findAll('.test-btn')).toHaveLength(1)
    expect(wrapper.get('.select-search-input').attributes('placeholder')).toBe('admin.clash.selector.searchPlaceholder')
  })

  it('greys out full, unavailable and unprobed exits but not the ones the account itself occupies', async () => {
    const full = exit({ proxy_id: 301, occupants: [{ id: 99, name: 'other-account', platform: 'openai', is_shadow: false }] })
    const down = exit({ proxy_id: 302, available: false, unavailable_reason: 'health check failing', health_status: 'unhealthy' })
    const unprobed = exit({ proxy_id: 303, exit_ip: '', exit_key: 'node:303' })
    const own = exit({ proxy_id: 304, occupants: [{ id: 7, name: 'this-account', platform: 'openai', is_shadow: false }] })
    const free = exit({ proxy_id: 305 })
    const wrapper = await openSelector({ clashExits: list([full, down, unprobed, own, free]), accountId: 7 })
    await expandUnavailable(wrapper)

    expect(optionFor(wrapper, 301).attributes('aria-disabled')).toBe('true')
    expect(optionFor(wrapper, 301).get('[data-testid="clash-exit-occupancy"]').text()).toContain('other-account')
    expect(optionFor(wrapper, 302).attributes('aria-disabled')).toBe('true')
    expect(optionFor(wrapper, 302).get('[data-testid="clash-exit-block-reason"]').text()).toBe('admin.clash.reasons.healthFailing')
    expect(optionFor(wrapper, 303).attributes('aria-disabled')).toBe('true')
    expect(optionFor(wrapper, 303).get('[data-testid="clash-exit-block-reason"]').text()).toBe('admin.clash.selector.exitUnprobedBlocked')
    expect(optionFor(wrapper, 304).attributes('aria-disabled')).toBeUndefined()
    expect(optionFor(wrapper, 304).get('[data-testid="clash-exit-occupancy"]').text()).toBe('admin.clash.selector.idle')
    expect(optionFor(wrapper, 305).attributes('aria-disabled')).toBeUndefined()

    await optionFor(wrapper, 301).trigger('click')
    await optionFor(wrapper, 302).trigger('click')
    await optionFor(wrapper, 303).trigger('click')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()

    await optionFor(wrapper, 304).trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([[304]])
  })

  it('keeps the unavailable reason visible when an exit is also full', async () => {
    const both = exit({
      proxy_id: 306,
      available: false,
      unavailable_reason: 'health check failing',
      occupants: [{ id: 98, name: 'someone', platform: 'openai', is_shadow: false }]
    })
    const wrapper = await openSelector({ clashExits: list([both]) })
    await expandUnavailable(wrapper)
    const option = optionFor(wrapper, 306)
    expect(option.attributes('aria-disabled')).toBe('true')
    expect(option.get('[data-testid="clash-exit-block-reason"]').text()).toBe('admin.clash.reasons.healthFailing')
    expect(option.get('[data-testid="clash-exit-occupancy"]').text()).toContain('someone')
    expect(option.attributes('title')).toBe('admin.clash.reasons.healthFailing')
  })

  it('does not count shadow accounts as occupants', async () => {
    const shared = exit({ proxy_id: 311, occupants: [{ id: 50, name: 'shadow', platform: 'openai', is_shadow: true }] })
    const wrapper = await openSelector({ clashExits: list([shared]) })
    expect(optionFor(wrapper, 311).attributes('aria-disabled')).toBeUndefined()
  })

  it('respects a per-exit limit above one', async () => {
    const shared = exit({ proxy_id: 321, occupants: [{ id: 51, name: 'first', platform: 'openai', is_shadow: false }] })
    const wrapper = await openSelector({ clashExits: list([shared], { max_accounts_per_exit: 2 }) })
    expect(optionFor(wrapper, 321).attributes('aria-disabled')).toBeUndefined()
    expect(optionFor(wrapper, 321).get('[data-testid="clash-exit-occupancy"]').text()).toContain('first')
  })

  it('keeps the current binding selectable even when it is blocked', async () => {
    const down = exit({ proxy_id: 331, available: false, unavailable_reason: 'node disabled', status: 'disabled' })
    const wrapper = await openSelector({ clashExits: list([down]), modelValue: 331 })
    expect(optionFor(wrapper, 331).attributes('aria-disabled')).toBeUndefined()
    expect(optionFor(wrapper, 331).classes()).toContain('select-option-selected')
  })

  it('lets unprobed exits through when the pool allows it', async () => {
    const unprobed = exit({ proxy_id: 341, exit_ip: '' })
    const wrapper = await openSelector({ clashExits: list([unprobed], { allow_unprobed_exit_binding: true }) })
    expect(optionFor(wrapper, 341).attributes('aria-disabled')).toBeUndefined()
  })

  it.each([
    ['anthropic', { anthropic: 'fail' }, true],
    ['openai', { openai: 'challenge' }, true],
    ['antigravity', { gemini: 'fail' }, true],
    ['gemini', { gemini: 'challenge' }, true],
    ['grok', { grok: 'fail' }, true],
    ['grok', { grok: 'warn' }, false],
    ['antigravity', { openai: 'fail' }, false],
    ['', { openai: 'fail' }, false]
  ])('platform %s with checks %j warns: %s', async (platform, results, warns) => {
    const checked = exit({ proxy_id: 351, platform_checks: { checked_at: null, results: results as never } })
    const wrapper = await openSelector({ clashExits: list([checked]), platform })
    const option = optionFor(wrapper, 351)
    expect(option.find('[data-testid="clash-exit-platform-warning"]').exists()).toBe(warns)
    // Warnings never block the exit.
    expect(option.attributes('aria-disabled')).toBeUndefined()
  })

  it('labels a selected exit as "node (exit IP)" with the Clash tag', async () => {
    const selected = exit({ proxy_id: 361, node_name: 'SG 01', exit_ip: '203.0.113.7' })
    const wrapper = mount(ProxySelector, {
      props: { modelValue: 361, proxies: [manualProxy(1)], clashExits: list([selected]) },
      global: { stubs: { Icon: true, CountryFlag: true } }
    })
    const trigger = wrapper.get('.select-trigger')
    expect(trigger.text()).toContain('admin.clash.selector.selectedLabel {"node":"SG 01","ip":"203.0.113.7"}')
    expect(trigger.find('[data-testid="clash-tag"]').exists()).toBe(true)
  })

  it('shows an unresolvable binding by id instead of "no proxy"', async () => {
    const wrapper = mount(ProxySelector, {
      props: { modelValue: 777, proxies: [manualProxy(1)] },
      global: { stubs: { Icon: true } }
    })
    expect(wrapper.get('.select-trigger').text()).toContain('admin.clash.selector.unlistedProxy {"id":777}')
    expect(wrapper.get('.select-trigger').text()).not.toContain('admin.accounts.noProxy')

    await wrapper.setProps({ modelValue: null })
    expect(wrapper.get('.select-trigger').text()).toBe('admin.accounts.noProxy')
  })

  it('searches node names, subscriptions, exit IPs and countries', async () => {
    const exits = [
      exit({ proxy_id: 371, profile_name: 'Airport A', node_name: 'HK 01', exit_ip: '1.1.1.1', exit_country: 'Hong Kong' }),
      exit({ proxy_id: 372, profile_id: 2, profile_name: 'Backup B', node_name: 'US 01', exit_ip: '2.2.2.2', exit_country: 'United States' })
    ]
    const wrapper = await openSelector({ clashExits: list(exits) })
    const search = wrapper.get('.select-search-input')

    for (const [query, expected] of [
      ['hk 01', [371]],
      ['backup', [372]],
      ['2.2.2', [372]],
      ['hong kong', [371]]
    ] as const) {
      await search.setValue(query)
      const shown = exits.filter((item) => wrapper.find(`[data-testid="clash-exit-${item.proxy_id}"]`).exists())
      expect(shown.map((item) => item.proxy_id)).toEqual(expected)
    }

    await search.setValue('nothing-matches')
    expect(wrapper.find('.select-empty').exists()).toBe(true)
  })

  it('batch-tests only manual proxies with at most four requests in flight', async () => {
    const resolvers: Array<() => void> = []
    testProxy.mockImplementation(() => new Promise((resolve) => {
      resolvers.push(() => resolve({ success: true, latency_ms: 10 }))
    }))
    const proxies = [1, 2, 3, 4, 5, 6].map(manualProxy)
    const wrapper = mount(ProxySelector, {
      props: { modelValue: null, proxies, clashExits: list([exit({ proxy_id: 381 })]) },
      global: { stubs }
    })
    await wrapper.get('.select-trigger').trigger('click')
    await wrapper.get('.batch-test-btn').trigger('click')
    await flushPromises()
    expect(testProxy).toHaveBeenCalledTimes(4)

    while (resolvers.length > 0) {
      resolvers.shift()!()
      await flushPromises()
    }
    expect(testProxy.mock.calls.map(([id]) => id)).toEqual([1, 2, 3, 4, 5, 6])
    expect(wrapper.get('.batch-test-btn').attributes('disabled')).toBeUndefined()
  })
})
