import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { AdminGroup } from '@/types'
import SyncFromCrsModal from '../SyncFromCrsModal.vue'

const { previewFromCrs, syncFromCrs, showError, showSuccess } = vi.hoisted(() => ({
  previewFromCrs: vi.fn(),
  syncFromCrs: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: { previewFromCrs, syncFromCrs }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess })
}))

vi.mock('@/stores', () => ({
  useAuthStore: () => ({ isSimpleMode: false })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const BaseDialogStub = defineComponent({
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const groups = [
  { id: 1, name: 'claude-pool', platform: 'anthropic', kind: 'channel', status: 'active' },
  { id: 2, name: 'openai-pool', platform: 'openai', kind: 'channel', status: 'active' }
] as unknown as AdminGroup[]

const previewWith = (newAccounts: Array<{ id: string; platform: string }>) => ({
  new_accounts: newAccounts.map(({ id, platform }) => ({
    crs_account_id: id,
    kind: 'claude',
    name: `acc-${id}`,
    platform,
    type: 'oauth'
  })),
  existing_accounts: []
})

async function openPreview(newAccounts: Array<{ id: string; platform: string }>) {
  previewFromCrs.mockResolvedValue(previewWith(newAccounts))
  const wrapper = mount(SyncFromCrsModal, {
    props: { show: false, groups },
    global: { stubs: { BaseDialog: BaseDialogStub, Icon: true, PlatformIcon: true } }
  })
  await wrapper.setProps({ show: true })
  await wrapper.get('#crs-base-url').setValue('https://crs.example.com')
  await wrapper.get('#crs-username').setValue('admin')
  await wrapper.get('#crs-password').setValue('secret')
  await wrapper.get('form#sync-from-crs-form').trigger('submit')
  await flushPromises()
  return wrapper
}

const syncButton = (wrapper: Awaited<ReturnType<typeof openPreview>>) =>
  wrapper.findAll('button').find(button => button.text() === 'admin.accounts.syncNow')!

describe('SyncFromCrsModal target groups', () => {
  beforeEach(() => {
    for (const fn of [previewFromCrs, syncFromCrs, showError, showSuccess]) fn.mockReset()
    syncFromCrs.mockResolvedValue({ created: 1, updated: 0, skipped: 0, failed: 0, items: [] })
  })

  it('sends the chosen target groups with the selected new accounts', async () => {
    const wrapper = await openPreview([{ id: 'a', platform: 'anthropic' }])

    await wrapper.get('[data-testid="crs-target-groups"] input[type="checkbox"][value="1"]').setValue(true)
    await syncButton(wrapper).trigger('click')
    await flushPromises()

    expect(syncFromCrs).toHaveBeenCalledWith(expect.objectContaining({
      selected_account_ids: ['a'],
      group_ids: [1]
    }))
  })

  it('requires target groups for new accounts', async () => {
    const wrapper = await openPreview([{ id: 'a', platform: 'anthropic' }])

    await syncButton(wrapper).trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('admin.accounts.importTargetGroupsRequired')
    expect(syncFromCrs).not.toHaveBeenCalled()
  })

  it('blocks the sync while a selected platform has no target group', async () => {
    const wrapper = await openPreview([
      { id: 'a', platform: 'anthropic' },
      { id: 'b', platform: 'openai' }
    ])

    await wrapper.get('[data-testid="crs-target-groups"] input[type="checkbox"][value="1"]').setValue(true)
    const chips = wrapper.findAll('[data-testid="target-platform-coverage"] > span')
    expect(chips.map(chip => chip.attributes('data-covered'))).toEqual(['true', 'false'])

    await syncButton(wrapper).trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('admin.accounts.importTargetGroupsMissing')
    expect(syncFromCrs).not.toHaveBeenCalled()
  })

  it('only checks platforms of the accounts that stay selected', async () => {
    const wrapper = await openPreview([
      { id: 'a', platform: 'anthropic' },
      { id: 'b', platform: 'openai' }
    ])

    // 取消勾选 openai 账号后，只需要 anthropic 的目标分组
    const accountBoxes = wrapper.findAll('input[type="checkbox"]').filter(
      input => !input.element.closest('[data-testid="crs-target-groups"]')
    )
    await accountBoxes[1]!.trigger('change')
    await wrapper.get('[data-testid="crs-target-groups"] input[type="checkbox"][value="1"]').setValue(true)
    await syncButton(wrapper).trigger('click')
    await flushPromises()

    expect(syncFromCrs).toHaveBeenCalledWith(expect.objectContaining({
      selected_account_ids: ['a'],
      group_ids: [1]
    }))
  })
})
