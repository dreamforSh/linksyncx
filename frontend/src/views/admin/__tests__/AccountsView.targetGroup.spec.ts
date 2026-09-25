import { defineComponent, reactive } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { routeLocationKey, routerKey } from 'vue-router'

import AccountsView from '../AccountsView.vue'

const { listAccounts, countUngrouped, getAllGroups } = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  countUngrouped: vi.fn(),
  getAllGroups: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      countUngrouped,
      listWithEtag: vi.fn(),
      getBatchTodayStats: vi.fn().mockResolvedValue({ stats: {} }),
      getUpstreamBillingProbeSettings: vi.fn().mockResolvedValue({ enabled: true, interval_minutes: 30 }),
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      toggleSchedulable: vi.fn()
    },
    proxies: { getAll: vi.fn().mockResolvedValue([]) },
    groups: { getAll: getAllGroups }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ token: 'test-token', isSimpleMode: false })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const groups = [
  { id: 5, name: 'openai-pool', platform: 'openai', kind: 'channel', status: 'active' },
  { id: 7, name: 'acme-claude', platform: 'anthropic', kind: 'managed', category: 'team', status: 'active' }
]

const PickerStub = defineComponent({
  name: 'TargetGroupPickerDialog',
  props: {
    show: { type: Boolean, default: false },
    groups: { type: Array, default: () => [] },
    initialGroupId: { type: Number, default: null },
    zIndex: { type: Number, default: 50 }
  },
  emits: ['select', 'close', 'create-group'],
  template: '<div v-if="show" data-test="picker" />'
})

const CreateModalStub = defineComponent({
  name: 'CreateAccountModal',
  props: {
    show: { type: Boolean, default: false },
    presetGroup: { type: Object, default: null },
    groups: { type: Array, default: () => [] },
    proxies: { type: Array, default: () => [] }
  },
  emits: ['close', 'created', 'change-group'],
  template: '<div v-if="show" data-test="create-modal">{{ presetGroup && presetGroup.name }}</div>'
})

const ActionsStub = defineComponent({
  emits: ['create', 'refresh'],
  template: '<div><button data-test="create-account" @click="$emit(\'create\')" /><slot name="after" /></div>'
})

const router = { push: vi.fn(), replace: vi.fn() }
let route = reactive({ query: {} as Record<string, string> })

function mountView() {
  return mount(AccountsView, {
    global: {
      provide: {
        [routerKey as symbol]: router,
        [routeLocationKey as symbol]: route
      },
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
        },
        DataTable: true,
        AccountTableActions: ActionsStub,
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
        TargetGroupPickerDialog: PickerStub,
        CreateAccountModal: CreateModalStub,
        EditAccountModal: true,
        BulkEditAccountModal: true,
        PlatformTypeBadge: true,
        HelpTooltip: true,
        Icon: true,
        Teleport: true
      }
    }
  })
}

describe('admin AccountsView target group flow', () => {
  beforeEach(() => {
    localStorage.clear()
    route = reactive({ query: {} })
    router.push.mockReset()
    router.replace.mockReset()
    listAccounts.mockReset().mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
    countUngrouped.mockReset().mockResolvedValue(0)
    getAllGroups.mockReset().mockResolvedValue(groups)
  })

  it('asks for a target group before opening the account form', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="create-account"]').trigger('click')
    expect(wrapper.find('[data-test="picker"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="create-modal"]').exists()).toBe(false)

    wrapper.getComponent(PickerStub).vm.$emit('select', groups[0])
    await flushPromises()

    expect(wrapper.find('[data-test="picker"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="create-modal"]').text()).toBe('openai-pool')
  })

  it('reopens the picker above the form to change the target group', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="create-account"]').trigger('click')
    wrapper.getComponent(PickerStub).vm.$emit('select', groups[0])
    await flushPromises()

    wrapper.getComponent(CreateModalStub).vm.$emit('change-group')
    await flushPromises()

    const picker = wrapper.getComponent(PickerStub)
    expect(picker.props('show')).toBe(true)
    expect(picker.props('initialGroupId')).toBe(5)
    expect(picker.props('zIndex')).toBe(60)

    picker.vm.$emit('select', groups[1])
    await flushPromises()
    expect(wrapper.get('[data-test="create-modal"]').text()).toBe('acme-claude')
  })

  it('sends the admin to the groups page to create a missing group', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="create-account"]').trigger('click')

    wrapper.getComponent(PickerStub).vm.$emit('create-group', 'managed')
    await flushPromises()

    expect(router.push).toHaveBeenCalledWith({ path: '/admin/groups', query: { create: '1', kind: 'managed' } })
    expect(wrapper.find('[data-test="picker"]').exists()).toBe(false)
  })

  it('opens the form with the group from the route and filters the list by it', async () => {
    route = reactive({ query: { create: '1', group: '7' } })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-test="create-modal"]').text()).toBe('acme-claude')
    expect(listAccounts).toHaveBeenCalledWith(1, 20, expect.objectContaining({ group: '7' }), expect.anything())
    expect(router.replace).toHaveBeenCalledWith({ query: { group: '7' } })
  })

  it('falls back to the picker when the route group is unknown', async () => {
    route = reactive({ query: { create: '1', group: '999' } })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-test="create-modal"]').exists()).toBe(false)
    expect(wrapper.getComponent(PickerStub).props('initialGroupId')).toBe(999)
    expect(wrapper.find('[data-test="picker"]').exists()).toBe(true)
  })

  it('highlights ungrouped accounts and filters to them on demand', async () => {
    countUngrouped.mockResolvedValue(3)
    const wrapper = mountView()
    await flushPromises()

    const banner = wrapper.get('[data-testid="accounts-ungrouped-banner"]')
    expect(banner.text()).toContain('admin.accounts.ungroupedBanner')

    const viewButton = banner.findAll('button').find(button => button.text() === 'admin.accounts.ungroupedView')
    await viewButton!.trigger('click')
    await flushPromises()

    expect(listAccounts).toHaveBeenLastCalledWith(
      1,
      20,
      expect.objectContaining({ group: 'ungrouped' }),
      expect.anything()
    )
    expect(wrapper.find('[data-testid="accounts-ungrouped-banner"]').exists()).toBe(false)
  })

  it('keeps the banner hidden when the count is unavailable', async () => {
    countUngrouped.mockRejectedValue(new Error('boom'))
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-testid="accounts-ungrouped-banner"]').exists()).toBe(false)
  })
})
