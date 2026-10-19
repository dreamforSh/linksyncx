import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import type { PublicSettings } from '@/types'
import { useAppStore } from '@/stores/app'
import ChannelStatusView from '../ChannelStatusView.vue'
import ChannelStatusV1View from '../ChannelStatusV1View.vue'

const { getPublicSettings, list, route } = vi.hoisted(() => ({
  getPublicSettings: vi.fn(),
  list: vi.fn(),
  route: { query: {} as Record<string, string> },
}))

vi.mock('@/api/auth', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/auth')>()),
  getPublicSettings,
}))
vi.mock('@/api/channelMonitor', () => ({ list, status: vi.fn() }))
vi.mock('vue-router', () => ({ useRoute: () => route }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key }),
}))
// V2/V3 are heavy and irrelevant here; V1 stays real so its mount-time list request is observable.
vi.mock('../ChannelStatusV2View.vue', () => ({
  default: defineComponent({ name: 'ChannelStatusV2View', setup: () => () => h('div', { 'data-testid': 'v2' }) }),
}))
vi.mock('../ChannelStatusV3View.vue', () => ({
  default: defineComponent({ name: 'ChannelStatusV3View', setup: () => () => h('div', { 'data-testid': 'v3' }) }),
}))

const settings = (overrides: Partial<PublicSettings>) =>
  ({ channel_monitor_enabled: true, ...overrides }) as PublicSettings

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

const mountView = () =>
  mount(ChannelStatusView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        MonitorHero: true,
        MonitorCardGrid: true,
        MonitorDetailDialog: true,
      },
    },
  })

let wrapper: ReturnType<typeof mountView> | undefined

describe('ChannelStatusView mode switch', () => {
  beforeEach(() => {
    delete window.__APP_CONFIG__
    route.query = {}
    setActivePinia(createPinia())
    getPublicSettings.mockReset()
    list.mockReset().mockResolvedValue({ items: [] })
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })

  afterEach(() => {
    wrapper?.unmount()
    wrapper = undefined
    delete window.__APP_CONFIG__
    vi.restoreAllMocks()
  })

  it('mounts neither implementation while settings are pending, then V3 without touching V1', async () => {
    const pending = deferred<PublicSettings>()
    getPublicSettings.mockReturnValue(pending.promise)

    wrapper = mountView()
    await flushPromises()

    expect(wrapper.findComponent(ChannelStatusV1View).exists()).toBe(false)
    expect(list).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="v2"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="v3"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="channel-status-mode-loading"]').exists()).toBe(true)
    expect(getPublicSettings).toHaveBeenCalledTimes(1)

    pending.resolve(settings({ channel_monitor_mode: 'v2' }))
    await flushPromises()

    expect(wrapper.find('[data-testid="channel-status-mode-loading"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="v3"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="v2"]').exists()).toBe(false)
    expect(wrapper.findComponent(ChannelStatusV1View).exists()).toBe(false)
    expect(list).not.toHaveBeenCalled()
  })

  it('renders the injected mode on first paint without fetching', async () => {
    window.__APP_CONFIG__ = settings({ channel_monitor_mode: 'v2' })
    useAppStore().initFromInjectedConfig()

    wrapper = mountView()
    await flushPromises()

    expect(getPublicSettings).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="v3"]').exists()).toBe(true)
    expect(list).not.toHaveBeenCalled()
  })

  it('renders the legacy V2 page when monitor_view=v2 is requested in v2 mode', async () => {
    route.query = { monitor_view: 'v2' }
    getPublicSettings.mockResolvedValue(settings({ channel_monitor_mode: 'v2' }))

    wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-testid="v2"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="v3"]').exists()).toBe(false)
  })

  it('ignores monitor_view=v2 in v1 mode', async () => {
    route.query = { monitor_view: 'v2' }
    getPublicSettings.mockResolvedValue(settings({ channel_monitor_mode: 'v1' }))

    wrapper = mountView()
    await flushPromises()

    expect(wrapper.findComponent(ChannelStatusV1View).exists()).toBe(true)
    expect(wrapper.find('[data-testid="v2"]').exists()).toBe(false)
  })

  it.each([
    ['v1 mode', settings({ channel_monitor_mode: 'v1' })],
    ['a missing mode', settings({})],
  ])('renders V1 once loaded settings carry %s', async (_, loaded) => {
    getPublicSettings.mockResolvedValue(loaded)

    wrapper = mountView()
    await flushPromises()

    expect(wrapper.findComponent(ChannelStatusV1View).exists()).toBe(true)
    expect(wrapper.find('[data-testid="v2"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="v3"]').exists()).toBe(false)
    expect(list).toHaveBeenCalledTimes(1)
  })

  it('falls back to V1 when the settings request fails', async () => {
    getPublicSettings.mockRejectedValue(new Error('network down'))

    wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-testid="channel-status-mode-loading"]').exists()).toBe(false)
    expect(wrapper.findComponent(ChannelStatusV1View).exists()).toBe(true)
  })
})
