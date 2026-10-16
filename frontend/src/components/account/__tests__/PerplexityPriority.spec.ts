import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AdminAccountTestModal from '@/components/admin/account/AccountTestModal.vue'
import LegacyAccountTestModal from '../AccountTestModal.vue'
import ScheduledTestsPanel from '@/components/admin/account/ScheduledTestsPanel.vue'
import PerplexityOptionsFields from '../PerplexityOptionsFields.vue'
import { applyPerplexityOptions, defaultPerplexityOptions, readPerplexityOptions, validatePerplexityOptions } from '../credentialsBuilder'
import type { Account, PerplexityModelProbeResult } from '@/types'

const mocks = vi.hoisted(() => ({ models: vi.fn(), plans: vi.fn(), create: vi.fn(), update: vi.fn(), results: vi.fn(), success: vi.fn(), error: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { getAvailableModels: mocks.models }, scheduledTests: { listByAccount: mocks.plans, create: mocks.create, update: mocks.update, listResults: mocks.results, delete: vi.fn() } } }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: vi.fn() }) }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: mocks.success, showError: mocks.error }) }))
vi.mock('@/utils/format', () => ({ formatDateTime: (value: string) => value }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'en' } })
}))

const select = {
  props: ['modelValue', 'options', 'disabled'], emits: ['update:modelValue'],
  template: `<select :value="modelValue" :disabled="disabled" @change="$emit('update:modelValue', $event.target.value)"><option value=""></option><option v-for="option in options" :key="option.value ?? option.id" :value="option.value ?? option.id">{{ option.label ?? option.display_name ?? option.id }}</option></select>`
}
const global = { stubs: {
  BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' }, Select: select,
  Input: { props: ['modelValue'], emits: ['update:modelValue'], template: '<input :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />' },
  TextArea: true, Icon: true, Toggle: true, ConfirmDialog: true, HelpTooltip: { template: '<div><slot name="trigger" /></div>' }
} }
const account = { id: 72, name: 'Perplexity fixture', platform: 'perplexity', type: 'apikey', status: 'active' } as Account
function probe(verified = true): PerplexityModelProbeResult {
  return { requested_model: 'public', preference: 'gpt54', mode: 'copilot', status: verified ? 'verified' : 'substituted', verified, success: true,
    identity: { requested_preference: 'gpt54', actual_model: verified ? 'gpt54' : 'claude46sonnet', status: verified ? 'matched' : 'substituted', conflict: false },
    duration_ms: 42, observed_at_ms: 1, finished_at: '2026-10-16T00:00:00Z', identity_fingerprint: 'fixture', source: 'manual', persistence: 'stored' }
}
function stream(events: unknown[]) {
  const packets = events.map(event => new TextEncoder().encode(`data: ${JSON.stringify(event)}\n\n`))
  const cancel = vi.fn()
  let index = 0
  return { ok: true, body: { getReader: () => ({ read: async () => index < packets.length ? { done: false, value: packets[index++] } : { done: true }, cancel }) } } as unknown as Response
}
function clickNamed(wrapper: ReturnType<typeof mount>, name: string) {
  const button = wrapper.findAll('button').find(button => button.text().includes(name))
  expect(button, name).toBeTruthy()
  return button!.trigger('click')
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.models.mockResolvedValue([{ id: 'public', type: 'model', display_name: 'Public model', created_at: '' }])
  mocks.plans.mockResolvedValue([])
  mocks.create.mockResolvedValue({ id: 1 })
  mocks.update.mockResolvedValue({ id: 1 })
  mocks.results.mockResolvedValue([])
  vi.stubGlobal('localStorage', { getItem: () => 'fixture-token', setItem: vi.fn(), removeItem: vi.fn() })
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(stream([{ type: 'test_complete', success: true }])))
})

describe('Perplexity priority configuration', () => {
  it('round trips system preservation and exact query limits, including explicit zero', () => {
    const stored = { perplexity_options: { ...defaultPerplexityOptions(), keep_system_prompt: true, max_query_chars: 0 } }
    const options = readPerplexityOptions(stored)
    expect(options.keep_system_prompt).toBe(true)
    expect(options.max_query_chars).toBe(0)
    applyPerplexityOptions(stored, options)
    expect(stored.perplexity_options).toEqual(options)
    for (const value of [-1, 1, 999, 1000.5, 96001, Number.NaN]) {
      expect(validatePerplexityOptions({ ...options, max_query_chars: value })).toBe('invalidQueryChars')
    }
    expect(validatePerplexityOptions({ ...options, max_query_chars: 96000 })).toBeNull()
    expect(validatePerplexityOptions(readPerplexityOptions({ perplexity_options: { max_query_chars: '90000' } }))).toBe('invalidQueryChars')
  })
  it('shows the unlimited warning and emits integers without mutating parent options', async () => {
    const options = { ...defaultPerplexityOptions(), max_query_chars: 0 }
    const wrapper = mount(PerplexityOptionsFields, { props: { modelValue: options }, global })
    expect(wrapper.get('[data-testid=perplexity-unlimited-warning]').exists()).toBe(true)
    await wrapper.get('[data-testid=perplexity-max-query-chars]').setValue('24000')
    expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toMatchObject({ max_query_chars: 24000 })
    expect(options.max_query_chars).toBe(0)
  })
})

for (const [label, Modal] of [['admin', AdminAccountTestModal], ['legacy', LegacyAccountTestModal]] as const) {
  describe(`Perplexity ${label} model tests`, () => {
    it('does not invoke a model on open and defaults to session-only even with no model catalog', async () => {
      mocks.models.mockResolvedValue([])
      const wrapper = mount(Modal, { props: { show: false, account }, global })
      await wrapper.setProps({ show: true }); await flushPromises()
      expect(fetch).not.toHaveBeenCalled()
      expect(wrapper.get('[data-testid=perplexity-test-hint]').text()).toContain('sessionHint')
      await clickNamed(wrapper, 'admin.accounts.startTest'); await flushPromises()
      expect(JSON.parse((fetch as ReturnType<typeof vi.fn>).mock.calls[0][1].body)).toMatchObject({ mode: 'default', model_id: '' })
      expect(wrapper.text()).toContain('admin.accounts.testCompleted')
      await wrapper.get('[data-testid=perplexity-test-mode]').setValue('perplexity_model')
      expect(wrapper.text()).not.toContain('admin.accounts.testCompleted')
    })
    it('requires explicit model mode and presents substitution as unverified rather than success', async () => {
      vi.mocked(fetch).mockResolvedValue(stream([{ type: 'test_start', model: 'public' }, { type: 'perplexity_probe', data: probe(false) }, { type: 'content', text: 'OK' }, { type: 'test_complete', success: true }]))
      const wrapper = mount(Modal, { props: { show: false, account }, global })
      await wrapper.setProps({ show: true }); await flushPromises()
      await wrapper.get('[data-testid=perplexity-test-mode]').setValue('perplexity_model')
      expect(wrapper.get('[data-testid=perplexity-test-hint]').text()).toContain('costHint')
      await clickNamed(wrapper, 'admin.accounts.startTest'); await flushPromises()
      expect(JSON.parse((fetch as ReturnType<typeof vi.fn>).mock.calls[0][1].body)).toMatchObject({ mode: 'perplexity_model', model_id: 'public' })
      expect(wrapper.get('[data-testid=perplexity-probe-status]').text()).toContain('substituted')
      expect(wrapper.get('[data-testid=perplexity-actual-model]').text()).toBe('claude46sonnet')
      expect(wrapper.text()).toContain('admin.accounts.perplexity.probe.sendingProbe')
      expect(wrapper.text()).not.toContain('admin.accounts.sendingTestMessage')
      expect(wrapper.get('[data-testid=perplexity-probe-warning]').exists()).toBe(true)
    })
    it('does not accept a stream without a terminal test event', async () => {
      vi.mocked(fetch).mockResolvedValue(stream([{ type: 'content', text: 'partial' }]))
      const wrapper = mount(Modal, { props: { show: false, account }, global })
      await wrapper.setProps({ show: true }); await flushPromises()
      await clickNamed(wrapper, 'admin.accounts.startTest'); await flushPromises()
      expect(wrapper.text()).toContain('incompleteStream')
      expect(wrapper.text()).not.toContain('admin.accounts.testCompleted')
    })
    it('cancels on close and ignores a late result from the previous modal generation', async () => {
      let resolveFetch!: (value: Response) => void
      let signal!: AbortSignal
      vi.mocked(fetch).mockImplementation((_input, options) => {
        signal = options!.signal as AbortSignal
        return new Promise(resolve => { resolveFetch = resolve })
      })
      const wrapper = mount(Modal, { props: { show: false, account }, global })
      await wrapper.setProps({ show: true }); await flushPromises()
      await wrapper.get('[data-testid=perplexity-test-mode]').setValue('perplexity_model')
      await clickNamed(wrapper, 'admin.accounts.startTest')
      await wrapper.setProps({ show: false }); await flushPromises()
      expect(signal.aborted).toBe(true)
      await wrapper.setProps({ show: true }); await flushPromises()
      resolveFetch(stream([{ type: 'perplexity_probe', data: probe(false) }, { type: 'test_complete', success: true }]))
      await flushPromises()
      expect(wrapper.find('[data-testid=perplexity-probe-result]').exists()).toBe(false)
      expect(wrapper.get('[data-testid=perplexity-test-mode]').element).toHaveProperty('value', 'default')
    })
  })
}

describe('Perplexity scheduled probe controls', () => {
  it('creates session-only plans by default, and requires model selection for explicit probes', async () => {
    const wrapper = mount(ScheduledTestsPanel, { props: { show: false, accountId: 72, accountPlatform: 'perplexity', modelOptions: [{ value: 'public', label: 'Public model' }] }, global })
    await wrapper.setProps({ show: true }); await flushPromises()
    await clickNamed(wrapper, 'admin.scheduledTests.addPlan')
    await wrapper.get('input[placeholder="*/30 * * * *"]').setValue('7 9 * * *')
    await clickNamed(wrapper, 'common.save'); await flushPromises()
    expect(mocks.create.mock.calls[0][0]).toMatchObject({ test_mode: 'default', auto_recover: false, model_id: '' })
    await clickNamed(wrapper, 'admin.scheduledTests.addPlan')
    await wrapper.get('[data-testid=perplexity-test-mode]').setValue('perplexity_model')
    expect(wrapper.get('[data-testid=perplexity-test-hint]').text()).toContain('scheduleHint')
    const modelSelect = wrapper.findAll('select').find(select => !select.attributes('data-testid'))!
    await modelSelect.setValue('public')
    await wrapper.get('input[placeholder="*/30 * * * *"]').setValue('7 9 * * *')
    await clickNamed(wrapper, 'common.save'); await flushPromises()
    expect(mocks.create.mock.calls[1][0]).toMatchObject({ test_mode: 'perplexity_model', model_id: 'public', auto_recover: false })
  })
  it('renders identity metadata and a warning badge for nonmatching scheduled results', async () => {
    mocks.plans.mockResolvedValue([{ id: 1, account_id: 72, model_id: 'public', test_mode: 'perplexity_model', cron_expression: '7 9 * * *', enabled: false, auto_recover: false }])
    mocks.results.mockResolvedValue([{ id: 2, status: 'warning', response_text: 'OK', latency_ms: 42, started_at: '', metadata: { perplexity_probe: probe(false) } }])
    const wrapper = mount(ScheduledTestsPanel, { props: { show: false, accountId: 72, accountPlatform: 'perplexity', modelOptions: [] }, global })
    await wrapper.setProps({ show: true }); await flushPromises()
    await wrapper.get('.flex.cursor-pointer').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid=perplexity-probe-status]').text()).toContain('substituted')
    expect(wrapper.text()).toContain('warningStatus')
    expect(wrapper.get('[data-testid=perplexity-actual-model]').text()).toBe('claude46sonnet')
  })
})
