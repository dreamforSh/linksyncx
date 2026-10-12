import { describe, expect, it, vi, beforeEach } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

const { batchDeviceProfileMock, showErrorMock, showSuccessMock } = vi.hoisted(() => ({
  batchDeviceProfileMock: vi.fn(),
  showErrorMock: vi.fn(),
  showSuccessMock: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      batchDeviceProfile: batchDeviceProfileMock
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: showErrorMock,
    showSuccess: showSuccessMock
  })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key)
  })
}))

import DeviceProfileBatchDialog from '../DeviceProfileBatchDialog.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

function mountDialog(accountIds = [1, 2, 3]) {
  return mount(DeviceProfileBatchDialog, {
    props: { show: true, accountIds },
    global: { stubs: { BaseDialog: BaseDialogStub } }
  })
}

describe('DeviceProfileBatchDialog', () => {
  beforeEach(() => {
    batchDeviceProfileMock.mockReset()
    showErrorMock.mockReset()
    showSuccessMock.mockReset()
  })

  it('randomizes with identity reset by default and lists accounts that were not updated', async () => {
    batchDeviceProfileMock.mockResolvedValue({
      updated: 1,
      skipped: 1,
      failed: 1,
      results: [
        { account_id: 1, name: 'claude-a', platform: 'anthropic', status: 'updated', identity_reset: true },
        { account_id: 2, name: 'spark', platform: 'openai', status: 'skipped', reason: 'shadow' },
        { account_id: 3, name: 'codex-b', platform: 'openai', status: 'failed', reason: 'db down' }
      ]
    })

    const wrapper = mountDialog()
    await wrapper.get('[data-testid="device-profile-batch-submit"]').trigger('click')
    await flushPromises()

    expect(batchDeviceProfileMock).toHaveBeenCalledWith([1, 2, 3], 'randomize', true)
    expect(showErrorMock).toHaveBeenCalledTimes(1)
    const result = wrapper.get('[data-testid="device-profile-batch-result"]')
    expect(result.text()).toContain('admin.accounts.deviceProfile.batch.identityResetCount:{"count":1}')
    expect(result.text()).toContain('admin.accounts.deviceProfile.batch.skipReasons.shadow')
    expect(result.text()).toContain('db down')
    expect(result.text()).not.toContain('claude-a')
    expect(wrapper.emitted('done')).toHaveLength(1)
  })

  it('clears profiles without resetting identities when unchecked', async () => {
    batchDeviceProfileMock.mockResolvedValue({ updated: 2, skipped: 0, failed: 0, results: [] })

    const wrapper = mountDialog([4, 5])
    await wrapper.get('[data-testid="device-profile-batch-clear"]').setValue(true)
    await wrapper.get('[data-testid="device-profile-batch-reset-identity"]').setValue(false)
    await wrapper.get('[data-testid="device-profile-batch-submit"]').trigger('click')
    await flushPromises()

    expect(batchDeviceProfileMock).toHaveBeenCalledWith([4, 5], 'clear', false)
    expect(showSuccessMock).toHaveBeenCalledTimes(1)
  })

  it('refuses batches above the backend limit', async () => {
    const wrapper = mountDialog(Array.from({ length: 1001 }, (_, i) => i + 1))
    await wrapper.get('[data-testid="device-profile-batch-submit"]').trigger('click')
    await flushPromises()

    expect(batchDeviceProfileMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('admin.accounts.deviceProfile.batch.tooMany:{"max":1000}')
  })
})
