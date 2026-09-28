import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, ref } from 'vue'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import ProxySelector from '../ProxySelector.vue'
import BaseDialog from '../BaseDialog.vue'
import { CLASH_EXIT_FILTERS_STORAGE_KEY } from '../proxySelectorModel'
import type { ClashBoundAccount, ClashExitList, ClashExitOption, Proxy } from '@/types'

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

const someone: ClashBoundAccount = { id: 99, name: 'someone', platform: 'openai', is_shadow: false }

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

// The dropdown is teleported to <body>; most tests render it in place.
const stubs = { Icon: true, CountryFlag: true, teleport: true }

function mountSelector(props: Record<string, unknown> = {}, options: { attach?: boolean } = {}) {
  return mount(ProxySelector, {
    props: { modelValue: null, proxies: [manualProxy(1)], ...props },
    global: { stubs },
    ...(options.attach ? { attachTo: document.body } : {})
  })
}

type SelectorWrapper = ReturnType<typeof mountSelector>

async function openSelector(props: Record<string, unknown> = {}, options: { attach?: boolean } = {}) {
  const wrapper = mountSelector(props, options)
  await wrapper.get('.select-trigger').trigger('click')
  await flushPromises()
  return wrapper
}

const shownExitIds = (wrapper: SelectorWrapper) =>
  wrapper
    .findAll('[role="option"][data-testid^="clash-exit-"]')
    .map((option) => Number(option.attributes('data-testid')!.slice('clash-exit-'.length)))

const optionFor = (wrapper: SelectorWrapper, proxyId: number) => wrapper.get(`[data-testid="clash-exit-${proxyId}"]`)

const savedFilters = () => JSON.parse(localStorage.getItem(CLASH_EXIT_FILTERS_STORAGE_KEY) || 'null')

describe('ProxySelector — filters and sorting', () => {
  it('shows no Clash toolbar for the manual-only selector', async () => {
    const wrapper = await openSelector()
    expect(wrapper.find('[data-testid="clash-exit-filters"]').exists()).toBe(false)
  })

  it('narrows the exits with the quick filters and remembers them for the next account', async () => {
    const exits = [
      exit({ proxy_id: 401, node_name: 'JP 1' }),
      exit({ proxy_id: 402, node_name: 'JP 2', occupants: [someone] }),
      exit({ proxy_id: 403, node_name: 'HK 1', exit_country: 'Hong Kong', exit_country_code: 'HK', profile_id: 2, profile_name: 'Backup B' }),
      exit({ proxy_id: 404, node_name: 'Down', available: false, unavailable_reason: 'health check failing' })
    ]
    const props = { clashExits: list(exits, { max_accounts_per_exit: 2 }) }
    const wrapper = await openSelector(props)
    expect(shownExitIds(wrapper)).toEqual([401, 402, 403])
    expect(wrapper.get('[data-testid="clash-exit-unavailable-toggle"]').text()).toContain('{"count":1}')
    expect(wrapper.find('[data-testid="clash-exit-filter-summary"]').exists()).toBe(false)

    await wrapper.get('[data-testid="clash-exit-filter-available"]').trigger('click')
    expect(wrapper.find('[data-testid="clash-exit-unavailable-toggle"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="clash-exit-filter-available"]').attributes('aria-pressed')).toBe('true')

    await wrapper.get('[data-testid="clash-exit-filter-idle"]').trigger('click')
    expect(shownExitIds(wrapper)).toEqual([401, 403])

    await wrapper.get('[data-testid="clash-exit-filter-country"]').setValue('HK')
    expect(shownExitIds(wrapper)).toEqual([403])
    expect(wrapper.get('[data-testid="clash-exit-filters"] country-flag-stub').attributes('code')).toBe('HK')
    expect(wrapper.get('[data-testid="clash-exit-filter-summary"]').text()).toBe(
      'admin.clash.selector.filters.summary {"shown":1,"total":4}'
    )

    await wrapper.get('[data-testid="clash-exit-filter-country"]').setValue('')
    await wrapper.get('[data-testid="clash-exit-filter-profile"]').setValue('1')
    expect(shownExitIds(wrapper)).toEqual([401])
    expect(savedFilters()).toMatchObject({ onlyAvailable: true, onlyIdle: true, country: '', profileId: 1 })

    // Another account editor starts with the same choices.
    const next = await openSelector(props)
    expect(next.get('[data-testid="clash-exit-filter-idle"]').attributes('aria-pressed')).toBe('true')
    expect(shownExitIds(next)).toEqual([401])

    await next.get('[data-testid="clash-exit-filter-clear"]').trigger('click')
    expect(shownExitIds(next)).toEqual([401, 402, 403])
    expect(next.find('[data-testid="clash-exit-unavailable-toggle"]').exists()).toBe(true)
    expect(savedFilters()).toMatchObject({ onlyAvailable: false, onlyIdle: false, profileId: null })
  })

  it('offers the platform check filter only for platforms with a reachability check', async () => {
    const exits = [
      exit({ proxy_id: 411, node_name: 'Pass', platform_checks: { checked_at: null, results: { gemini: 'pass' } } }),
      exit({ proxy_id: 412, node_name: 'Warn', platform_checks: { checked_at: null, results: { gemini: 'warn' } } }),
      exit({ proxy_id: 413, node_name: 'Unchecked' })
    ]
    const wrapper = await openSelector({ clashExits: list(exits), platform: 'antigravity' })
    const chip = wrapper.get('[data-testid="clash-exit-filter-platform"]')
    expect(chip.text()).toBe('admin.clash.selector.filters.platformPass {"platform":"Gemini"}')
    await chip.trigger('click')
    expect(shownExitIds(wrapper)).toEqual([411])

    await wrapper.setProps({ platform: 'sora' })
    expect(wrapper.find('[data-testid="clash-exit-filter-platform"]').exists()).toBe(false)
    // The remembered choice does not apply to a platform without a check.
    expect(shownExitIds(wrapper)).toEqual([411, 413, 412])
  })

  it('sorts naturally by default, by latency or idle first, and colours latency like the Clash page', async () => {
    const exits = [
      exit({ proxy_id: 601, node_name: 'HK 10', latency_ms: 50 }),
      exit({ proxy_id: 602, node_name: 'HK 2', latency_ms: 800, occupants: [someone] }),
      exit({ proxy_id: 603, node_name: 'HK 1', health_status: 'unhealthy', latency_ms: null }),
      exit({ proxy_id: 604, node_name: 'HK 3', latency_ms: null }),
      exit({ proxy_id: 605, node_name: 'HK 4', health_status: 'unknown', latency_ms: null }),
      exit({ proxy_id: 606, node_name: 'HK 5', latency_ms: 1500 })
    ]
    const wrapper = await openSelector({ clashExits: list(exits, { max_accounts_per_exit: 2 }) })
    expect(shownExitIds(wrapper)).toEqual([603, 602, 604, 605, 606, 601])

    const sort = wrapper.get('[data-testid="clash-exit-sort"]')
    await sort.setValue('latency')
    expect(shownExitIds(wrapper)).toEqual([601, 602, 606, 604, 605, 603])
    expect(savedFilters()).toMatchObject({ sort: 'latency' })

    await sort.setValue('idle')
    expect(shownExitIds(wrapper)).toEqual([603, 604, 605, 606, 601, 602])

    const chipClasses = (proxyId: number) => optionFor(wrapper, proxyId).get('[data-testid="clash-exit-health"]').classes()
    expect(chipClasses(601)).toContain('bg-emerald-50')
    expect(chipClasses(602)).toContain('bg-amber-50')
    expect(chipClasses(606)).toContain('bg-red-50')
    expect(chipClasses(603)).toContain('bg-red-50')
    expect(chipClasses(605)).toContain('bg-gray-100')
  })

  it('trims the manual proxy search and matches every word', async () => {
    const wrapper = await openSelector({ proxies: [manualProxy(1), manualProxy(2)] })
    await wrapper.get('.select-search-input').setValue('  proxy 2  ')
    expect(wrapper.findAll('.test-btn')).toHaveLength(1)
    expect(wrapper.text()).toContain('Proxy 2')
    expect(wrapper.text()).not.toContain('Proxy 1')
  })
})

describe('ProxySelector — list presentation', () => {
  it('folds blocked exits into a collapsed section after the usable ones, with the reason inline', async () => {
    const exits = [
      exit({ proxy_id: 701, node_name: 'Full', occupants: [someone] }),
      exit({ proxy_id: 702, node_name: 'Unprobed', exit_ip: '', exit_country: '', exit_country_code: '', exit_city: '' }),
      exit({ proxy_id: 703, node_name: 'Free' })
    ]
    const wrapper = await openSelector({ clashExits: list(exits) })
    expect(shownExitIds(wrapper)).toEqual([703])
    const toggle = wrapper.get('[data-testid="clash-exit-unavailable-toggle"]')
    expect(toggle.attributes('aria-expanded')).toBe('false')
    expect(toggle.text()).toBe('admin.clash.selector.unavailableSection {"count":2}')

    await toggle.trigger('click')
    expect(shownExitIds(wrapper)).toEqual([703, 701, 702])
    const listbox = wrapper.get('[role="listbox"]').html()
    expect(listbox.indexOf('clash-exit-703')).toBeLessThan(listbox.indexOf('clash-exit-unavailable-toggle'))

    expect(optionFor(wrapper, 701).get('[data-testid="clash-exit-block-reason"]').text()).toBe(
      'admin.clash.selector.full {"max":1}'
    )
    // "Not probed" is said once: by the block reason, not also in the detail line.
    const unprobed = optionFor(wrapper, 702).text()
    expect(unprobed).toContain('admin.clash.selector.exitUnprobedBlocked')
    expect(unprobed.split('admin.clash.selector.exitUnprobed')).toHaveLength(2)
  })

  it('mentions an unprobed exit once when the pool allows binding it', async () => {
    const wrapper = await openSelector({
      clashExits: list([exit({ proxy_id: 711, exit_ip: '', exit_country: '', exit_country_code: '' })], { allow_unprobed_exit_binding: true })
    })
    const text = optionFor(wrapper, 711).text()
    expect(text.split('admin.clash.selector.exitUnprobed')).toHaveLength(2)
    expect(optionFor(wrapper, 711).find('[data-testid="clash-exit-block-reason"]').exists()).toBe(false)
  })

  it('collapses a subscription from its header, which counts usable, idle and total exits', async () => {
    const exits = [
      exit({ proxy_id: 721, node_name: 'A 1' }),
      exit({ proxy_id: 722, node_name: 'A 2', occupants: [someone] }),
      exit({ proxy_id: 723, node_name: 'A 3', available: false, unavailable_reason: 'node disabled' }),
      exit({ proxy_id: 724, node_name: 'B 1', profile_id: 2, profile_name: 'Backup B' })
    ]
    const wrapper = await openSelector({ clashExits: list(exits, { max_accounts_per_exit: 2 }) })
    const header = wrapper.findAll('[data-testid="clash-exit-group"]')[0]
    expect(header.text()).toContain('admin.clash.selector.groupCounts {"usable":2,"idle":1,"total":3}')
    expect(header.attributes('aria-expanded')).toBe('true')

    await header.trigger('click')
    expect(shownExitIds(wrapper)).toEqual([724])
    expect(wrapper.findAll('[data-testid="clash-exit-group"]')[0].attributes('aria-expanded')).toBe('false')
  })

  it('shows the flag and a latency chip for a bound exit on the trigger', () => {
    const bound = exit({ proxy_id: 731, node_name: 'SG 01', exit_ip: '203.0.113.7', exit_country: 'Singapore', exit_country_code: 'SG', latency_ms: 450 })
    const wrapper = mountSelector({ modelValue: 731, clashExits: list([bound]) })
    const trigger = wrapper.get('.select-trigger')
    expect(trigger.get('country-flag-stub').attributes('code')).toBe('SG')
    const chip = trigger.get('[data-testid="proxy-selector-selected-health"]')
    expect(chip.text()).toBe('450ms')
    expect(chip.classes()).toContain('bg-amber-50')
    expect(trigger.text()).toContain('admin.clash.selector.selectedLabel {"node":"SG 01","ip":"203.0.113.7"}')
  })

  it('renders only a window of a long list without breaking search and selection', async () => {
    const exits = Array.from({ length: 300 }, (_, index) => exit({ proxy_id: 1000 + index, node_name: `Node ${index + 1}` }))
    const wrapper = await openSelector({ clashExits: list(exits) })
    const rendered = wrapper.findAll('[role="option"]')
    expect(rendered.length).toBeGreaterThan(5)
    expect(rendered.length).toBeLessThan(40)
    // Spacer rows stand in for the options that are not rendered, and the set size is announced.
    expect(wrapper.find('[role="listbox"] > [aria-hidden="true"]').exists()).toBe(true)
    expect(rendered[0].attributes('aria-setsize')).toBe('302')

    const search = wrapper.get('.select-search-input')
    await search.setValue('node 250')
    expect(shownExitIds(wrapper)).toEqual([1249])
    await optionFor(wrapper, 1249).trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([[1249]])
  })
})

describe('ProxySelector — bindings', () => {
  it('keeps the exit it started with selectable after picking another one', async () => {
    const original = exit({ proxy_id: 501, node_name: 'Original', available: false, unavailable_reason: 'health check failing' })
    const other = exit({ proxy_id: 502, node_name: 'Other' })
    const blocked = exit({ proxy_id: 503, node_name: 'Blocked', available: false, unavailable_reason: 'node disabled' })
    const wrapper = mountSelector({ modelValue: 501, proxies: [], clashExits: list([original, other, blocked]), accountId: 7 })
    await wrapper.setProps({ modelValue: 502 })
    await wrapper.get('.select-trigger').trigger('click')
    await wrapper.get('[data-testid="clash-exit-filter-available"]').trigger('click')

    // "Only available" still lists the original binding, which says why it is down.
    expect(shownExitIds(wrapper)).toEqual([501, 502])
    expect(optionFor(wrapper, 501).attributes('aria-disabled')).toBeUndefined()
    expect(optionFor(wrapper, 501).get('[data-testid="clash-exit-block-reason"]').text()).toBe('admin.clash.reasons.healthFailing')
    await optionFor(wrapper, 501).trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([[501]])
  })

  it('takes the binding of a newly edited account as its original one', async () => {
    const first = exit({ proxy_id: 511, node_name: 'First', available: false, unavailable_reason: 'node disabled' })
    const second = exit({ proxy_id: 512, node_name: 'Second' })
    const wrapper = mountSelector({ modelValue: 511, proxies: [], clashExits: list([first, second]), accountId: 7 })
    await wrapper.setProps({ accountId: 8, modelValue: 512 })
    await wrapper.get('.select-trigger').trigger('click')
    await wrapper.get('[data-testid="clash-exit-unavailable-toggle"]').trigger('click')
    expect(optionFor(wrapper, 511).attributes('aria-disabled')).toBe('true')
  })
})

describe('ProxySelector — keyboard', () => {
  const keyboardExits = () => [
    exit({ proxy_id: 801, node_name: 'A 1' }),
    exit({ proxy_id: 802, node_name: 'A 2', available: false, unavailable_reason: 'node disabled' }),
    exit({ proxy_id: 803, node_name: 'A 3' })
  ]

  it('moves the highlight over pickable options only and picks it with Enter', async () => {
    const wrapper = mountSelector({ clashExits: list(keyboardExits()) }, { attach: true })
    const trigger = wrapper.get('.select-trigger')
    ;(trigger.element as HTMLElement).focus()
    await trigger.trigger('keydown', { key: 'ArrowDown' })
    await flushPromises()
    expect(trigger.attributes('aria-expanded')).toBe('true')

    const input = wrapper.get('.select-search-input')
    expect(document.activeElement).toBe(input.element)
    expect(input.attributes('role')).toBe('combobox')
    expect(trigger.attributes('aria-controls')).toBe(wrapper.get('[role="listbox"]').attributes('id'))
    await wrapper.get('[data-testid="clash-exit-unavailable-toggle"]').trigger('click')

    const active = () => wrapper.get('.select-option-active')
    expect(active().text()).toContain('admin.accounts.noProxy')
    expect(input.attributes('aria-activedescendant')).toBe(active().attributes('id'))

    const press = async (key: string) => {
      await input.trigger('keydown', { key })
    }
    await press('ArrowDown')
    expect(active().text()).toContain('Proxy 1')
    await press('ArrowDown')
    // Skips the subscription header.
    expect(active().attributes('data-testid')).toBe('clash-exit-801')
    await press('ArrowDown')
    expect(active().attributes('data-testid')).toBe('clash-exit-803')
    // Skips the unavailable section (its header and the blocked exit) and wraps around.
    await press('ArrowDown')
    expect(active().text()).toContain('admin.accounts.noProxy')
    await press('ArrowUp')
    expect(active().attributes('data-testid')).toBe('clash-exit-803')
    expect(input.attributes('aria-activedescendant')).toBe(active().attributes('id'))

    const enter = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
    input.element.dispatchEvent(enter)
    await flushPromises()
    expect(enter.defaultPrevented).toBe(true)
    expect(wrapper.emitted('update:modelValue')).toEqual([[803]])
    expect(trigger.attributes('aria-expanded')).toBe('false')
    expect(document.activeElement).toBe(trigger.element)
  })

  it('lets the pointer take the highlight only when it really moves', async () => {
    const wrapper = await openSelector({ clashExits: list(keyboardExits()) })
    const activeId = () => wrapper.get('.select-option-active').attributes('data-testid')
    await optionFor(wrapper, 801).trigger('mousemove', { clientX: 10, clientY: 20 })
    expect(activeId()).toBe('clash-exit-801')
    await wrapper.get('.select-search-input').trigger('keydown', { key: 'ArrowDown' })
    expect(activeId()).toBe('clash-exit-803')
    // Arrow keys scroll rows under a resting pointer: same coordinates, different row.
    await optionFor(wrapper, 801).trigger('mousemove', { clientX: 10, clientY: 20 })
    expect(activeId()).toBe('clash-exit-803')
    await optionFor(wrapper, 801).trigger('mousemove', { clientX: 12, clientY: 20 })
    expect(activeId()).toBe('clash-exit-801')
  })

  it('highlights the first match while typing', async () => {
    const exits = Array.from({ length: 40 }, (_, index) => exit({ proxy_id: 2000 + index, node_name: `Node ${index + 1}` }))
    const wrapper = await openSelector({ clashExits: list(exits) }, { attach: true })
    const input = wrapper.get('.select-search-input')
    await input.setValue('node 1')
    expect(wrapper.get('.select-option-active').attributes('data-testid')).toBe('clash-exit-2000')
    await input.trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('update:modelValue')).toEqual([[2000]])
  })

  it('closes on Tab and hands focus back to the trigger', async () => {
    const wrapper = await openSelector({ clashExits: list(keyboardExits()) }, { attach: true })
    await wrapper.get('.select-search-input').trigger('keydown', { key: 'Tab' })
    await flushPromises()
    expect(wrapper.find('[data-testid="proxy-selector-panel"]').exists()).toBe(false)
    expect(document.activeElement).toBe(wrapper.get('.select-trigger').element)
  })

  it('closes on a click outside the panel', async () => {
    const wrapper = await openSelector({ clashExits: list(keyboardExits()) }, { attach: true })
    await wrapper.get('[data-testid="clash-exit-filter-idle"]').trigger('click')
    expect(wrapper.find('[data-testid="proxy-selector-panel"]').exists()).toBe(true)
    document.body.click()
    await flushPromises()
    expect(wrapper.find('[data-testid="proxy-selector-panel"]').exists()).toBe(false)
  })

  it('closes only the dropdown on Escape, never the dialog around it', async () => {
    const onClose = vi.fn()
    const documentKeydown = vi.fn()
    const Host = defineComponent({
      components: { BaseDialog, ProxySelector },
      setup: () => ({ value: ref<number | null>(null), proxies: [manualProxy(1)], onClose }),
      template: '<BaseDialog :show="true" title="Edit account" @close="onClose"><ProxySelector v-model="value" :proxies="proxies" /></BaseDialog>'
    })
    mount(Host, { attachTo: document.body, global: { stubs: { Icon: true, CountryFlag: true } } })
    document.addEventListener('keydown', documentKeydown)
    try {
      await flushPromises()
      const trigger = document.querySelector<HTMLButtonElement>('[data-testid="proxy-selector"] .select-trigger')!
      trigger.click()
      await flushPromises()
      // The panel is teleported to <body>, outside the dialog.
      const panel = document.querySelector('[data-testid="proxy-selector-panel"]')!
      expect(panel.parentElement).toBe(document.body)
      const search = panel.querySelector('input')!
      expect(document.activeElement).toBe(search)

      // Escape while an IME composition is open only cancels the composition.
      search.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', isComposing: true, bubbles: true, cancelable: true }))
      await flushPromises()
      expect(trigger.getAttribute('aria-expanded')).toBe('true')

      search.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
      await flushPromises()
      expect(trigger.getAttribute('aria-expanded')).toBe('false')
      // The leave transition runs for a couple of frames before the panel is removed.
      await vi.waitFor(() => expect(document.querySelector('[data-testid="proxy-selector-panel"]')).toBeNull())
      expect(onClose).not.toHaveBeenCalled()
      expect(documentKeydown).not.toHaveBeenCalled()
      expect(document.activeElement).toBe(trigger)

      // With the dropdown closed, Escape reaches the dialog as before.
      trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
      await flushPromises()
      expect(onClose).toHaveBeenCalledTimes(1)
      expect(documentKeydown).toHaveBeenCalledTimes(1)
    } finally {
      document.removeEventListener('keydown', documentKeydown)
    }
  })
})
