import { describe, expect, it, vi, beforeEach } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import zh from '@/i18n/locales/zh'

type Translate = (key: string, params?: Record<string, unknown>) => string

const { listMock, createMock, deleteMock, showErrorMock, showSuccessMock, i18nState } = vi.hoisted(() => ({
  listMock: vi.fn(),
  createMock: vi.fn(),
  deleteMock: vi.fn(),
  showErrorMock: vi.fn(),
  showSuccessMock: vi.fn(),
  // 默认把 key 原样返回，便于按 key 断言；需要验证真实中文文案的用例换成 zh 语言包。
  i18nState: { translate: null as Translate | null }
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
    t: (key: string, params?: Record<string, unknown>) =>
      i18nState.translate ? i18nState.translate(key, params) : key
  })
}))

import TLSFingerprintProfilesModal from '../TLSFingerprintProfilesModal.vue'

// vitest 用的是 vue-i18n 运行时构建、没有开 JIT 编译，真实 createI18n 无法编译字符串文案，
// 这里直接按 key 查 zh 语言包并替换 {name} 占位符；缺失的 key 与 vue-i18n 一样原样返回。
function zhMessage(key: string, params: Record<string, unknown> = {}): string {
  const value = key.split('.').reduce<unknown>(
    (node, segment) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[segment] : undefined),
    zh
  )
  if (typeof value !== 'string') return key
  return value.replace(/\{(\w+)\}/g, (match, name: string) => (name in params ? String(params[name]) : match))
}

function useZhMessages() {
  i18nState.translate = zhMessage
}

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

// API 客户端拦截器把后端错误改写成 { status, code, reason, message, metadata }，没有 response.data.detail。
// 后端没有映射的错误一律是 500 "internal error"（infraerrors 的兜底）。
const internalError = { status: 500, code: 500, message: 'internal error' }

async function mountOpenModal() {
  const wrapper = mount(TLSFingerprintProfilesModal, {
    props: { show: false },
    global: { stubs: { BaseDialog: BaseDialogStub, ConfirmDialog: ConfirmDialogStub, Icon: true } }
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

async function submitNewProfile(wrapper: VueWrapper, name: string, curves?: string) {
  await wrapper.get('button.btn-primary.btn-sm').trigger('click')
  await wrapper.get('input[type="text"]').setValue(name)
  if (curves !== undefined) {
    await wrapper.get('textarea[placeholder="29, 23, 24"]').setValue(curves)
  }
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}

async function deleteFirstProfile(wrapper: VueWrapper, deleteButtonTitle = 'common.delete') {
  await wrapper.get(`button[title="${deleteButtonTitle}"]`).trigger('click')
  await wrapper.get('[data-testid="confirm-delete"]').trigger('click')
  await flushPromises()
}

describe('TLSFingerprintProfilesModal', () => {
  beforeEach(() => {
    listMock.mockReset()
    createMock.mockReset()
    deleteMock.mockReset()
    showErrorMock.mockReset()
    showSuccessMock.mockReset()
    i18nState.translate = null
  })

  // 后端拒绝不自洽模板时给出的原因（哪个字段、缺了哪个组）必须展示给管理员，只显示“保存模板失败”
  // 会让人无从下手。后端 message 用的是 API 字段名（key_share_groups / curves / extensions），
  // 中文界面要按 reason 换成表单上的字段名，并带上 metadata 里不在椭圆曲线中的组。
  it.each([
    [
      'a key share group missing from the curves',
      {
        status: 400,
        code: 400,
        reason: 'TLS_PROFILE_KEY_SHARE_NOT_IN_CURVES',
        metadata: { group: '4588' },
        message:
          'key_share_groups: TLS key share 4588 is absent from supported groups; every key share group must also be listed in curves (empty fields fall back to the built-in defaults)'
      },
      '密钥共享组中的 4588 没有出现在椭圆曲线中。密钥共享组的每一项都必须同时列在椭圆曲线里；留空的字段使用内置默认值（椭圆曲线 4588, 29, 23, 24；密钥共享组 4588, 29）'
    ],
    [
      'key_share without supported_groups in the extensions',
      {
        status: 400,
        code: 400,
        reason: 'TLS_PROFILE_KEY_SHARE_WITHOUT_SUPPORTED_GROUPS',
        message:
          'extensions: TLS key_share extension (51) is sent without the supported_groups extension (10); add supported_groups (10) to extensions, or leave extensions empty to use the built-in order'
      },
      '扩展中包含 key_share（51）却没有 supported_groups（10），椭圆曲线将无法发送。请在扩展中加入 10，或留空扩展以使用内置顺序'
    ]
  ])('localizes the rejection reason for %s', async (_label, error, expected) => {
    useZhMessages()
    listMock.mockResolvedValue([])
    createMock.mockRejectedValue(error)
    const wrapper = await mountOpenModal()

    await submitNewProfile(wrapper, 'node-like', '29, 23, 24')

    expect(createMock).toHaveBeenCalledWith(expect.objectContaining({ name: 'node-like', curves: [29, 23, 24] }))
    expect(showErrorMock).toHaveBeenCalledWith(expected)
    expect(showSuccessMock).not.toHaveBeenCalled()
  })

  // 没有本地化文案的 4xx（如其余校验失败、请求体解析失败）原样展示后端 message，总比只说“保存失败”强。
  it('shows the backend message for a 4xx without a localized reason', async () => {
    useZhMessages()
    const invalidError = {
      status: 400,
      code: 400,
      reason: 'TLS_PROFILE_INVALID',
      metadata: { field: 'name' },
      message: 'name: name is required'
    }
    listMock.mockResolvedValue([])
    createMock.mockRejectedValue(invalidError)
    const wrapper = await mountOpenModal()

    await submitNewProfile(wrapper, 'renamed')

    expect(showErrorMock).toHaveBeenCalledWith(invalidError.message)
  })

  // 重名保存（409）与删除已不存在的模板（404）按 reason 显示本地化文案，而不是英文的后端 message。
  it('localizes the duplicate-name and missing-profile reasons', async () => {
    useZhMessages()
    listMock.mockResolvedValue([{ id: 3, name: 'chrome', enable_grease: false }])
    createMock.mockRejectedValue({
      status: 409,
      code: 409,
      reason: 'TLS_PROFILE_NAME_EXISTS',
      message: 'TLS fingerprint profile name already exists'
    })
    deleteMock.mockRejectedValue({
      status: 404,
      code: 404,
      reason: 'TLS_PROFILE_NOT_FOUND',
      message: 'TLS fingerprint profile not found'
    })
    const wrapper = await mountOpenModal()

    await submitNewProfile(wrapper, 'chrome')
    expect(showErrorMock).toHaveBeenLastCalledWith('已有同名模板，请换一个模板名称')

    await deleteFirstProfile(wrapper, '删除')
    expect(deleteMock).toHaveBeenCalledWith(3)
    expect(showErrorMock).toHaveBeenLastCalledWith('模板不存在，可能已被其他管理员删除')
    expect(showSuccessMock).not.toHaveBeenCalled()
  })

  // 5xx 时后端只有通用的英文 "internal error"，网络错误也没有可用的原因：
  // 都显示本地化的“保存 / 删除模板失败”。
  it.each([
    ['a 500 from the backend', internalError],
    ['a network error', { status: 0, code: 'ERR_NETWORK', message: 'Network error. Please check your connection.' }]
  ])('falls back to the localized failure text for %s', async (_label, error) => {
    useZhMessages()
    listMock.mockResolvedValue([{ id: 3, name: 'chrome', enable_grease: false }])
    createMock.mockRejectedValue(error)
    deleteMock.mockRejectedValue(error)
    const wrapper = await mountOpenModal()

    await submitNewProfile(wrapper, 'defaults')
    expect(showErrorMock).toHaveBeenLastCalledWith('保存模板失败')

    await deleteFirstProfile(wrapper, '删除')
    expect(deleteMock).toHaveBeenCalledWith(3)
    expect(showErrorMock).toHaveBeenLastCalledWith('删除模板失败')
  })

  it('falls back to the generic message when the error carries none', async () => {
    listMock.mockResolvedValue([])
    createMock.mockRejectedValue({})
    const wrapper = await mountOpenModal()

    await submitNewProfile(wrapper, 'defaults')

    expect(showErrorMock).toHaveBeenCalledWith('admin.tlsFingerprintProfiles.saveFailed')
  })

  it('explains the key share / curves rule next to the key share field', async () => {
    listMock.mockResolvedValue([])
    const wrapper = await mountOpenModal()
    await wrapper.get('button.btn-primary.btn-sm').trigger('click')

    expect(wrapper.text()).toContain('admin.tlsFingerprintProfiles.form.keyShareGroupsHint')
  })

  it('explains that key_share needs supported_groups next to the extensions field', async () => {
    listMock.mockResolvedValue([])
    const wrapper = await mountOpenModal()
    await wrapper.get('button.btn-primary.btn-sm').trigger('click')

    expect(wrapper.text()).toContain('admin.tlsFingerprintProfiles.form.extensionsHint')
  })
})
