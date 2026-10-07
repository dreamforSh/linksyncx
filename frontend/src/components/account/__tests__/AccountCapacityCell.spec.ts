import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountCapacityCell from '../AccountCapacityCell.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const account = (overrides: Partial<Account> = {}) => ({
  id: 1, name: 'claude', platform: 'anthropic', type: 'oauth', concurrency: 3, current_concurrency: 0,
  ...overrides,
}) as Account

const sessionBadge = (value: Account) => {
  const wrapper = mount(AccountCapacityCell, { props: { account: value } })
  return wrapper.findAll('span[title]').find((el) => el.attributes('title')?.includes('capacity.sessions'))
}

describe('AccountCapacityCell session budget', () => {
  it('shows the effective budget for accounts without their own limit', () => {
    const badge = sessionBadge(account({ session_budget: 5, active_sessions: 2 }))
    expect(badge?.text()).toBe('2/5')
    expect(badge?.attributes('title')).toContain('admin.accounts.capacity.sessions.normal')
    expect(badge?.attributes('title')).toContain('admin.accounts.capacity.sessions.systemDefault')
  })

  it('marks single-session accounts and reports a full budget', () => {
    const badge = sessionBadge(account({ session_budget: 1, active_sessions: 1, session_id_masking_enabled: true }))
    expect(badge?.text()).toBe('1/1')
    expect(badge?.classes()).toContain('bg-red-100')
    expect(badge?.attributes('title')).toContain('admin.accounts.capacity.sessions.full')
    expect(badge?.attributes('title')).toContain('admin.accounts.capacity.sessions.singleSession')
  })

  it('uses the account limit when it is set, and falls back to max_sessions from older backends', () => {
    expect(sessionBadge(account({ max_sessions: 3, session_budget: 3, active_sessions: 1 }))?.attributes('title'))
      .not.toContain('systemDefault')
    expect(sessionBadge(account({ max_sessions: 3 }))?.text()).toBe('0/3')
  })

  it('hides the badge when the budget is unlimited or the account is not Claude OAuth', () => {
    expect(sessionBadge(account())).toBeUndefined()
    expect(sessionBadge(account({ type: 'apikey', session_budget: 5 }))).toBeUndefined()
  })
})
