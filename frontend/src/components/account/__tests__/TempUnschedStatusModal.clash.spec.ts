import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import TempUnschedStatusModal from '../TempUnschedStatusModal.vue'
import type { Account } from '@/types'

const mocks = vi.hoisted(() => ({ getTempUnschedulableStatus: vi.fn(), recoverState: vi.fn(), showError: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: mocks } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => mocks }))
vi.mock('@/utils/format', () => ({ formatDateTime: () => 'date' }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)
beforeEach(() => vi.clearAllMocks())

const active = (message: string) => ({
  active: true,
  state: { until_unix: Date.now() / 1000 + 1800, error_message: message, rule_index: -1 }
})

async function open(account: Partial<Account>) {
  const wrapper = mount(TempUnschedStatusModal, {
    props: { show: false, account: { id: 1, name: 'acc', ...account } as Account },
    global: { stubs: { BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' } } }
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

describe('TempUnschedStatusModal — Clash exit pauses', () => {
  it('explains that the pause is managed by Clash health checks', async () => {
    mocks.getTempUnschedulableStatus.mockResolvedValue(active('[clash-exit] SG 01: node disabled'))
    const wrapper = await open({})

    const notice = wrapper.get('[data-testid="temp-unsched-clash-exit"]')
    expect(notice.text()).toContain('admin.accounts.tempUnschedulable.clashExitManaged')
    expect(wrapper.text()).not.toContain('admin.accounts.recoverStateHint')
    expect(wrapper.text()).toContain('SG 01')
    expect(wrapper.text()).toContain('admin.clash.reasons.nodeDisabled')
    expect(wrapper.text()).not.toContain('admin.accounts.tempUnschedulable.matchedKeyword')
    // Manual recovery stays possible (it is simply re-applied while the node is down).
    expect(wrapper.get('button.btn-primary').attributes('disabled')).toBeUndefined()
  })

  it('falls back to the account reason when the status payload has no message', async () => {
    mocks.getTempUnschedulableStatus.mockResolvedValue(active(''))
    const wrapper = await open({ temp_unschedulable_reason: '[clash-exit] HK 01: health check failing' })
    expect(wrapper.find('[data-testid="temp-unsched-clash-exit"]').exists()).toBe(true)
  })

  it('keeps the regular layout for other pauses', async () => {
    mocks.getTempUnschedulableStatus.mockResolvedValue(active('upstream overloaded'))
    const wrapper = await open({})
    expect(wrapper.find('[data-testid="temp-unsched-clash-exit"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('admin.accounts.recoverStateHint')
    expect(wrapper.text()).toContain('admin.accounts.tempUnschedulable.matchedKeyword')
  })
})
