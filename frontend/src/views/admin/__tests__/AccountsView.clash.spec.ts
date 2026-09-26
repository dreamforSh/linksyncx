import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'
import type { ClashExitList } from '@/types'

const api = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  getById: vi.fn(),
  exportData: vi.fn(),
  listExits: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn()
}))
const toast = vi.hoisted(() => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn(), showWarning: vi.fn() }))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: api.listAccounts,
      listWithEtag: vi.fn(),
      getById: api.getById,
      exportData: api.exportData,
      getBatchTodayStats: vi.fn().mockResolvedValue({ stats: {} }),
      getUpstreamBillingProbeSettings: vi.fn().mockResolvedValue({ enabled: true, interval_minutes: 30 }),
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      toggleSchedulable: vi.fn()
    },
    clash: { listExits: api.listExits },
    proxies: { getAll: api.getAllProxies },
    groups: { getAll: api.getAllGroups }
  }
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => toast }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ token: 'test-token', isSimpleMode: false }) }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => (params ? `${key} ${JSON.stringify(params)}` : key)
    })
  }
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
      exit_ip: '103.151.172.20',
      exit_country: 'Hong Kong',
      exit_country_code: 'HK',
      exit_city: '',
      exit_status: 'ok',
      exit_pending_ip: '',
      exit_key: 'ip:103.151.172.20',
      available: true,
      unavailable_reason: '',
      occupants: [{ id: 1, name: 'clash-bound', platform: 'anthropic', is_shadow: false }],
      platform_checks: { checked_at: null, results: {} }
    }
  ]
}

const account = (id: number, name: string, proxy?: Record<string, unknown>) => ({
  id,
  name,
  platform: 'anthropic',
  type: 'oauth',
  status: 'active',
  schedulable: true,
  proxy_id: proxy ? proxy.id : null,
  proxy,
  group_ids: [],
  concurrency: 1,
  priority: 1,
  created_at: '2026-09-01T00:00:00Z',
  updated_at: '2026-09-01T00:00:00Z'
})

const rows = [
  account(1, 'clash-bound', { id: 101, name: 'Clash·Airport A·HK 01', protocol: 'socks5', host: '127.0.0.1', port: 42001, source: 'clash', expires_at: null }),
  account(2, 'manual-bound', { id: 7, name: 'Manual HK', protocol: 'http', host: 'hk.example', port: 8080, source: 'manual', expires_at: null })
]

const DataTableStub = {
  props: ['data'],
  template: `<div>
    <div v-for="row in data" :key="row.id" :data-row="row.id"><slot name="cell-proxy" :row="row" /></div>
  </div>`
}

function mountView() {
  return mount(AccountsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        DataTable: DataTableStub,
        AccountTableActions: { template: '<div><slot name="after" /></div>' },
        AccountTableFilters: true,
        AccountBulkActionsBar: true,
        Pagination: true,
        ConfirmDialog: true,
        AccountActionMenu: true,
        ImportDataModal: true,
        ReAuthAccountModal: true,
        AccountTestModal: true,
        AccountStatsModal: true,
        ScheduledTestsPanel: true,
        SyncFromCrsModal: true,
        TempUnschedStatusModal: true,
        ErrorPassthroughRulesModal: true,
        TLSFingerprintProfilesModal: true,
        CreateAccountModal: true,
        EditAccountModal: true,
        BulkEditAccountModal: true,
        TargetGroupPickerDialog: true,
        CountryFlag: true,
        HelpTooltip: true,
        Icon: true,
        Teleport: true
      }
    }
  })
}

describe('admin AccountsView — Clash exits', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.clearAllMocks()
    api.listAccounts.mockResolvedValue({ items: rows, total: rows.length, page: 1, page_size: 20, pages: 1 })
    api.listExits.mockResolvedValue(exits)
    api.getAllProxies.mockResolvedValue([])
    api.getAllGroups.mockResolvedValue([])
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('loads the exits on mount and hands them to the create/edit modals only', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(api.listExits).toHaveBeenCalledTimes(1)
    expect(wrapper.findComponent({ name: 'CreateAccountModal' }).props('clashExits')).toEqual(exits)
    expect(wrapper.findComponent({ name: 'EditAccountModal' }).props('clashExits')).toEqual(exits)
    expect(wrapper.findComponent({ name: 'BulkEditAccountModal' }).props()).not.toHaveProperty('clashExits')
  })

  it('shows the Clash tag, node and exit IP for accounts bound to an exit', async () => {
    const wrapper = mountView()
    await flushPromises()

    const clashCell = wrapper.get('[data-row="1"] [data-testid="account-clash-proxy"]')
    expect(clashCell.find('[data-testid="clash-tag"]').exists()).toBe(true)
    expect(clashCell.text()).toContain('HK 01')
    expect(clashCell.text()).toContain('103.151.172.20')
    expect(wrapper.get('[data-row="2"]').find('[data-testid="account-clash-proxy"]').exists()).toBe(false)
    expect(wrapper.get('[data-row="2"]').text()).toContain('Manual HK')
  })

  it('keeps working when the exits cannot be loaded', async () => {
    api.listExits.mockRejectedValue({ status: 404, message: 'not found' })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.findComponent({ name: 'CreateAccountModal' }).props('clashExits')).toBeNull()
    const clashCell = wrapper.get('[data-row="1"] [data-testid="account-clash-proxy"]')
    expect(clashCell.text()).toContain('Clash·Airport A·HK 01')
    expect(clashCell.text()).toContain('admin.clash.selector.exitUnprobed')
    expect(toast.showError).not.toHaveBeenCalled()
  })

  it('refreshes the exits when an account is opened for editing', async () => {
    api.getById.mockResolvedValue({ ...rows[0] })
    const wrapper = mountView()
    await flushPromises()
    api.listExits.mockClear()

    // Row actions live in the (stubbed) table; call the same handler they use.
    await (wrapper.vm as unknown as { handleEdit: (row: unknown) => Promise<void> }).handleEdit(rows[0])
    await flushPromises()
    expect(api.getById).toHaveBeenCalledWith(1)
    expect(api.listExits).toHaveBeenCalledTimes(1)

    wrapper.findComponent({ name: 'EditAccountModal' }).vm.$emit('updated', rows[0])
    await flushPromises()
    expect(api.listExits).toHaveBeenCalledTimes(2)
  })

  it('warns after export when Clash bindings were not exported', async () => {
    api.exportData.mockResolvedValue({ exported_at: 'now', proxies: [], accounts: [], skipped_clash_bindings: 2 })
    vi.stubGlobal('URL', { ...URL, createObjectURL: vi.fn(() => 'blob:x'), revokeObjectURL: vi.fn() })
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    const wrapper = mountView()
    await flushPromises()

    const exportDialog = wrapper.findAllComponents({ name: 'ConfirmDialog' }).find((dialog) => dialog.props('title') === 'admin.accounts.dataExport')!
    exportDialog.vm.$emit('confirm')
    await flushPromises()

    expect(api.exportData).toHaveBeenCalled()
    expect(click).toHaveBeenCalled()
    click.mockRestore()
    expect(toast.showWarning).toHaveBeenCalledWith('admin.accounts.dataExportedSkippedClash {"count":2}')
    expect(toast.showSuccess).not.toHaveBeenCalledWith('admin.accounts.dataExported')
  })
})
