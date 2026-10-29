import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, onMounted } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'
import Select from '@/components/common/Select.vue'

const api = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  getById: vi.fn(),
  getBatchUsage: vi.fn(),
  setSchedulable: vi.fn()
}))
const auth = vi.hoisted(() => ({ token: 'test-token', isSimpleMode: false }))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: api.listAccounts,
      listWithEtag: vi.fn(),
      getById: api.getById,
      getBatchUsage: api.getBatchUsage,
      getBatchTodayStats: vi.fn().mockResolvedValue({ stats: {} }),
      getUpstreamBillingProbeSettings: vi.fn().mockResolvedValue({ enabled: true, interval_minutes: 30 }),
      setSchedulable: api.setSchedulable,
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn()
    },
    proxies: { getAll: vi.fn().mockResolvedValue([]) },
    groups: { getAll: vi.fn().mockResolvedValue([]) }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn(), showWarning: vi.fn() })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => auth
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const account = (id: number, name: string) => ({
  id,
  name,
  platform: 'anthropic',
  type: 'oauth',
  status: 'active',
  schedulable: true,
  proxy_id: null,
  group_ids: [],
  concurrency: 1,
  priority: 1,
  created_at: '2026-09-01T00:00:00Z',
  updated_at: '2026-09-01T00:00:00Z'
})

const rows = [account(1, 'alpha'), account(2, 'beta')]

// Behaves like AccountUsageCell on a desktop viewport: asks the page for batched usage on mount.
const UsageCellStub = defineComponent({
  props: ['account', 'requestBatchedUsage', 'batchedUsage'],
  setup(props) {
    onMounted(() => props.requestBatchedUsage?.(props.account))
  },
  template: '<div data-test="usage-cell">{{ batchedUsage ? "usage-loaded" : "" }}</div>'
})

const BulkActionsBarStub = {
  props: ['selectedIds'],
  template: '<div data-test="bulk-bar">{{ selectedIds.join(",") }}</div>'
}

function mountView() {
  return mount(AccountsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        DataTable: { template: '<div data-test="data-table" />' },
        AccountTableActions: { template: '<div><slot name="before" /><slot name="after" /></div>' },
        AccountTableFilters: true,
        AccountBulkActionsBar: BulkActionsBarStub,
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
        PlatformTypeBadge: true,
        AccountCapacityCell: true,
        AccountStatusIndicator: true,
        AccountTodayStatsCell: true,
        AccountGroupsCell: true,
        AccountUsageCell: UsageCellStub,
        CustomUsageCell: true,
        UpstreamBillingRateCell: true,
        HelpTooltip: true,
        Icon: true,
        Teleport: true
      }
    }
  })
}

// The usage batch is flushed on a zero-delay timer after the cards mount.
async function settle() {
  await flushPromises()
  await new Promise((resolve) => setTimeout(resolve, 0))
  await flushPromises()
}

describe('admin AccountsView card view', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.clearAllMocks()
    auth.isSimpleMode = false
    api.listAccounts.mockResolvedValue({ items: rows, total: rows.length, page: 1, page_size: 20, pages: 1 })
    api.getById.mockImplementation(async (id: number) => ({ ...rows.find((row) => row.id === id) }))
    api.getBatchUsage.mockResolvedValue({ usage: { 1: { five_hour: null }, 2: { five_hour: null } }, errors: {} })
    api.setSchedulable.mockResolvedValue({ ...rows[0], schedulable: false })
  })

  it('defaults to the table and remembers switching views', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-test="data-table"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="account-card-grid"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="account-view-mode-table"]').attributes('aria-checked')).toBe('true')

    await wrapper.get('[data-testid="account-view-mode-card"]').trigger('click')
    expect(localStorage.getItem('account-view-mode')).toBe('card')
    expect(wrapper.find('[data-test="data-table"]').exists()).toBe(false)
    expect(wrapper.findAll('[data-testid="account-card"]')).toHaveLength(2)
    wrapper.unmount()

    const again = mountView()
    await flushPromises()
    expect(again.findAll('[data-testid="account-card"]')).toHaveLength(2)

    await again.get('[data-testid="account-view-mode-table"]').trigger('click')
    expect(localStorage.getItem('account-view-mode')).toBe('table')
    expect(again.find('[data-test="data-table"]').exists()).toBe(true)
    expect(again.find('[data-testid="account-card"]').exists()).toBe(false)
  })

  it('shows card sections according to the table column settings', async () => {
    localStorage.setItem('account-view-mode', 'card')
    const wrapper = mountView()
    await flushPromises()

    const card = wrapper.findAll('[data-testid="account-card"]')[0]
    // Fresh preferences: usage and groups are visible columns, today stats and proxy are hidden by default.
    expect(card.find('[data-testid="account-card-usage"]').exists()).toBe(true)
    expect(card.find('[data-testid="account-card-groups"]').exists()).toBe(true)
    expect(card.find('[data-testid="account-card-today-stats"]').exists()).toBe(false)
    expect(card.find('[data-testid="account-card-proxy"]').exists()).toBe(false)
    wrapper.unmount()

    // Simple mode has no groups column, so cards drop the section too.
    auth.isSimpleMode = true
    const simple = mountView()
    await flushPromises()
    expect(simple.find('[data-testid="account-card-groups"]').exists()).toBe(false)
    expect(simple.find('[data-testid="account-card-usage"]').exists()).toBe(true)
  })

  it('wires card selection and row actions to the page handlers', async () => {
    localStorage.setItem('account-view-mode', 'card')
    const wrapper = mountView()
    await flushPromises()

    const cards = wrapper.findAll('[data-testid="account-card"]')
    await cards[0].get('[data-testid="account-card-select"]').setValue(true)
    expect(wrapper.get('[data-test="bulk-bar"]').text()).toBe('1')
    await wrapper.get('[data-testid="account-cards-select-page"]').setValue(true)
    expect(wrapper.get('[data-test="bulk-bar"]').text()).toBe('1,2')

    await cards[1].get('[data-testid="account-card-more"]').trigger('click')
    const menu = wrapper.findComponent({ name: 'AccountActionMenu' })
    expect(menu.props('show')).toBe(true)
    expect(menu.props('account')).toMatchObject({ id: 2 })
    expect(menu.props('anchorRect')).not.toBeNull()

    await cards[1].get('[data-testid="account-card-delete"]').trigger('click')
    const deleteDialog = wrapper
      .findAllComponents({ name: 'ConfirmDialog' })
      .find((dialog) => dialog.props('title') === 'admin.accounts.deleteAccount')!
    expect(deleteDialog.props('show')).toBe(true)

    await cards[0].get('[data-testid="account-card-edit"]').trigger('click')
    await flushPromises()
    expect(api.getById).toHaveBeenCalledWith(1)
    expect(wrapper.findComponent({ name: 'EditAccountModal' }).props('show')).toBe(true)

    await cards[0].get('[data-testid="account-card-schedulable"]').trigger('click')
    await flushPromises()
    expect(api.setSchedulable).toHaveBeenCalledWith(1, false)
  })

  it('sorts from the card toolbar through the shared sort state', async () => {
    localStorage.setItem('account-view-mode', 'card')
    const wrapper = mountView()
    await flushPromises()

    wrapper.findComponent(Select).vm.$emit('update:modelValue', 'priority')
    await flushPromises()
    expect(api.listAccounts).toHaveBeenLastCalledWith(
      1,
      20,
      expect.objectContaining({ sort_by: 'priority', sort_order: 'asc' }),
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )
    // Stored in the DataTable's format so the table header shows the same sort.
    expect(JSON.parse(localStorage.getItem('account-table-sort') || '{}')).toEqual({ key: 'priority', order: 'asc' })

    await wrapper.get('[data-testid="account-cards-sort-order"]').trigger('click')
    await flushPromises()
    expect(api.listAccounts).toHaveBeenLastCalledWith(
      1,
      20,
      expect.objectContaining({ sort_by: 'priority', sort_order: 'desc' }),
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )
    expect(JSON.parse(localStorage.getItem('account-table-sort') || '{}')).toEqual({ key: 'priority', order: 'desc' })
  })

  it('offers the sortable columns the table header shows', async () => {
    localStorage.setItem('account-view-mode', 'card')
    localStorage.setItem('account-hidden-columns', JSON.stringify(['priority', 'today_stats']))
    localStorage.setItem('account-hidden-columns-version', 'scheduler-score-hidden-by-default')
    const wrapper = mountView()
    await flushPromises()

    const keys = (wrapper.findComponent(Select).props('options') as Array<{ value: string }>).map((option) => option.value)
    expect(keys).toContain('name')
    expect(keys).toContain('last_used_at')
    expect(keys).not.toContain('priority')
  })

  it('batches the usage requests of the card usage cells', async () => {
    localStorage.setItem('account-view-mode', 'card')
    const wrapper = mountView()
    await settle()

    expect(api.getBatchUsage).toHaveBeenCalledTimes(1)
    expect(api.getBatchUsage).toHaveBeenCalledWith([1, 2], false)
    expect(wrapper.findAll('[data-test="usage-cell"]').map((cell) => cell.text())).toEqual(['usage-loaded', 'usage-loaded'])
  })
})
