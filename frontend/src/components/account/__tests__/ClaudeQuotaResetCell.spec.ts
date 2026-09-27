import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ClaudeQuotaResetCell from '../ClaudeQuotaResetCell.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import type { Account, ClaudeResetSnapshot } from '@/types'
import { refreshClaudeQuota, resetClaudeQuota } from '@/api/admin/accounts'

vi.mock('@/api/admin/accounts', () => ({
  refreshClaudeQuota: vi.fn(),
  resetClaudeQuota: vi.fn()
}))

// 只有列出的 key “存在”：用来验证限额名与错误码的本地化分支。
const KNOWN_KEYS = new Set([
  'admin.accounts.claudeQuotaReset.limits.five_hour',
  'admin.accounts.claudeQuotaReset.limits.seven_day',
  'admin.accounts.claudeQuotaReset.errors.CLAUDE_RESET_REQUIRES_LIMIT'
])

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
      te: (key: string) => KNOWN_KEYS.has(key)
    })
  }
})

const FUTURE = '2099-10-22T07:00:00Z'
const FUTURE_EARLY = '2099-10-05T07:00:00Z'
const PAST = '2020-01-01T00:00:00Z'

function makeAccount(overrides: Partial<Account> = {}): Account {
  return {
    id: 7,
    name: 'claude-max',
    platform: 'anthropic',
    type: 'oauth',
    proxy_id: null,
    concurrency: 3,
    priority: 50,
    status: 'active',
    error_message: null,
    last_used_at: null,
    expires_at: null,
    auto_pause_on_expired: false,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
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

function snapshot(overrides: Partial<ClaudeResetSnapshot> = {}): ClaudeResetSnapshot {
  return {
    fetched_at: '2026-09-27T12:00:00Z',
    at_wall: false,
    available_count: 0,
    cedar_ember: {
      eligible: true,
      at_limit: false,
      exhausted: [],
      grants: [
        { resets_total: 1, resets_left: 1, ends_at: FUTURE, clears: ['five_hour', 'seven_day'], paused: false, usable_now: true, use_requires_limit: false, next: true },
        { resets_total: 2, resets_left: 2, ends_at: PAST, clears: ['five_hour'], paused: false, usable_now: true, use_requires_limit: false, next: false }
      ]
    },
    juniper_tide: null,
    ...overrides
  }
}

const withSnapshot = (value: ClaudeResetSnapshot) => makeAccount({ extra: { claude_reset_snapshot: value } })

const countButton = (wrapper: ReturnType<typeof mount>) => wrapper.get('[data-testid="claude-reset-count"]')
const useButton = (wrapper: ReturnType<typeof mount>) => wrapper.get('[data-testid="claude-reset-use"]')

beforeEach(() => {
  vi.mocked(refreshClaudeQuota).mockReset()
  vi.mocked(resetClaudeQuota).mockReset()
})

describe('ClaudeQuotaResetCell', () => {
  it('only renders for Claude OAuth accounts', () => {
    expect(mount(ClaudeQuotaResetCell, { props: { account: makeAccount({ type: 'setup-token' }) } }).html()).toBe('<!--v-if-->')
    expect(mount(ClaudeQuotaResetCell, { props: { account: makeAccount({ platform: 'openai' }) } }).html()).toBe('<!--v-if-->')
    expect(mount(ClaudeQuotaResetCell, { props: { account: makeAccount() } }).find('[data-testid="claude-reset-count"]').exists()).toBe(true)
  })

  it('rehydrates the count from the snapshot and drops expired grants', () => {
    const wrapper = mount(ClaudeQuotaResetCell, {
      props: {
        account: withSnapshot(snapshot({ juniper_tide: { eligible: true, in_experiment: true, arm: 'reset', available: true } }))
      }
    })
    // 1 张未过期券 + 可用的每周会话重置；过期券（剩余 2 次）不计入
    expect(countButton(wrapper).text()).toContain('2')
    expect(wrapper.get('[data-testid="claude-reset-details"]').text()).toContain('admin.accounts.claudeQuotaReset.expiresAt')
    expect(wrapper.get('[data-testid="claude-reset-weekly"]').text()).toBe('admin.accounts.claudeQuotaReset.weeklyAvailable')
    expect(useButton(wrapper).attributes('disabled')).toBeUndefined()
  })

  it('keeps the reset disabled until a snapshot is loaded', () => {
    const wrapper = mount(ClaudeQuotaResetCell, { props: { account: makeAccount() } })
    expect(useButton(wrapper).attributes('disabled')).toBeDefined()
    expect(useButton(wrapper).attributes('title')).toBe('admin.accounts.claudeQuotaReset.resetTooltipNeedQuery')
  })

  it('flags grants that can only be used at the limit', () => {
    const wrapper = mount(ClaudeQuotaResetCell, {
      props: {
        account: withSnapshot(snapshot({
          cedar_ember: {
            eligible: true,
            at_limit: false,
            exhausted: [],
            grants: [{ resets_total: 1, resets_left: 1, ends_at: FUTURE, clears: ['five_hour'], paused: false, usable_now: false, use_requires_limit: true, next: true }]
          }
        }))
      }
    })
    expect(wrapper.text()).toContain('admin.accounts.claudeQuotaReset.requiresLimit')
    expect(useButton(wrapper).attributes('disabled')).toBeDefined()
    expect(useButton(wrapper).attributes('title')).toBe('admin.accounts.claudeQuotaReset.resetTooltipRequiresLimit')
  })

  it('shows every held grant when expanded, nearest expiry first', async () => {
    const grants = snapshot().cedar_ember!.grants
    grants[1] = { ...grants[1], ends_at: FUTURE_EARLY }
    const wrapper = mount(ClaudeQuotaResetCell, { props: { account: withSnapshot(snapshot({ cedar_ember: { eligible: true, at_limit: false, exhausted: [], grants } })) } })
    expect(countButton(wrapper).text()).toContain('3')
    await wrapper.get('[data-testid="claude-reset-grant-toggle"]').trigger('click')
    const rows = wrapper.get('[data-testid="claude-reset-grant-details"]').findAll('span[title]')
    expect(rows).toHaveLength(2)
    // 最早到期的券（剩 2 次）排在第一行
    expect(rows[0].text()).toContain('×2')
    expect(rows[1].text()).not.toContain('×')
  })

  it('refreshes the snapshot and forwards usage and account updates', async () => {
    const account = makeAccount()
    const updatedAccount = { ...account, extra: { claude_subscription: { plan_type: 'max_20x', updated_at: '2026-09-27T12:00:00Z' } } }
    vi.mocked(refreshClaudeQuota).mockResolvedValue({
      snapshot: snapshot(),
      usage: { five_hour: { utilization: 10, resets_at: null, remaining_seconds: 0 } },
      cache_persisted: true,
      account: updatedAccount
    })
    const wrapper = mount(ClaudeQuotaResetCell, { props: { account } })

    await countButton(wrapper).trigger('click')
    await flushPromises()

    expect(refreshClaudeQuota).toHaveBeenCalledWith(7)
    expect(countButton(wrapper).text()).toContain('1')
    expect(wrapper.emitted('usage-updated')?.[0]?.[0]).toMatchObject({ five_hour: { utilization: 10 } })
    expect(wrapper.emitted('account-updated')?.[0]?.[0]).toStrictEqual(updatedAccount)
  })

  it('lets the operator choose the reset when both kinds are available', async () => {
    vi.mocked(resetClaudeQuota).mockResolvedValue({
      program: 'cedar_ember',
      result: 'reset',
      cleared: ['five_hour', 'seven_day'],
      snapshot: snapshot({ cedar_ember: { eligible: true, at_limit: false, exhausted: [], grants: [] } }),
      cache_refreshed: true,
      account_state_recovered: true
    })
    const wrapper = mount(ClaudeQuotaResetCell, {
      props: { account: withSnapshot(snapshot({ juniper_tide: { eligible: true, in_experiment: true, arm: 'reset', available: true } })) },
      global: { stubs: { teleport: true } }
    })

    await useButton(wrapper).trigger('click')
    const programs = wrapper.get('[data-testid="claude-reset-programs"]')
    expect(programs.findAll('input[type="radio"]').map((input) => (input.element as HTMLInputElement).value)).toEqual([
      'juniper_tide',
      'cedar_ember'
    ])
    await wrapper.get('[data-testid="claude-reset-program-cedar_ember"]').setValue(true)
    wrapper.findComponent(ConfirmDialog).vm.$emit('confirm')
    await flushPromises()

    expect(resetClaudeQuota).toHaveBeenCalledWith(7, 'cedar_ember')
    expect(wrapper.text()).toContain('admin.accounts.claudeQuotaReset.resetSuccess')
    expect(wrapper.text()).toContain('admin.accounts.claudeQuotaReset.limits.five_hour')
    // 新快照里已没有可用的券，但每周会话重置状态不在其中 → 次数归零
    expect(countButton(wrapper).text()).toContain('0')
  })

  it('reports results that did not spend a reset', async () => {
    vi.mocked(resetClaudeQuota).mockResolvedValue({
      program: 'cedar_ember',
      result: 'not_limited',
      cache_refreshed: false,
      account_state_recovered: false
    })
    const wrapper = mount(ClaudeQuotaResetCell, { props: { account: withSnapshot(snapshot()) } })

    await useButton(wrapper).trigger('click')
    wrapper.findComponent(ConfirmDialog).vm.$emit('confirm')
    await flushPromises()

    expect(resetClaudeQuota).toHaveBeenCalledWith(7, 'cedar_ember')
    expect(wrapper.text()).toContain('admin.accounts.claudeQuotaReset.resetNotLimited')
    expect(countButton(wrapper).text()).toContain('1')
    expect(wrapper.emitted('account-updated')).toBeUndefined()
  })

  it('treats a consumed reset without a read-back snapshot as unknown', async () => {
    vi.mocked(resetClaudeQuota).mockResolvedValue({
      program: 'cedar_ember',
      result: 'reset',
      cache_refreshed: false,
      account_state_recovered: true,
      warning_code: 'reset_credit_cache_refresh_failed'
    })
    const wrapper = mount(ClaudeQuotaResetCell, { props: { account: withSnapshot(snapshot()) } })

    await useButton(wrapper).trigger('click')
    wrapper.findComponent(ConfirmDialog).vm.$emit('confirm')
    await flushPromises()

    expect(wrapper.text()).toContain('admin.accounts.claudeQuotaReset.resetCacheRefreshFailed')
    expect(countButton(wrapper).text()).not.toMatch(/\d/)
    expect(useButton(wrapper).attributes('disabled')).toBeDefined()
  })

  it('maps known error codes to localized messages', async () => {
    vi.mocked(resetClaudeQuota).mockRejectedValue({ status: 409, reason: 'CLAUDE_RESET_REQUIRES_LIMIT', message: 'raw' })
    const wrapper = mount(ClaudeQuotaResetCell, { props: { account: withSnapshot(snapshot()) } })

    await useButton(wrapper).trigger('click')
    wrapper.findComponent(ConfirmDialog).vm.$emit('confirm')
    await flushPromises()
    expect(wrapper.text()).toContain('admin.accounts.claudeQuotaReset.errors.CLAUDE_RESET_REQUIRES_LIMIT')

    vi.mocked(refreshClaudeQuota).mockRejectedValue({ status: 502, reason: 'SOMETHING_ELSE', message: 'upstream exploded' })
    await countButton(wrapper).trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('upstream exploded')
  })
})
