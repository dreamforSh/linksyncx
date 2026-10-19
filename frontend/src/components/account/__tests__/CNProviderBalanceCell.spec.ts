import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import CNProviderBalanceCell from '../CNProviderBalanceCell.vue'
import type { Account } from '@/types'

const { queryBalance } = vi.hoisted(() => ({
  queryBalance: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    cnProviders: { queryBalance }
  }
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

const account = {
  id: 7,
  platform: 'kimi',
  type: 'apikey',
  credentials: { account_mode: 'payg' },
  extra: {
    kimi_balance: 12.5,
    kimi_balance_currency: 'CNY'
  }
} as Account

describe('CNProviderBalanceCell', () => {
  beforeEach(() => {
    queryBalance.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('renders the persisted balance as static text with an explicit query action', async () => {
    const wrapper = mount(CNProviderBalanceCell, { props: { account } })
    await flushPromises()

    // Snapshot value renders without any probe.
    expect(queryBalance).not.toHaveBeenCalled()
    expect(wrapper.get('[data-test="cn-provider-balance-value"]').text()).toContain('CNY 12.50')

    // The control reads as an action; the i18n mock returns the key itself.
    const probeButton = wrapper.get('[data-test="cn-provider-balance-probe"]')
    expect(probeButton.text()).toBe('admin.accounts.cnProviders.probe')

    await probeButton.trigger('click')
    await flushPromises()
    expect(queryBalance).toHaveBeenCalledWith(account.id)
  })

  it('shows the low-balance badge from the snapshot marker', () => {
    const lowAccount = {
      ...account,
      extra: { kimi_balance: 0.4, kimi_balance_low: true }
    } as Account

    const wrapper = mount(CNProviderBalanceCell, { props: { account: lowAccount } })

    expect(wrapper.text()).toContain('admin.accounts.cnProviders.balanceLow')
  })

  it('keeps the snapshot balance visible when a query fails', async () => {
    queryBalance.mockResolvedValue({ success: false, error: 'HTTP 401' })
    const wrapper = mount(CNProviderBalanceCell, { props: { account } })

    await wrapper.get('[data-test="cn-provider-balance-probe"]').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('CNY 12.50')
    expect(wrapper.text()).toContain('HTTP 401')
  })

  it('stops background polling once unmounted, even with a poll in flight', async () => {
    vi.useFakeTimers()
    let resolve!: (value: unknown) => void
    queryBalance
      .mockImplementationOnce(() => new Promise((r) => { resolve = r }))
      .mockResolvedValue({ success: true, balance: 2, currency: 'CNY' })
    const wrapper = mount(CNProviderBalanceCell, { props: { account } })
    await vi.advanceTimersByTimeAsync(60_000)
    expect(queryBalance).toHaveBeenCalledTimes(1)

    wrapper.unmount()
    resolve({ success: true, balance: 1, currency: 'CNY' })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(15 * 60_000)

    expect(queryBalance).toHaveBeenCalledTimes(1)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('drops an in-flight poll result after the cell switches to another account', async () => {
    vi.useFakeTimers()
    let resolve!: (value: unknown) => void
    queryBalance
      .mockImplementationOnce(() => new Promise((r) => { resolve = r }))
      .mockResolvedValue({ success: true, balance: 3, currency: 'CNY' })
    const wrapper = mount(CNProviderBalanceCell, { props: { account } })
    await vi.advanceTimersByTimeAsync(60_000)

    await wrapper.setProps({ account: { ...account, id: 8, extra: {} } as Account })
    resolve({ success: true, balance: 111, currency: 'OLD' })
    await flushPromises()

    expect(wrapper.text()).not.toContain('OLD')
    wrapper.unmount()
  })
})
