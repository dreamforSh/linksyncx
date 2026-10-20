import { describe, expect, it, vi, beforeEach } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

const { listMock, createMock, deleteMock, showErrorMock, showSuccessMock } = vi.hoisted(() => ({
  listMock: vi.fn(),
  createMock: vi.fn(),
  deleteMock: vi.fn(),
  showErrorMock: vi.fn(),
  showSuccessMock: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    tlsFingerprintProfiles: {
      list: listMock,
      create: createMock,
      update: vi.fn(),
      delete: deleteMock
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
    t: (key: string) => key
  })
}))

import TLSFingerprintProfilesModal from '../TLSFingerprintProfilesModal.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const ConfirmDialogStub = defineComponent({
  name: 'ConfirmDialog',
  props: { show: { type: Boolean, default: false } },
  emits: ['confirm', 'cancel'],
  template: '<button v-if="show" data-testid="confirm-delete" @click="$emit(\'confirm\')">confirm</button>'
})

// API 客户端拦截器把后端错误改写成 { status, code, message }，没有 response.data.detail。
const keyShareError = {
  status: 400,
  code: 400,
  message: 'key_share_groups: TLS key share 4588 is absent from supported groups; every key share group must also be listed in curves'
}

async function mountOpenModal() {
  const wrapper = mount(TLSFingerprintProfilesModal, {
    props: { show: false },
    global: { stubs: { BaseDialog: BaseDialogStub, ConfirmDialog: ConfirmDialogStub, Icon: true } }
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

describe('TLSFingerprintProfilesModal', () => {
  beforeEach(() => {
    listMock.mockReset()
    createMock.mockReset()
    deleteMock.mockReset()
    showErrorMock.mockReset()
    showSuccessMock.mockReset()
  })

  // 后端拒绝不自洽模板时给出的原因（哪个字段、缺了哪个组）必须展示给管理员，
  // 只显示“保存模板失败”会让人无从下手。
  it('shows the backend validation message when saving is rejected', async () => {
    listMock.mockResolvedValue([])
    createMock.mockRejectedValue(keyShareError)
    const wrapper = await mountOpenModal()

    await wrapper.get('button.btn-primary.btn-sm').trigger('click')
    await wrapper.get('input[type="text"]').setValue('node-like')
    await wrapper.get('textarea[placeholder="29, 23, 24"]').setValue('29, 23, 24')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(createMock).toHaveBeenCalledWith(expect.objectContaining({ name: 'node-like', curves: [29, 23, 24] }))
    expect(showErrorMock).toHaveBeenCalledWith(keyShareError.message)
    expect(showSuccessMock).not.toHaveBeenCalled()
  })

  it('shows the backend message when deleting fails', async () => {
    listMock.mockResolvedValue([{ id: 3, name: 'chrome', enable_grease: false }])
    deleteMock.mockRejectedValue({ status: 500, code: 500, message: 'database unavailable' })
    const wrapper = await mountOpenModal()

    await wrapper.get('button[title="common.delete"]').trigger('click')
    await wrapper.get('[data-testid="confirm-delete"]').trigger('click')
    await flushPromises()

    expect(deleteMock).toHaveBeenCalledWith(3)
    expect(showErrorMock).toHaveBeenCalledWith('database unavailable')
  })

  it('falls back to the generic message when the error carries none', async () => {
    listMock.mockResolvedValue([])
    createMock.mockRejectedValue({})
    const wrapper = await mountOpenModal()

    await wrapper.get('button.btn-primary.btn-sm').trigger('click')
    await wrapper.get('input[type="text"]').setValue('defaults')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(showErrorMock).toHaveBeenCalledWith('admin.tlsFingerprintProfiles.saveFailed')
  })

  it('explains the key share / curves rule next to the key share field', async () => {
    listMock.mockResolvedValue([])
    const wrapper = await mountOpenModal()
    await wrapper.get('button.btn-primary.btn-sm').trigger('click')

    expect(wrapper.text()).toContain('admin.tlsFingerprintProfiles.form.keyShareGroupsHint')
  })
})
