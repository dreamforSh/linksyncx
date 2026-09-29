import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import ClashSettingsDialog from '../ClashSettingsDialog.vue'
import { makeClashPoolSettings } from '@/__tests__/fixtures/clash'
import type { ClashPoolSettings } from '@/types'

const api = vi.hoisted(() => ({ getSettings: vi.fn(), updateSettings: vi.fn() }))
const toast = vi.hoisted(() => ({ showSuccess: vi.fn(), showError: vi.fn() }))

vi.mock('@/api/admin', () => ({ adminAPI: { clash: api } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => toast }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

enableAutoUnmount(afterEach)

beforeEach(() => {
  vi.resetAllMocks()
})

async function openDialog() {
  const wrapper = mount(ClashSettingsDialog, {
    props: { show: true },
    global: {
      stubs: {
        BaseDialog: {
          props: ['show'],
          template: '<div v-if="show"><slot /><slot name="footer" /></div>'
        },
        Select: true
      }
    }
  })
  await flushPromises()
  return wrapper
}

describe('ClashSettingsDialog automatic probes', () => {
  it('saves an explicit false and keeps it off after closing and reopening', async () => {
    let stored = makeClashPoolSettings()
    const original = { ...stored }
    api.getSettings.mockImplementation(async () => ({ ...stored }))
    api.updateSettings.mockImplementation(async (settings: ClashPoolSettings) => {
      stored = { ...settings }
      return { ...stored }
    })
    const wrapper = await openDialog()
    const toggle = '[data-testid="clash-setting-automatic_probes_enabled"]'

    expect(wrapper.get(toggle).attributes('aria-checked')).toBe('true')
    await wrapper.get(toggle).trigger('click')
    await wrapper.get('#clash-settings-form').trigger('submit')
    await flushPromises()

    const expected = { ...original, automatic_probes_enabled: false }
    expect(api.updateSettings).toHaveBeenCalledTimes(1)
    expect(api.updateSettings).toHaveBeenCalledWith(expected)
    expect(wrapper.emitted('saved')).toEqual([[expected]])
    expect(wrapper.emitted('close')).toHaveLength(1)
    expect(toast.showError).not.toHaveBeenCalled()

    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()

    expect(api.getSettings).toHaveBeenCalledTimes(2)
    expect(wrapper.get(toggle).attributes('aria-checked')).toBe('false')
    expect(wrapper.get('[data-testid="clash-setting-platform_checks_enabled"]').attributes('aria-checked')).toBe('true')
    await wrapper.get('#clash-settings-form').trigger('submit')
    await flushPromises()
    expect(api.updateSettings).toHaveBeenLastCalledWith(expected)
    expect(api.updateSettings).toHaveBeenCalledTimes(2)
  })

  it('defaults to on when an older settings response omits the field', async () => {
    const settings: Partial<ClashPoolSettings> = makeClashPoolSettings()
    delete settings.automatic_probes_enabled
    api.getSettings.mockResolvedValue(settings)

    const wrapper = await openDialog()

    expect(wrapper.get('[data-testid="clash-setting-automatic_probes_enabled"]').attributes('aria-checked')).toBe('true')
    expect(api.updateSettings).not.toHaveBeenCalled()
  })
})
