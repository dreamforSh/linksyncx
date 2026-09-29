import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import ClashView from '../ClashView.vue'
import Select from '@/components/common/Select.vue'
import type { ClashNode, ClashProfile, ClashRuntimeStatus } from '@/types'
import { makeClashPoolSettings } from '@/__tests__/fixtures/clash'

const api = vi.hoisted(() => ({
  getRuntime: vi.fn(),
  listProfiles: vi.fn(),
  getSettings: vi.fn(),
  listNodes: vi.fn(),
  listNodeIds: vi.fn(),
  listExits: vi.fn(),
  refreshProfile: vi.fn(),
  updateProfile: vi.fn(),
  deleteProfile: vi.fn(),
  createProfile: vi.fn(),
  previewProfile: vi.fn(),
  resyncRuntime: vi.fn(),
  testNodesLatency: vi.fn(),
  probeNodesExit: vi.fn(),
  enableNode: vi.fn(),
  disableNode: vi.fn(),
  updateNodes: vi.fn(),
  acceptNodeExit: vi.fn(),
  updateSettings: vi.fn(),
  getAllProxies: vi.fn()
}))
const accountsApi = vi.hoisted(() => ({ list: vi.fn(), update: vi.fn(), setSchedulable: vi.fn() }))
const toast = vi.hoisted(() => ({ showSuccess: vi.fn(), showError: vi.fn(), showWarning: vi.fn(), showInfo: vi.fn() }))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    clash: api,
    proxies: { getAll: api.getAllProxies },
    accounts: accountsApi
  }
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => toast }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key} ${JSON.stringify(params)}` : key)
  })
}))

enableAutoUnmount(afterEach)

const DialogStub = {
  props: ['show', 'title'],
  template: '<div v-if="show" data-test="dialog" :data-title="title"><slot /><slot name="footer" /></div>'
}

const runtime = (mode: ClashRuntimeStatus['mode'] = 'embedded'): ClashRuntimeStatus => ({
  mode,
  enabled: mode !== 'disabled',
  local: {
    instance_id: 'node-a',
    mode,
    ready: mode !== 'disabled',
    version: 'mihomo v1.19',
    config_hash: 'abcdef0123456789',
    listeners: 3,
    listener_failures: 1,
    last_applied_at: '2026-09-25T00:00:00Z',
    last_error: '',
    updated_at: '2026-09-25T00:00:00Z'
  },
  instances: []
})

function profile(overrides: Partial<ClashProfile> = {}): ClashProfile {
  return {
    id: 1,
    name: 'Airport A',
    url_masked: 'https://sub.example/a?token=****',
    user_agent: 'clash.meta',
    enabled: true,
    refresh_interval_minutes: 360,
    include_pattern: '',
    exclude_pattern: '',
    fetch_proxy_id: null,
    notes: '',
    last_refresh_at: '2026-09-25T00:00:00Z',
    last_refresh_status: 'ok',
    last_refresh_error: '',
    last_format: 'clash_yaml',
    upload_bytes: 1,
    download_bytes: 1,
    total_bytes: 0,
    expire_at: null,
    node_count: 3,
    stats: { total: 3, active: 2, healthy: 2, unhealthy: 1, missing: 0, invalid: 0, disabled: 1, bound: 1, bound_accounts: 1 },
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-01T00:00:00Z',
    ...overrides
  }
}

function node(overrides: Partial<ClashNode> = {}): ClashNode {
  return {
    id: 11,
    profile_id: 1,
    profile_name: 'Airport A',
    name: 'HK 01',
    type: 'vmess',
    server: 'hk.example',
    server_port: 443,
    status: 'active',
    status_reason: '',
    missing_since: null,
    listen_port: 42001,
    proxy_id: 101,
    health_status: 'healthy',
    latency_ms: 40,
    consecutive_failures: 0,
    last_checked_at: null,
    last_check_error: '',
    exit_ip: '1.2.3.4',
    exit_country: 'Hong Kong',
    exit_country_code: 'HK',
    exit_region: '',
    exit_city: '',
    exit_status: 'ok',
    exit_pending_ip: '',
    exit_checked_at: null,
    exit_changed_at: null,
    platform_checks: { checked_at: null, results: { openai: 'pass', anthropic: 'fail' } },
    available: true,
    unavailable_reason: '',
    accounts: [],
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-01T00:00:00Z',
    ...overrides
  }
}

const traffic = (overrides: Partial<NonNullable<ClashNode['traffic']>> = {}): NonNullable<ClashNode['traffic']> => ({
  upload_bytes: 3 * 1024 * 1024,
  download_bytes: 7 * 1024 * 1024,
  today_upload_bytes: 1024 * 1024,
  today_download_bytes: 1024 * 1024,
  daily: ['2026-09-21', '2026-09-22', '2026-09-23', '2026-09-24', '2026-09-25', '2026-09-26', '2026-09-27'].map((date, index) => ({
    date,
    upload_bytes: index * 1024,
    download_bytes: index * 4096
  })),
  updated_at: '2026-09-27T00:00:00Z',
  upload_rate: 2048,
  download_rate: 10 * 1024,
  connections: 2,
  ...overrides
})

// Fresh objects per test: batch results update the listed nodes in place.
const makeNodes = () => [
  node({
    id: 11,
    name: 'HK 01',
    accounts: [{ id: 5, name: 'claude-main', platform: 'anthropic', is_shadow: false }],
    traffic: traffic()
  }),
  node({ id: 12, name: 'SG 01', exit_ip: '5.6.7.8', exit_status: 'changed', exit_pending_ip: '9.9.9.9', available: false }),
  node({ id: 13, name: 'KR 01', status: 'disabled', status_reason: 'disabled by admin', available: false })
]

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  api.getRuntime.mockResolvedValue(runtime())
  api.listProfiles.mockResolvedValue([profile()])
  api.getSettings.mockResolvedValue(makeClashPoolSettings())
  const nodes = makeNodes()
  api.listNodes.mockResolvedValue({ items: nodes, total: nodes.length, page: 1, page_size: 20, pages: 1 })
  api.listExits.mockResolvedValue({ max_accounts_per_exit: 1, allow_unprobed_exit_binding: false, exits: [] })
  api.getAllProxies.mockResolvedValue([])
  accountsApi.list.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 8, pages: 0 })
})

const mountView = async () => {
  const wrapper = mount(ClashView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        BaseDialog: DialogStub,
        Icon: true,
        CountryFlag: true
      }
    }
  })
  await flushPromises()
  return wrapper
}

type Wrapper = Awaited<ReturnType<typeof mountView>>

const dialog = (wrapper: Wrapper, title: string) => {
  const found = wrapper.findAll('[data-test="dialog"]').find((item) => item.attributes('data-title') === title)
  if (!found) throw new Error(`dialog ${title} is not open`)
  return found
}

const clickDialogButton = async (wrapper: Wrapper, title: string, text: string) => {
  const button = dialog(wrapper, title).findAll('button').find((item) => item.text() === text)
  if (!button) throw new Error(`button ${text} not found in ${title}`)
  await button.trigger('click')
}

describe('ClashView', () => {
  it('renders runtime, stats, subscriptions and nodes', async () => {
    const wrapper = await mountView()

    expect(api.getRuntime).toHaveBeenCalled()
    expect(api.listNodes).toHaveBeenCalledWith(1, expect.any(Number), expect.objectContaining({ profile_id: undefined }), expect.anything())
    expect(wrapper.get('[data-testid="clash-runtime-status"]').text()).toContain('admin.clash.runtime.ready')
    expect(wrapper.text()).toContain('mihomo v1.19')
    expect(wrapper.text()).toContain('admin.clash.runtime.listenerFailures {"count":1}')
    expect(wrapper.get('[data-testid="clash-profile-table"]').text()).toContain('Airport A')
    expect(wrapper.get('[data-testid="clash-profile-refresh-status"]').text()).toContain('admin.clash.profiles.refreshStatus.ok')
    // total_bytes = 0 hides the traffic bar
    expect(wrapper.find('[data-testid="clash-profile-traffic"]').exists()).toBe(false)
    const panel = wrapper.get('[data-testid="clash-nodes-panel"]')
    expect(panel.text()).toContain('HK 01')
    expect(panel.text()).toContain('claude-main')
    expect(panel.get('[data-testid="clash-node-exit-changed"]').text()).toContain('admin.clash.nodes.pendingExit {"ip":"9.9.9.9"}')
    expect(wrapper.find('[data-testid="clash-disabled-banner"]').exists()).toBe(false)
  })

  it('shows the disabled banner and blocks write actions when the pool is off', async () => {
    api.getRuntime.mockResolvedValue(runtime('disabled'))
    const wrapper = await mountView()

    expect(wrapper.find('[data-testid="clash-disabled-banner"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="clash-add-profile"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="clash-profile-refresh"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="clash-runtime-resync"]').attributes('disabled')).toBeDefined()
  })

  it('offers a forced refresh when drop protection skips an update', async () => {
    api.refreshProfile
      .mockResolvedValueOnce({ profile_id: 1, status: 'skipped', error: 'node count dropped', parsed: 1, filtered: 0, private: 0, inserted: 0, updated: 0, missing: 0 })
      .mockResolvedValueOnce({ profile_id: 1, status: 'ok', parsed: 3, filtered: 0, private: 0, inserted: 0, updated: 3, missing: 0 })
    const wrapper = await mountView()

    await wrapper.get('[data-testid="clash-profile-refresh"]').trigger('click')
    await flushPromises()
    expect(api.refreshProfile).toHaveBeenNthCalledWith(1, 1, { force: false })
    expect(dialog(wrapper, 'admin.clash.refresh.forceTitle').text()).toContain('node count dropped')

    await clickDialogButton(wrapper, 'admin.clash.refresh.forceTitle', 'admin.clash.actions.forceRefresh')
    await flushPromises()
    expect(api.refreshProfile).toHaveBeenNthCalledWith(2, 1, { force: true })
    expect(toast.showSuccess).toHaveBeenCalledWith(expect.stringContaining('admin.clash.refresh.success'))
  })

  it('lists bound accounts and allows a forced delete when the subscription is in use', async () => {
    api.deleteProfile
      .mockRejectedValueOnce({ status: 409, reason: 'CLASH_PROFILE_IN_USE', message: 'in use', metadata: { accounts: 'acc-a, acc-b' } })
      .mockResolvedValueOnce({ deleted: true })
    const wrapper = await mountView()

    await wrapper.get('[data-testid="clash-profile-delete"]').trigger('click')
    await clickDialogButton(wrapper, 'admin.clash.deleteDialog.title', 'common.delete')
    await flushPromises()
    expect(api.deleteProfile).toHaveBeenNthCalledWith(1, 1, { force: false })

    const inUse = wrapper.get('[data-testid="clash-delete-in-use"]')
    expect(inUse.text()).toContain('acc-a')
    expect(inUse.text()).toContain('acc-b')

    await wrapper.get('[data-testid="clash-force-delete"]').trigger('click')
    await flushPromises()
    expect(api.deleteProfile).toHaveBeenNthCalledWith(2, 1, { force: true })
    expect(wrapper.find('[data-testid="clash-delete-in-use"]').exists()).toBe(false)
  })

  it('asks before disabling a subscription whose nodes are bound', async () => {
    api.updateProfile.mockResolvedValue(profile({ enabled: false }))
    const wrapper = await mountView()

    await wrapper.get('[data-testid="clash-profile-table"] [role="switch"]').trigger('click')
    expect(api.updateProfile).not.toHaveBeenCalled()
    await clickDialogButton(wrapper, 'admin.clash.toggleDialog.title', 'admin.clash.actions.disable')
    await flushPromises()
    expect(api.updateProfile).toHaveBeenCalledWith(1, { enabled: false })
  })

  it('filters the node panel when viewing the nodes of a subscription', async () => {
    const wrapper = await mountView()
    api.listNodes.mockClear()

    await wrapper.get('[data-testid="clash-profile-view-nodes"]').trigger('click')
    await flushPromises()
    expect(api.listNodes).toHaveBeenLastCalledWith(1, expect.any(Number), expect.objectContaining({ profile_id: 1 }), expect.anything())
  })

  it('confirms node disabling with the accounts that will be paused', async () => {
    api.disableNode.mockResolvedValue({ enabled: false })
    const wrapper = await mountView()

    await wrapper.findAll('[data-testid="clash-node-disable"]')[0].trigger('click')
    expect(wrapper.get('[data-testid="clash-node-disable-bound"]').text()).toContain('claude-main')
    await clickDialogButton(wrapper, 'admin.clash.nodes.disableConfirm.title', 'admin.clash.actions.disable')
    await flushPromises()
    expect(api.disableNode).toHaveBeenCalledWith(11)
  })

  it('enables a disabled node directly', async () => {
    api.enableNode.mockResolvedValue({ enabled: true })
    const wrapper = await mountView()
    await wrapper.get('[data-testid="clash-node-enable"]').trigger('click')
    await flushPromises()
    expect(api.enableNode).toHaveBeenCalledWith(13)
  })

  it('accepts a pending exit change after confirmation', async () => {
    api.acceptNodeExit.mockResolvedValue({ accepted: true })
    const wrapper = await mountView()

    await wrapper.get('[data-testid="clash-node-accept-exit"]').trigger('click')
    const accept = dialog(wrapper, 'admin.clash.nodes.acceptConfirm.title')
    expect(accept.text()).toContain('"from":"5.6.7.8"')
    expect(accept.text()).toContain('"to":"9.9.9.9"')
    await clickDialogButton(wrapper, 'admin.clash.nodes.acceptConfirm.title', 'admin.clash.actions.acceptExit')
    await flushPromises()
    expect(api.acceptNodeExit).toHaveBeenCalledWith(12)
  })

  it.each([true, false])('runs manual latency and exit probes with automatic probes set to %s', async (enabled) => {
    api.getSettings.mockResolvedValue(makeClashPoolSettings({ automatic_probes_enabled: enabled }))
    api.testNodesLatency.mockResolvedValue([
      { node_id: 11, success: true, latency_ms: 30, health_status: 'healthy' },
      { node_id: 12, success: false, error: 'timeout', health_status: 'unhealthy' }
    ])
    api.probeNodesExit.mockResolvedValue([{ node_id: 11, success: true, exit_status: 'changed', exit_ip: '9.9.9.9' }])
    const wrapper = await mountView()

    const cards = wrapper.get('[data-testid="clash-nodes-panel"]').findAll('[data-testid="clash-node-select"]')
    await cards[0].setValue(true)
    await cards[1].setValue(true)
    expect(wrapper.get('[data-testid="clash-nodes-bulk-bar"]').text()).toContain('admin.clash.nodes.selectedCount {"count":2}')

    await wrapper.get('[data-testid="clash-nodes-bulk-latency"]').trigger('click')
    await flushPromises()
    expect(api.testNodesLatency).toHaveBeenCalledWith({ node_ids: [11, 12] })
    expect(toast.showWarning).toHaveBeenCalledWith('admin.clash.nodes.latencyDone {"success":1,"failed":1}')
    expect(wrapper.get('[data-testid="clash-nodes-batch-progress"]').text()).toContain('2/2')

    await wrapper.get('[data-testid="clash-nodes-bulk-probe"]').trigger('click')
    await flushPromises()
    expect(api.probeNodesExit).toHaveBeenCalledWith({ node_ids: [11, 12] })
    expect(toast.showWarning).toHaveBeenLastCalledWith(expect.stringContaining('admin.clash.nodes.probeChanged'))
  })

  it('selects rows in the list view and remembers the chosen view', async () => {
    const wrapper = await mountView()
    expect(wrapper.findAll('[data-testid="clash-node-card"]')).toHaveLength(3)

    await wrapper.get('[data-testid="clash-nodes-view-table"]').trigger('click')
    expect(localStorage.getItem('clash-nodes-view-mode')).toBe('table')
    expect(wrapper.find('[data-testid="clash-node-card"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="clash-node-traffic"]').text()).toContain('2 MB')

    const rows = wrapper.get('[data-testid="clash-nodes-panel"]').findAll('input[data-test="select-row"]')
    await rows[0].setValue(true)
    expect(wrapper.get('[data-testid="clash-nodes-bulk-bar"]').text()).toContain('admin.clash.nodes.selectedCount {"count":1}')

    const again = await mountView()
    expect(again.find('[data-testid="clash-node-card"]').exists()).toBe(false)
  })

  it('shows traffic and live connections on node cards', async () => {
    const wrapper = await mountView()
    const card = wrapper.findAll('[data-testid="clash-node-card"]')[0]
    expect(card.get('[data-testid="clash-node-traffic-today"]').text()).toContain('2 MB')
    expect(card.get('[data-testid="clash-node-traffic-total"]').text()).toContain('10 MB')
    expect(card.get('[data-testid="clash-node-traffic-live"]').text()).toContain('10 KB/s')
    expect(card.get('[data-testid="clash-node-occupancy"]').text()).toBe('1/1')

    const idle = wrapper.findAll('[data-testid="clash-node-card"]')[1]
    expect(idle.get('[data-testid="clash-node-traffic-today"]').text()).toContain('0 B')
    expect(idle.text()).toContain('admin.clash.nodes.traffic.noTraffic')
  })

  it('runs a full check over every filtered node', async () => {
    api.listNodeIds.mockResolvedValue([11, 14, 15])
    api.testNodesLatency.mockResolvedValue([
      { node_id: 11, success: true, latency_ms: 30, health_status: 'healthy' },
      { node_id: 14, success: false, error: 'timeout', health_status: 'unknown' },
      { node_id: 15, success: true, latency_ms: 50, health_status: 'healthy' }
    ])
    api.probeNodesExit.mockResolvedValue([
      { node_id: 11, success: true, exit_status: 'ok', exit_ip: '1.2.3.4' },
      { node_id: 15, success: true, exit_status: 'ok', exit_ip: '1.2.3.5' }
    ])
    const wrapper = await mountView()
    api.listNodes.mockClear()

    await wrapper.get('[data-testid="clash-nodes-batch-menu"]').trigger('click')
    await wrapper.get('[data-testid="clash-nodes-batch-full"]').trigger('click')
    await flushPromises()

    expect(api.listNodeIds).toHaveBeenCalledWith(expect.objectContaining({ profile_id: undefined }), { live: true })
    expect(api.testNodesLatency).toHaveBeenCalledWith({ node_ids: [11, 14, 15] })
    expect(api.probeNodesExit).toHaveBeenCalledWith({ node_ids: [11, 15] })
    expect(toast.showWarning).toHaveBeenCalledWith('admin.clash.nodes.batch.fullDone {"success":2,"failed":1}')
    expect(api.listNodes).toHaveBeenCalled()
  })

  it('stops a batch test without starting new chunks', async () => {
    api.listNodeIds.mockResolvedValue(Array.from({ length: 40 }, (_, index) => index + 100))
    const pending: Array<() => void> = []
    api.testNodesLatency.mockImplementation(({ node_ids }: { node_ids: number[] }) =>
      new Promise((resolve) => {
        pending.push(() => resolve(node_ids.map((id) => ({ node_id: id, success: true, latency_ms: 10, health_status: 'healthy' }))))
      }))
    const wrapper = await mountView()

    await wrapper.get('[data-testid="clash-nodes-batch-menu"]').trigger('click')
    await wrapper.get('[data-testid="clash-nodes-batch-latency"]').trigger('click')
    await flushPromises()
    expect(api.testNodesLatency).toHaveBeenCalledTimes(3)

    await wrapper.get('[data-testid="clash-nodes-batch-stop"]').trigger('click')
    pending.forEach((resolve) => resolve())
    await flushPromises()
    expect(api.testNodesLatency).toHaveBeenCalledTimes(3)
    expect(toast.showInfo).toHaveBeenCalledWith('admin.clash.nodes.batch.stopped {"done":30,"total":40}')
  })

  it('binds and unbinds accounts from a node card', async () => {
    api.listExits.mockResolvedValue({
      max_accounts_per_exit: 2,
      allow_unprobed_exit_binding: false,
      exits: [{ node_id: 11, proxy_id: 101, node_name: 'HK 01', occupants: [{ id: 5, name: 'claude-main', platform: 'anthropic', is_shadow: false }] }]
    })
    accountsApi.list.mockResolvedValue({
      items: [
        { id: 7, name: 'gpt-team', platform: 'openai', proxy_id: null },
        { id: 8, name: 'claude-relay', platform: 'anthropic', proxy_id: null, custom_base_url_enabled: true },
        { id: 9, name: 'spark-shadow', platform: 'openai', proxy_id: null, parent_account_id: 7 }
      ],
      total: 3,
      page: 1,
      page_size: 8,
      pages: 1
    })
    accountsApi.update.mockResolvedValue({})
    accountsApi.setSchedulable.mockResolvedValue({})
    const wrapper = await mountView()

    await wrapper.findAll('[data-testid="clash-node-accounts"]')[0].trigger('click')
    await flushPromises()
    const dialogEl = wrapper.get('[data-testid="clash-node-bindings"]')
    expect(dialogEl.get('[data-testid="clash-bindings-current"]').text()).toContain('claude-main')
    expect(dialogEl.get('[data-testid="clash-bindings-occupancy"]').text()).toContain('{"used":1,"max":2}')
    expect(accountsApi.list).toHaveBeenCalledWith(1, 8, { search: undefined, lite: '1' })

    const candidates = dialogEl.findAll('[data-testid="clash-bindings-candidate"]')
    expect(candidates[1].attributes('disabled')).toBeDefined()
    expect(candidates[2].attributes('disabled')).toBeDefined()
    await candidates[0].setValue(true)
    await wrapper.get('[data-testid="clash-bindings-submit"]').trigger('click')
    await flushPromises()
    expect(accountsApi.update).toHaveBeenCalledWith(7, { proxy_id: 101 })
    expect(toast.showSuccess).toHaveBeenCalledWith('admin.clash.nodes.bindings.bound {"count":1}')

    await wrapper.findAll('[data-testid="clash-bindings-unbind"]')[0].trigger('click')
    expect(wrapper.get('[data-testid="clash-bindings-pause"]').element).toHaveProperty('checked', true)
    await wrapper.get('[data-testid="clash-bindings-unbind-submit"]').trigger('click')
    await flushPromises()
    expect(accountsApi.setSchedulable).toHaveBeenCalledWith(5, false)
    expect(accountsApi.update).toHaveBeenLastCalledWith(5, { proxy_id: 0 })
    expect(accountsApi.setSchedulable.mock.invocationCallOrder[0]).toBeLessThan(accountsApi.update.mock.invocationCallOrder[1])
    expect(toast.showSuccess).toHaveBeenLastCalledWith('admin.clash.nodes.bindings.unboundPaused {"name":"claude-main"}')
  })

  it('does not offer binding on unavailable nodes', async () => {
    const wrapper = await mountView()
    await wrapper.findAll('[data-testid="clash-node-accounts"]')[1].trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="clash-bindings-blocked"]').text()).toContain('admin.clash.nodes.bindings.unavailable')
    expect(wrapper.get('[data-testid="clash-bindings-submit"]').attributes('disabled')).toBeDefined()
  })

  it('hides a node after confirming, naming the accounts that will be paused', async () => {
    api.updateNodes.mockResolvedValue({ action: 'hide', updated: 1, skipped: 0, node_ids: [11] })
    const wrapper = await mountView()
    api.listNodes.mockClear()

    await wrapper.findAll('[data-testid="clash-node-hide"]')[0].trigger('click')
    expect(api.updateNodes).not.toHaveBeenCalled()
    const confirm = dialog(wrapper, 'admin.clash.nodes.actionConfirm.hide.titleOne')
    expect(confirm.text()).toContain('admin.clash.nodes.actionConfirm.hide.messageOne {"name":"HK 01"}')
    const bound = wrapper.get('[data-testid="clash-node-action-bound"]').text()
    expect(bound).toContain('admin.clash.nodes.actionConfirm.boundOne {"count":1}')
    expect(bound).toContain('claude-main')

    await clickDialogButton(wrapper, 'admin.clash.nodes.actionConfirm.hide.titleOne', 'admin.clash.actions.hide')
    await flushPromises()
    expect(api.updateNodes).toHaveBeenCalledWith('hide', [11])
    expect(toast.showSuccess).toHaveBeenCalledWith('admin.clash.nodes.actionDone.hide {"count":1}')
    expect(api.listNodes).toHaveBeenCalled()
  })

  it('applies bulk actions to the selection and offers unhide in the hidden view', async () => {
    api.updateNodes
      .mockResolvedValueOnce({ action: 'disable', updated: 1, skipped: 1, node_ids: [11] })
      .mockResolvedValueOnce({ action: 'unhide', updated: 1, skipped: 0, node_ids: [21] })
    const wrapper = await mountView()

    const selects = wrapper.get('[data-testid="clash-nodes-panel"]').findAll('[data-testid="clash-node-select"]')
    await selects[0].setValue(true)
    await selects[2].setValue(true)
    const bar = wrapper.get('[data-testid="clash-nodes-bulk-bar"]')
    expect(bar.find('[data-testid="clash-nodes-bulk-hide"]').exists()).toBe(true)
    expect(bar.find('[data-testid="clash-nodes-bulk-unhide"]').exists()).toBe(false)

    await bar.get('[data-testid="clash-nodes-bulk-disable"]').trigger('click')
    expect(dialog(wrapper, 'admin.clash.nodes.actionConfirm.disable.title').text())
      .toContain('admin.clash.nodes.actionConfirm.disable.messageMany {"count":2}')
    await clickDialogButton(wrapper, 'admin.clash.nodes.actionConfirm.disable.title', 'admin.clash.actions.disable')
    await flushPromises()
    expect(api.updateNodes).toHaveBeenLastCalledWith('disable', [11, 13])
    expect(toast.showSuccess).toHaveBeenLastCalledWith(
      'admin.clash.nodes.actionDone.disable {"count":1}admin.clash.nodes.actionSkipped {"count":1}'
    )
    expect(wrapper.find('[data-testid="clash-nodes-bulk-bar"]').exists()).toBe(false)

    // The hidden view lists hidden nodes with an unhide action instead of hide.
    const hiddenNode = node({ id: 21, name: 'US 09', status: 'disabled', status_reason: 'hidden by admin', hidden: true, available: false })
    api.listNodes.mockResolvedValue({ items: [hiddenNode], total: 1, page: 1, page_size: 20, pages: 1 })
    const visibility = wrapper.findAllComponents(Select)
      .find((item) => item.attributes('data-testid') === 'clash-nodes-visibility-filter')
    if (!visibility) throw new Error('visibility filter not found')
    visibility.vm.$emit('update:modelValue', 'hidden')
    visibility.vm.$emit('change', 'hidden', null)
    await flushPromises()
    expect(api.listNodes).toHaveBeenLastCalledWith(1, expect.any(Number), expect.objectContaining({ visibility: 'hidden' }), expect.anything())
    const card = wrapper.get('[data-testid="clash-node-card"]')
    expect(card.find('[data-testid="clash-node-hidden-badge"]').exists()).toBe(true)
    expect(card.text()).toContain('admin.clash.reasons.nodeHidden')
    expect(card.find('[data-testid="clash-node-hide"]').exists()).toBe(false)
    expect(card.find('[data-testid="clash-node-enable"]').exists()).toBe(false)

    await card.get('[data-testid="clash-node-select"]').setValue(true)
    expect(wrapper.find('[data-testid="clash-nodes-bulk-hide"]').exists()).toBe(false)
    await card.get('[data-testid="clash-node-unhide"]').trigger('click')
    await flushPromises()
    expect(api.updateNodes).toHaveBeenLastCalledWith('unhide', [21])
    expect(toast.showSuccess).toHaveBeenLastCalledWith('admin.clash.nodes.actionDone.unhide {"count":1}')
  })

  it('shows local-file subscriptions and their hidden node count', async () => {
    api.listProfiles.mockResolvedValue([profile({
      source_type: 'file',
      source_name: 'airport.yaml',
      source_size: 2048,
      url_masked: '',
      refresh_interval_minutes: 0,
      stats: { total: 3, active: 2, healthy: 2, unhealthy: 0, missing: 0, invalid: 0, disabled: 1, hidden: 4, bound: 1, bound_accounts: 1 }
    })])
    const wrapper = await mountView()

    const table = wrapper.get('[data-testid="clash-profile-table"]')
    expect(table.get('[data-testid="clash-profile-file-source"]').text()).toContain('airport.yaml')
    expect(table.get('[data-testid="clash-profile-hidden"]').text()).toContain('admin.clash.profiles.hiddenCount {"count":4}')
    expect(table.get('[data-testid="clash-profile-refresh"]').attributes('title')).toBe('admin.clash.actions.reparse')
  })

  it('uploads a local Clash file as a new subscription', async () => {
    const content = 'proxies:\n  - {name: HK, type: ss, server: hk.example, port: 1, cipher: aes-128-gcm, password: p}\n'
    api.previewProfile.mockResolvedValue({ format: 'clash_yaml', node_count: 1, usable: 1, nodes: [], skipped: [] })
    api.createProfile.mockResolvedValue({
      profile: profile({ id: 2, name: 'airport', source_type: 'file', source_name: 'airport.yaml' }),
      refresh: { profile_id: 2, status: 'ok', parsed: 1, filtered: 0, private: 0, inserted: 1, updated: 0, missing: 0 }
    })
    const wrapper = await mountView()

    await wrapper.get('[data-testid="clash-add-profile"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="clash-profile-source-file"]').trigger('click')
    expect(wrapper.find('[data-testid="clash-profile-url"]').exists()).toBe(false)
    expect(wrapper.find('#clash-profile-ua').exists()).toBe(false)

    // Submitting without a file is rejected locally.
    await wrapper.get('#clash-profile-form').trigger('submit')
    expect(wrapper.get('[data-testid="clash-profile-file-error"]').text()).toBe('admin.clash.form.validation.fileRequired')
    expect(api.createProfile).not.toHaveBeenCalled()

    const input = wrapper.get('[data-testid="clash-profile-file-input"]')
    const upload = new File([content], 'airport.yaml', { type: 'text/yaml' })
    Object.defineProperty(input.element, 'files', { value: [upload], configurable: true })
    await input.trigger('change')
    await vi.waitFor(() => expect(wrapper.find('[data-testid="clash-profile-file-name"]').exists()).toBe(true))
    expect(wrapper.get('[data-testid="clash-profile-file-name"]').text()).toBe('airport.yaml')
    expect((wrapper.get('#clash-profile-name').element as HTMLInputElement).value).toBe('airport')

    await wrapper.get('[data-testid="clash-preview-button"]').trigger('click')
    await flushPromises()
    expect(api.previewProfile).toHaveBeenCalledWith({
      source_type: 'file',
      content,
      include_pattern: undefined,
      exclude_pattern: undefined
    })

    await wrapper.get('#clash-profile-form').trigger('submit')
    await flushPromises()
    expect(api.createProfile).toHaveBeenCalledWith({
      name: 'airport',
      enabled: true,
      include_pattern: '',
      notes: '',
      source_type: 'file',
      content,
      source_name: 'airport.yaml'
    })
    expect(wrapper.get('[data-testid="clash-create-result"]').text()).toContain('admin.clash.createResult.fileParsed')
  })

  it('turns a node link pasted into the subscription URL box into a links subscription', async () => {
    const ssLink =
      'ss://YWVzLTEyOC1nY206dGVzdC1wYXNzd29yZA@us.example.com:13277?plugin=obfs-local%3Bobfs%3Dhttp%3Bobfs-host%3Dcdn.example.com' +
      '#%F0%9F%87%BA%F0%9F%87%B8%20%E7%BE%8E%E5%9B%BD-%E6%B4%9B%E6%9D%89%E7%9F%B6%2004'
    api.previewProfile.mockResolvedValue({ format: 'uri_list', node_count: 1, usable: 1, nodes: [], skipped: [] })
    api.createProfile.mockResolvedValue({
      profile: profile({ id: 3, name: '🇺🇸 美国-洛杉矶 04', source_type: 'links', url_masked: '' }),
      refresh: { profile_id: 3, status: 'ok', parsed: 1, filtered: 0, private: 0, inserted: 1, updated: 0, missing: 0 }
    })
    const wrapper = await mountView()

    await wrapper.get('[data-testid="clash-add-profile"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="clash-profile-url"]').trigger('paste', {
      clipboardData: { getData: () => `${ssLink}\n` }
    })
    await flushPromises()
    expect(wrapper.find('[data-testid="clash-profile-url"]').exists()).toBe(false)
    const links = wrapper.get('[data-testid="clash-profile-links-input"]')
    expect((links.element as HTMLTextAreaElement).value).toBe(ssLink)
    expect(wrapper.find('[data-testid="clash-profile-links-detected"]').exists()).toBe(true)
    expect((wrapper.get('#clash-profile-name').element as HTMLInputElement).value).toBe('🇺🇸 美国-洛杉矶 04')
    // No User-Agent, schedule or fetch proxy for pasted links.
    expect(wrapper.find('#clash-profile-ua').exists()).toBe(false)

    await wrapper.get('[data-testid="clash-preview-button"]').trigger('click')
    await flushPromises()
    expect(api.previewProfile).toHaveBeenCalledWith({
      source_type: 'links',
      content: ssLink,
      include_pattern: undefined,
      exclude_pattern: undefined
    })

    await wrapper.get('#clash-profile-form').trigger('submit')
    await flushPromises()
    expect(api.createProfile).toHaveBeenCalledWith({
      name: '🇺🇸 美国-洛杉矶 04',
      enabled: true,
      include_pattern: '',
      notes: '',
      source_type: 'links',
      content: ssLink
    })
    expect(wrapper.get('[data-testid="clash-create-result"]').text()).toContain('admin.clash.createResult.linksParsed')
  })

  it('offers the links source when a node link is typed as a subscription URL', async () => {
    const wrapper = await mountView()
    await wrapper.get('[data-testid="clash-add-profile"]').trigger('click')
    await flushPromises()

    await wrapper.get('#clash-profile-name').setValue('manual')
    await wrapper.get('[data-testid="clash-profile-url"]').setValue('trojan://pw@jp.example.com:443#JP%2001')
    await wrapper.get('#clash-profile-form').trigger('submit')
    expect(wrapper.get('[data-testid="clash-profile-url-error"]').text()).toContain('admin.clash.form.validation.urlIsNodeLink')
    expect(api.createProfile).not.toHaveBeenCalled()

    await wrapper.get('[data-testid="clash-profile-use-links"]').trigger('click')
    expect((wrapper.get('[data-testid="clash-profile-links-input"]').element as HTMLTextAreaElement).value)
      .toBe('trojan://pw@jp.example.com:443#JP%2001')
    // A name the admin typed is kept.
    expect((wrapper.get('#clash-profile-name').element as HTMLInputElement).value).toBe('manual')

    // Clearing the links asks for at least one.
    await wrapper.get('[data-testid="clash-profile-links-input"]').setValue('  ')
    await wrapper.get('#clash-profile-form').trigger('submit')
    expect(wrapper.get('[data-testid="clash-profile-links-error"]').text()).toBe('admin.clash.form.validation.linksRequired')
  })

  it('replaces the links of a links subscription and re-parses it', async () => {
    const linksProfile = profile({ source_type: 'links', url_masked: '', refresh_interval_minutes: 0, node_count: 2 })
    api.listProfiles.mockResolvedValue([linksProfile])
    api.updateProfile.mockResolvedValue(linksProfile)
    api.refreshProfile.mockResolvedValue({ profile_id: 1, status: 'ok', parsed: 1, filtered: 0, private: 0, inserted: 0, updated: 1, missing: 1 })
    const wrapper = await mountView()
    expect(wrapper.get('[data-testid="clash-profile-links-source"]').text()).toContain('admin.clash.profiles.linksSource')

    await wrapper.get('[data-testid="clash-profile-edit"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="clash-profile-source-links"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="clash-profile-links"]').text()).toContain('admin.clash.form.linksKeepHint {"count":2}')

    // Saving other fields keeps the stored links.
    await wrapper.get('#clash-profile-form').trigger('submit')
    await flushPromises()
    expect(api.updateProfile.mock.calls[0][1]).not.toHaveProperty('content')
    expect(api.refreshProfile).not.toHaveBeenCalled()

    await wrapper.get('[data-testid="clash-profile-edit"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="clash-profile-links-input"]').setValue('  vless://id@sg.example.com:443#SG  ')
    await wrapper.get('#clash-profile-form').trigger('submit')
    await flushPromises()
    expect(api.updateProfile).toHaveBeenLastCalledWith(1, expect.objectContaining({ content: 'vless://id@sg.example.com:443#SG' }))
    expect(api.updateProfile.mock.calls[1][1]).not.toHaveProperty('source_type')
    expect(api.refreshProfile).toHaveBeenCalledWith(1, { force: false })
  })

  it('re-parses a file subscription right after a new file is uploaded', async () => {
    const fileProfile = profile({ source_type: 'file', source_name: 'old.yaml', source_size: 10, url_masked: '', refresh_interval_minutes: 0 })
    api.listProfiles.mockResolvedValue([fileProfile])
    api.updateProfile.mockResolvedValue({ ...fileProfile, source_name: 'new.yaml' })
    api.refreshProfile.mockResolvedValue({ profile_id: 1, status: 'ok', parsed: 2, filtered: 0, private: 0, inserted: 1, updated: 1, missing: 0 })
    const wrapper = await mountView()

    await wrapper.get('[data-testid="clash-profile-edit"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="clash-profile-source-url"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="clash-profile-file"]').text()).toContain('admin.clash.form.fileKeepHint')

    const input = wrapper.get('[data-testid="clash-profile-file-input"]')
    Object.defineProperty(input.element, 'files', { value: [new File(['proxies: []'], 'new.yaml')], configurable: true })
    await input.trigger('change')
    await vi.waitFor(() => expect(wrapper.find('[data-testid="clash-profile-file-name"]').exists()).toBe(true))

    await wrapper.get('#clash-profile-form').trigger('submit')
    await flushPromises()
    expect(api.updateProfile).toHaveBeenCalledWith(1, expect.objectContaining({ content: 'proxies: []', source_name: 'new.yaml' }))
    expect(api.updateProfile.mock.calls[0][1]).not.toHaveProperty('url')
    expect(api.updateProfile.mock.calls[0][1]).not.toHaveProperty('refresh_interval_minutes')
    expect(api.refreshProfile).toHaveBeenCalledWith(1, { force: false })
  })

  it('shows failing checks as failures instead of never tested', async () => {
    const failing = node({
      id: 21,
      name: 'US 04',
      type: 'ss',
      health_status: 'unknown',
      latency_ms: null,
      consecutive_failures: 2,
      last_checked_at: '2026-09-28T00:00:00Z',
      last_check_error: 'dial tcp: lookup us04.example.com: no such host',
      exit_ip: '',
      exit_country: '',
      exit_country_code: '',
      exit_status: 'unknown',
      exit_checked_at: '2026-09-28T00:01:00Z',
      platform_checks: { checked_at: null, results: {} }
    })
    const unchecked = node({
      id: 22,
      name: 'US 05',
      health_status: 'unknown',
      latency_ms: null,
      exit_ip: '',
      exit_status: 'unknown',
      platform_checks: { checked_at: null, results: {} }
    })
    api.listNodes.mockResolvedValue({ items: [failing, unchecked], total: 2, page: 1, page_size: 20, pages: 1 })
    const wrapper = await mountView()

    const [failingCard, uncheckedCard] = wrapper.findAll('[data-testid="clash-node-card"]')
    expect(failingCard.get('[data-testid="clash-node-health"]').text()).toContain('admin.clash.health.failed')
    expect(failingCard.get('[data-testid="clash-node-check-error"]').text()).toBe('dial tcp: lookup us04.example.com: no such host')
    expect(failingCard.get('[data-testid="clash-node-exit-failed"]').text()).toBe('admin.clash.nodes.exitFailed')
    expect(uncheckedCard.get('[data-testid="clash-node-health"]').text()).toContain('admin.clash.health.unknown')
    expect(uncheckedCard.find('[data-testid="clash-node-check-error"]').exists()).toBe(false)
    expect(uncheckedCard.get('[data-testid="clash-node-exit"]').text()).toContain('admin.clash.nodes.exitUnprobed')

    await wrapper.get('[data-testid="clash-nodes-view-table"]').trigger('click')
    const row = wrapper.get('[data-testid="clash-nodes-panel"] tbody tr')
    expect(row.get('[data-testid="clash-node-health"]').text()).toContain('admin.clash.nodes.consecutiveFailures {"count":2}')
    expect(row.get('[data-testid="clash-node-check-error"]').text()).toBe('dial tcp: lookup us04.example.com: no such host')
    expect(row.get('[data-testid="clash-node-exit-failed"]').text()).toBe('admin.clash.nodes.exitFailed')
  })

  it('names the reason when testing a single node fails', async () => {
    api.testNodesLatency.mockResolvedValue([{ node_id: 11, success: false, error: 'dial tcp 1.2.3.4:443: i/o timeout', health_status: 'unknown' }])
    api.probeNodesExit.mockResolvedValue([{ node_id: 11, success: false, error: 'socks connect: general SOCKS server failure', exit_status: 'unknown' }])
    const wrapper = await mountView()
    const card = wrapper.findAll('[data-testid="clash-node-card"]')[0]

    await card.get('[data-testid="clash-node-latency"]').trigger('click')
    await flushPromises()
    expect(toast.showError).toHaveBeenCalledWith('admin.clash.nodes.latencyFailedDetail {"error":"dial tcp 1.2.3.4:443: i/o timeout"}')

    await card.get('[data-testid="clash-node-probe"]').trigger('click')
    await flushPromises()
    expect(toast.showError).toHaveBeenLastCalledWith('admin.clash.nodes.probeFailedDetail {"error":"socks connect: general SOCKS server failure"}')
  })

  it('shows friendly errors for Clash API failures', async () => {
    api.resyncRuntime.mockRejectedValue({ status: 503, reason: 'CLASH_RUNTIME_UNAVAILABLE', message: 'core down' })
    const wrapper = await mountView()
    await wrapper.get('[data-testid="clash-runtime-resync"]').trigger('click')
    await flushPromises()
    expect(toast.showError).toHaveBeenCalledWith('admin.clash.errors.runtimeUnavailable {"message":"core down"}')
  })
})

describe('Clash route', () => {
  it('registers an admin-only route with localized title and description', () => {
    const router = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../../../router/index.ts'), 'utf8')
    const start = router.indexOf("path: '/admin/clash'")
    expect(start).toBeGreaterThan(-1)
    const route = router.slice(start, start + 400)
    expect(route).toContain("component: () => import('@/views/admin/ClashView.vue')")
    expect(route).toContain('requiresAuth: true')
    expect(route).toContain('requiresAdmin: true')
    expect(route).toContain("titleKey: 'admin.clash.title'")
    expect(route).toContain("descriptionKey: 'admin.clash.description'")
  })
})
