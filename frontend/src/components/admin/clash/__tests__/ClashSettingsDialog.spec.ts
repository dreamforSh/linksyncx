import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import ClashSettingsDialog from '../ClashSettingsDialog.vue'
import type { ClashPoolSettings } from '@/types'

const api = vi.hoisted(() => ({ getSettings: vi.fn(), updateSettings: vi.fn() }))
const toast = vi.hoisted(() => ({ showSuccess: vi.fn(), showError: vi.fn() }))

vi.mock('@/api/admin', () => ({ adminAPI: { clash: api } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => toast }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

enableAutoUnmount(afterEach)

const base = (): ClashPoolSettings => ({
  max_accounts_per_exit: 0,
  allow_unprobed_exit_binding: false,
  pause_automatic_probes: false,
  health_test_url: 'https://www.gstatic.com/generate_204',
  health_timeout_ms: 5000,
  bound_check_interval_seconds: 60,
  unbound_check_interval_seconds: 600,
  failure_threshold: 3,
  recovery_threshold: 2,
  exit_probe_interval_minutes: 720,
  exit_probe_per_minute: 30,
  platform_checks_enabled: true,
  exit_change_policy: 'pause',
  pause_ttl_minutes: 30,
  missing_retention_days: 7,
  drop_protection_percent: 50,
  default_user_agent: 'clash.meta'
})

beforeEach(() => vi.resetAllMocks())

async function openDialog() {
  const wrapper = mount(ClashSettingsDialog, {
    props: { show: true },
    global: {
      stubs: {
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
        Select: true
      }
    }
  })
  await flushPromises()
  return wrapper
}

const toggle = '[data-testid="clash-setting-pause_automatic_probes"]'

describe('ClashSettingsDialog automatic probes', () => {
  it('saves a pause and shows it after reopening', async () => {
    let stored = base()
    api.getSettings.mockImplementation(async () => ({ ...stored }))
    api.updateSettings.mockImplementation(async (s: ClashPoolSettings) => (stored = { ...s }))
    const wrapper = await openDialog()
    expect(wrapper.get(toggle).attributes('aria-checked')).toBe('false')
    await wrapper.get(toggle).trigger('click')
    await wrapper.get('#clash-settings-form').trigger('submit')
    await flushPromises()
    expect(api.updateSettings).toHaveBeenCalledWith({ ...base(), pause_automatic_probes: true })
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(wrapper.get(toggle).attributes('aria-checked')).toBe('true')
  })

  it('treats a response without the field as probing', async () => {
    const settings: Partial<ClashPoolSettings> = base()
    delete settings.pause_automatic_probes
    api.getSettings.mockResolvedValue(settings)
    const wrapper = await openDialog()
    expect(wrapper.get(toggle).attributes('aria-checked')).toBe('false')
    await wrapper.get('#clash-settings-form').trigger('submit')
    await flushPromises()
    expect(api.updateSettings).toHaveBeenCalledWith(expect.objectContaining({ pause_automatic_probes: false }))
  })
})
