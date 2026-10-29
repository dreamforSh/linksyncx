import { describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { mount } from '@vue/test-utils'

import AccountCard from '../AccountCard.vue'
import type { Account, AccountListItem, ClashExitOption } from '@/types'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => (params ? `${key} ${JSON.stringify(params)}` : key)
    })
  }
})

// Every key the table can show; individual tests drop keys to mirror hidden columns.
const ALL_COLUMNS = [
  'select', 'name', 'id', 'platform_type', 'capacity', 'status', 'schedulable', 'today_stats', 'groups',
  'usage', 'custom_usage', 'proxy', 'priority', 'rate_multiplier', 'upstream_billing_rate', 'last_used_at',
  'created_at', 'expires_at', 'notes', 'actions'
]

const StatusStub = defineComponent({
  props: ['account'],
  emits: ['show-temp-unsched'],
  template: '<button data-test="status-stub" @click="$emit(\'show-temp-unsched\', account)">status</button>'
})

const stubs = {
  PlatformTypeBadge: true,
  AccountStatusIndicator: StatusStub,
  AccountCapacityCell: true,
  AccountUsageCell: true,
  AccountTodayStatsCell: true,
  AccountGroupsCell: true,
  CustomUsageCell: true,
  UpstreamBillingRateCell: true,
  ClashTag: true,
  CountryFlag: true,
  Icon: true
}

function makeAccount(overrides: Partial<AccountListItem> = {}): AccountListItem {
  return {
    id: 7,
    name: 'claude-main',
    notes: 'Shared with the night shift',
    platform: 'anthropic',
    type: 'oauth',
    proxy_id: null,
    concurrency: 3,
    priority: 5,
    status: 'active',
    error_message: null,
    last_used_at: null,
    expires_at: null,
    auto_pause_on_expired: false,
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-01T00:00:00Z',
    schedulable: true,
    rate_limited_at: null,
    rate_limit_reset_at: null,
    overload_until: null,
    temp_unschedulable_until: null,
    temp_unschedulable_reason: null,
    session_window_start: null,
    session_window_end: null,
    session_window_status: null,
    ...overrides
  }
}

function mountCard(props: Record<string, unknown> = {}) {
  return mount(AccountCard, {
    props: { account: makeAccount(), visibleColumns: ALL_COLUMNS, ...props },
    global: { stubs }
  })
}

describe('AccountCard', () => {
  it('shows the name with #id, the notes hint, email and platform badge', () => {
    const wrapper = mountCard({
      displayEmail: 'ops@example.com',
      planType: 'max',
      authMode: 'oauth',
      privacyMode: 'training_off',
      tierLabel: 'admin.accounts.tier.pro',
      tierClass: 'bg-blue-100'
    })

    expect(wrapper.get('[data-testid="account-card-name"]').text()).toBe('claude-main')
    expect(wrapper.get('[data-testid="account-card-id"]').text()).toBe('#7')
    expect(wrapper.get('[data-testid="account-card-notes"]').attributes('title')).toBe('Shared with the night shift')
    expect(wrapper.get('[data-testid="account-card-email"]').text()).toBe('ops@example.com')

    const badge = wrapper.findComponent({ name: 'PlatformTypeBadge' })
    expect(badge.props()).toMatchObject({ platform: 'anthropic', type: 'oauth', planType: 'max', authMode: 'oauth', privacyMode: 'training_off' })
    expect(wrapper.get('[data-testid="account-card-platform"]').text()).toContain('admin.accounts.tier.pro')
    expect(wrapper.get('[data-testid="account-card-priority"]').text()).toContain('5')
  })

  it('links API key accounts to their homepage like the table name cell', () => {
    const wrapper = mountCard({ homepageUrl: 'https://relay.example.com' })
    const link = wrapper.get('a[data-testid="account-card-name"]')
    expect(link.attributes('href')).toBe('https://relay.example.com')
    expect(link.attributes('rel')).toBe('noopener noreferrer')
  })

  it('passes the table usage parameters through so batched usage keeps working', () => {
    const requestBatchedUsage = vi.fn()
    const batchedUsage = { five_hour: null }
    const todayStats = { requests: 3, tokens: 10, cost: 0.1, standard_cost: 0.1, user_cost: 0.2 }
    const wrapper = mountCard({
      todayStats,
      todayStatsLoading: true,
      manualRefreshToken: 4,
      batchedUsage,
      batchedUsageError: 'Failed',
      batchedUsageLoading: true,
      requestBatchedUsage
    })

    const usage = wrapper.findComponent({ name: 'AccountUsageCell' })
    expect(usage.props()).toMatchObject({
      todayStats,
      todayStatsLoading: true,
      manualRefreshToken: 4,
      batchedUsage,
      batchedUsageError: 'Failed',
      batchedUsageLoading: true,
      requestBatchedUsage
    })
    expect(usage.props('account')).toMatchObject({ id: 7 })

    usage.vm.$emit('usage-loaded', batchedUsage)
    expect(wrapper.emitted('usage-loaded')?.[0]).toEqual([batchedUsage])
  })

  it('hides optional sections the same way the table hides columns', () => {
    const wrapper = mountCard({
      visibleColumns: ['select', 'name', 'status', 'actions']
    })

    expect(wrapper.find('[data-testid="account-card-status"]').exists()).toBe(true)
    for (const section of ['platform', 'capacity', 'usage', 'today-stats', 'groups', 'proxy', 'priority', 'last-used', 'expires', 'schedulable']) {
      expect(wrapper.find(`[data-testid="account-card-${section}"]`).exists(), section).toBe(false)
    }
    // Name block and actions are always present, like the table's fixed columns.
    expect(wrapper.find('[data-testid="account-card-id"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="account-card-more"]').exists()).toBe(true)
  })

  it('summarizes a Clash-bound proxy with its exit', () => {
    const exit = { node_name: 'HK 01', exit_ip: '103.151.172.20', exit_country: 'Hong Kong', exit_country_code: 'HK' } as ClashExitOption
    const wrapper = mountCard({
      account: makeAccount({
        proxy_id: 101,
        proxy: {
          id: 101,
          name: 'Clash·Airport A·HK 01',
          protocol: 'socks5',
          host: '127.0.0.1',
          port: 42001,
          username: null,
          status: 'active',
          expires_at: null,
          fallback_mode: 'none',
          expiry_warn_days: 7,
          created_at: '',
          updated_at: '',
          source: 'clash'
        } as unknown as AccountListItem['proxy']
      }),
      clashExit: exit
    })

    const proxy = wrapper.get('[data-testid="account-card-clash-proxy"]')
    expect(proxy.text()).toContain('HK 01')
    expect(proxy.text()).toContain('103.151.172.20')
  })

  it('marks the selection and emits selection and schedulable toggles', async () => {
    const wrapper = mountCard({ selected: true })
    expect(wrapper.get('[data-testid="account-card"]').classes()).toContain('ring-2')
    expect(wrapper.get<HTMLInputElement>('[data-testid="account-card-select"]').element.checked).toBe(true)

    await wrapper.get('[data-testid="account-card-select"]').setValue(false)
    expect(wrapper.emitted('toggle-select')).toHaveLength(1)

    const toggle = wrapper.get('[data-testid="account-card-schedulable"]')
    expect(toggle.attributes('aria-checked')).toBe('true')
    await toggle.trigger('click')
    expect(wrapper.emitted('toggle-schedulable')).toHaveLength(1)

    await wrapper.setProps({ togglingSchedulable: true })
    expect(toggle.attributes('disabled')).toBeDefined()
  })

  it('forwards the temp-unschedulable details request with the account', async () => {
    const account = makeAccount()
    const wrapper = mountCard({ account })
    await wrapper.get('[data-test="status-stub"]').trigger('click')
    expect((wrapper.emitted('show-temp-unsched')?.[0]?.[0] as Account).id).toBe(7)
  })

  it('emits edit, delete and more; more carries the native click on the button', async () => {
    let anchor: EventTarget | null = null
    const wrapper = mount(AccountCard, {
      props: {
        account: makeAccount(),
        visibleColumns: ALL_COLUMNS,
        // Runs during dispatch, which is when AccountsView.openMenu reads currentTarget.
        onMore: (event: MouseEvent) => {
          anchor = event.currentTarget
        }
      },
      global: { stubs }
    })

    await wrapper.get('[data-testid="account-card-edit"]').trigger('click')
    await wrapper.get('[data-testid="account-card-delete"]').trigger('click')
    const more = wrapper.get('[data-testid="account-card-more"]')
    await more.trigger('click')

    expect(wrapper.emitted('edit')).toHaveLength(1)
    expect(wrapper.emitted('delete')).toHaveLength(1)
    const [event] = wrapper.emitted('more')![0] as [MouseEvent]
    expect(event).toBeInstanceOf(MouseEvent)
    expect(event.type).toBe('click')
    expect(anchor).toBe(more.element)
  })
})
