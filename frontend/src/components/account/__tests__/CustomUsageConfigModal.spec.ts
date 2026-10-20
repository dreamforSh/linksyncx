import { describe, expect, it, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import CustomUsageConfigModal from '../CustomUsageConfigModal.vue'

const { getConfigMock, saveConfigMock, queryUsageMock } = vi.hoisted(() => ({
  getConfigMock: vi.fn(),
  saveConfigMock: vi.fn(),
  queryUsageMock: vi.fn()
}))

vi.mock('@/api/admin/customUsage', () => ({
  getConfig: getConfigMock,
  saveConfig: saveConfigMock,
  queryUsage: queryUsageMock
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

function mountModal() {
  return mount(CustomUsageConfigModal, {
    props: { show: true, account: { id: 5, name: 'relay', type: 'apikey', credentials: { base_url: 'https://relay.example.com/v1' } } as never },
    global: { stubs: { BaseDialog: BaseDialogStub, Toggle: true, Icon: true, CustomUsageResult: true } }
  })
}

describe('CustomUsageConfigModal', () => {
  beforeEach(() => {
    getConfigMock.mockReset()
    saveConfigMock.mockReset()
    queryUsageMock.mockReset()
    // GET returns literal query/header values redacted to empty strings.
    getConfigMock.mockResolvedValue({
      enabled: true, template: 'custom', base_url: 'https://dash.example.com', timeout_seconds: 10, interval_minutes: 10,
      request: { url: '{{baseUrl}}/usage?token=&uid={{userId}}', method: 'GET', headers: { Authorization: 'Bearer {{apiKey}}', 'X-Token': '' } },
      extractor: { remaining: { path: 'remaining' } },
      configured: true, has_api_key: true, has_access_token: false
    })
    saveConfigMock.mockResolvedValue({})
  })

  it('explains that hidden saved values are kept only for the same target host', async () => {
    const wrapper = mountModal()
    await flushPromises()
    const hint = wrapper.find('#custom-usage-secret-hint')
    expect(hint.exists()).toBe(true)
    expect(hint.text()).toBe('admin.accounts.customUsage.savedSecretsHint')
    expect(wrapper.find('#custom-usage-url').attributes('aria-describedby')).toBe('custom-usage-secret-hint')
    expect(wrapper.find('#custom-usage-headers').attributes('aria-describedby')).toBe('custom-usage-secret-hint')
  })

  it('sends redacted values back untouched so the server can keep them', async () => {
    const wrapper = mountModal()
    await flushPromises()
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(saveConfigMock).toHaveBeenCalledTimes(1)
    const [, config] = saveConfigMock.mock.calls[0]
    expect(config.request.url).toBe('{{baseUrl}}/usage?token=&uid={{userId}}')
    expect(config.request.headers).toEqual({ Authorization: 'Bearer {{apiKey}}', 'X-Token': '' })
    expect(config).not.toHaveProperty('api_key')
  })
})
