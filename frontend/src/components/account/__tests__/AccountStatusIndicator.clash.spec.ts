import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountStatusIndicator from '../AccountStatusIndicator.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => (params ? `${key} ${JSON.stringify(params)}` : key)
    })
  }
})

function makeAccount(overrides: Partial<Account>): Account {
  return {
    id: 1,
    name: 'account',
    platform: 'openai',
    type: 'oauth',
    proxy_id: 101,
    concurrency: 1,
    priority: 1,
    status: 'active',
    error_message: null,
    last_used_at: null,
    expires_at: null,
    auto_pause_on_expired: true,
    created_at: '2026-03-15T00:00:00Z',
    updated_at: '2026-03-15T00:00:00Z',
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

const mountIndicator = (account: Account) =>
  mount(AccountStatusIndicator, { props: { account }, global: { stubs: { Icon: true } } })

describe('AccountStatusIndicator — Clash exit pauses', () => {
  it('labels [clash-exit] pauses as "exit unavailable" and shows the node + reason', async () => {
    const account = makeAccount({
      temp_unschedulable_until: '2099-01-01T00:00:00Z',
      temp_unschedulable_reason: '[clash-exit] JP 02: health check failing'
    })
    const wrapper = mountIndicator(account)

    const badge = wrapper.get('button.badge')
    expect(badge.text()).toBe('admin.accounts.status.clashExitUnavailable')
    expect(badge.attributes('data-clash-exit')).toBe('true')
    expect(badge.attributes('title')).toBe('JP 02 · admin.clash.reasons.healthFailing')
    expect(wrapper.get('[data-testid="clash-exit-pause-reason"]').text()).toBe('JP 02 · admin.clash.reasons.healthFailing')
    expect(wrapper.text()).not.toContain('admin.accounts.status.tempUnschedulableUntil')

    await badge.trigger('click')
    expect(wrapper.emitted('show-temp-unsched')?.[0]).toEqual([account])
  })

  it('keeps the regular temp-unschedulable badge for other reasons', () => {
    const wrapper = mountIndicator(makeAccount({
      temp_unschedulable_until: '2099-01-01T00:00:00Z',
      temp_unschedulable_reason: '{"status_code":529,"matched_keyword":"overloaded"}'
    }))
    const badge = wrapper.get('button.badge')
    expect(badge.text()).toBe('admin.accounts.status.tempUnschedulable')
    expect(badge.attributes('data-clash-exit')).toBeUndefined()
    expect(wrapper.find('[data-testid="clash-exit-pause-reason"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('admin.accounts.status.tempUnschedulableUntil')
  })

  it('ignores an expired Clash pause', () => {
    const wrapper = mountIndicator(makeAccount({
      temp_unschedulable_until: '2000-01-01T00:00:00Z',
      temp_unschedulable_reason: '[clash-exit] JP 02: health check failing'
    }))
    expect(wrapper.text()).not.toContain('admin.accounts.status.clashExitUnavailable')
    expect(wrapper.text()).toContain('admin.accounts.status.active')
  })
})
