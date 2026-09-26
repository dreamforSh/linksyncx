import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ClashExitList } from '@/types'

const { createAccountMock, importCodexSessionMock, showErrorMock } = vi.hoisted(() => ({
  createAccountMock: vi.fn(),
  importCodexSessionMock: vi.fn(),
  showErrorMock: vi.fn()
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: showErrorMock, showSuccess: vi.fn(), showWarning: vi.fn() })
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isSimpleMode: true }) }))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      create: createAccountMock,
      checkMixedChannelRisk: vi.fn().mockResolvedValue({ has_risk: false }),
      importCodexSession: importCodexSessionMock
    },
    settings: {
      getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }),
      getSettings: vi.fn().mockResolvedValue({})
    },
    tlsFingerprintProfiles: { list: vi.fn().mockResolvedValue([]) }
  }
}))
vi.mock('@/api/admin/accounts', () => ({ getAntigravityDefaultModelMapping: vi.fn().mockResolvedValue([]) }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

import CreateAccountModal from '../CreateAccountModal.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const ProxySelectorStub = defineComponent({
  name: 'ProxySelector',
  props: ['modelValue', 'proxies', 'clashExits', 'platform', 'accountId'],
  emits: ['update:modelValue'],
  template: `<div>
    <button type="button" data-testid="pick-clash-exit" @click="$emit('update:modelValue', 101)">clash</button>
    <button type="button" data-testid="pick-manual-proxy" @click="$emit('update:modelValue', 7)">manual</button>
  </div>`
})

const OAuthFlowStub = defineComponent({
  name: 'OAuthAuthorizationFlow',
  props: ['showCodexSessionImportOption', 'initialInputMethod'],
  data: () => ({ inputMethod: 'manual' }),
  emits: ['import-codex-session', 'validate-refresh-token', 'cookie-auth'],
  template: '<div data-testid="oauth-flow" />'
})

const exits: ClashExitList = {
  max_accounts_per_exit: 1,
  allow_unprobed_exit_binding: false,
  exits: [
    {
      proxy_id: 101,
      node_id: 1,
      profile_id: 1,
      profile_name: 'Airport A',
      node_name: 'HK 01',
      type: 'vmess',
      status: 'active',
      health_status: 'healthy',
      latency_ms: 30,
      exit_ip: '1.2.3.4',
      exit_country: 'HK',
      exit_country_code: 'HK',
      exit_city: '',
      exit_status: 'ok',
      exit_pending_ip: '',
      exit_key: 'ip:1.2.3.4',
      available: true,
      unavailable_reason: '',
      occupants: [],
      platform_checks: { checked_at: null, results: {} }
    }
  ]
}

const targetGroup = { id: 99, name: 'route-all', platform: 'composite', kind: 'channel', status: 'active' }

function mountModal() {
  return mount(CreateAccountModal, {
    props: {
      show: true,
      proxies: [{ id: 7, name: 'manual', protocol: 'http', host: 'p.example', port: 8080 }] as never,
      groups: [],
      presetGroup: targetGroup as never,
      clashExits: exits
    },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        OAuthAuthorizationFlow: OAuthFlowStub,
        ProxySelector: ProxySelectorStub,
        ConfirmDialog: true,
        Select: true,
        Icon: true,
        PlatformIcon: true,
        ProxyAdBanner: true,
        GroupSelector: true,
        ModelWhitelistSelector: true,
        QuotaLimitCard: true
      }
    }
  })
}

async function openOpenAIOAuthStep(proxy: 'clash' | 'manual') {
  const wrapper = mountModal()
  await wrapper.findAll('button').find((button) => button.text().includes('OpenAI'))!.trigger('click')
  await wrapper.get(proxy === 'clash' ? '[data-testid="pick-clash-exit"]' : '[data-testid="pick-manual-proxy"]').trigger('click')
  await wrapper.get('form#create-account-form input[type="text"]').setValue('batch account')
  await wrapper.get('form#create-account-form').trigger('submit.prevent')
  await flushPromises()
  return wrapper
}

describe('CreateAccountModal — Clash exits', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    importCodexSessionMock.mockResolvedValue({ created: 1, updated: 0, skipped: 0, failed: 0, errors: [], warnings: [] })
  })

  it('passes the exits and the chosen platform to the proxy selector', async () => {
    const wrapper = mountModal()
    await wrapper.findAll('button').find((button) => button.text().includes('OpenAI'))!.trigger('click')
    const selector = wrapper.findComponent(ProxySelectorStub)
    expect(selector.props('clashExits')).toEqual(exits)
    expect(selector.props('platform')).toBe('openai')
  })

  it('blocks multi refresh-token creation on a Clash exit before any token exchange', async () => {
    const wrapper = await openOpenAIOAuthStep('clash')
    wrapper.findComponent(OAuthFlowStub).vm.$emit('validate-refresh-token', 'rt-one\nrt-two')
    await flushPromises()

    expect(showErrorMock).toHaveBeenCalledWith('admin.clash.errors.batchCreate')
    expect(createAccountMock).not.toHaveBeenCalled()
  })

  it('blocks a multi-entry Codex import on a Clash exit but allows a single entry', async () => {
    const wrapper = await openOpenAIOAuthStep('clash')
    const flow = wrapper.findComponent(OAuthFlowStub)

    flow.vm.$emit('import-codex-session', 'token-a\ntoken-b')
    await flushPromises()
    expect(showErrorMock).toHaveBeenCalledWith('admin.clash.errors.batchCreate')
    expect(importCodexSessionMock).not.toHaveBeenCalled()

    showErrorMock.mockClear()
    flow.vm.$emit('import-codex-session', 'token-a')
    await flushPromises()
    expect(showErrorMock).not.toHaveBeenCalledWith('admin.clash.errors.batchCreate')
    expect(importCodexSessionMock).toHaveBeenCalledTimes(1)
    expect(importCodexSessionMock.mock.calls[0][0]).toMatchObject({ content: 'token-a', proxy_id: 101 })
  })

  it('lets batches through with a manual proxy', async () => {
    const wrapper = await openOpenAIOAuthStep('manual')
    wrapper.findComponent(OAuthFlowStub).vm.$emit('import-codex-session', 'token-a\ntoken-b')
    await flushPromises()
    expect(showErrorMock).not.toHaveBeenCalledWith('admin.clash.errors.batchCreate')
    expect(importCodexSessionMock).toHaveBeenCalledWith(expect.objectContaining({ proxy_id: 7 }))
  })

  it('shows a friendly message when the server rejects the exit', async () => {
    importCodexSessionMock.mockRejectedValue({ status: 409, reason: 'CLASH_EXIT_OCCUPIED', message: 'busy', metadata: { accounts: 'acc-a' } })
    const wrapper = await openOpenAIOAuthStep('clash')
    wrapper.findComponent(OAuthFlowStub).vm.$emit('import-codex-session', 'token-a')
    await flushPromises()
    expect(showErrorMock).toHaveBeenCalledWith('admin.clash.errors.exitOccupiedWithAccounts')
  })
})
