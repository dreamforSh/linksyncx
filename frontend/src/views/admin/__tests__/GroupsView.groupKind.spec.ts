import { defineComponent, reactive } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { routeLocationKey, routerKey } from 'vue-router'

import type { AdminGroup } from '@/types'
import GroupsView from '@/views/admin/GroupsView.vue'

const {
  listGroups,
  createGroup,
  updateGroup,
  getModelAllowlistCandidates,
  getUsageSummary,
  getCapacitySummary,
  getLiveCapability,
  showSuccess,
  showError
} = vi.hoisted(() => ({
  listGroups: vi.fn(),
  createGroup: vi.fn(),
  updateGroup: vi.fn(),
  getModelAllowlistCandidates: vi.fn(),
  getUsageSummary: vi.fn(),
  getCapacitySummary: vi.fn(),
  getLiveCapability: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn()
}))

const authState = vi.hoisted(() => ({ isSimpleMode: false }))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: {
      list: listGroups,
      create: createGroup,
      update: updateGroup,
      duplicate: vi.fn(),
      getModelAllowlistCandidates,
      getUsageSummary,
      getCapacitySummary,
      getLiveCapability,
      getAll: vi.fn(),
      delete: vi.fn(),
      updateSortOrder: vi.fn()
    },
    accounts: {
      list: vi.fn(),
      getById: vi.fn()
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => authState
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({
    isCurrentStep: vi.fn(() => false),
    nextStep: vi.fn()
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const baseGroup = {
  description: null,
  rate_multiplier: 1,
  rpm_limit: 0,
  status: 'active',
  subscription_type: 'standard',
  daily_limit_usd: null,
  weekly_limit_usd: null,
  monthly_limit_usd: null,
  claude_code_only: false,
  fallback_group_id: null,
  fallback_group_id_on_invalid_request: null,
  model_routing: null,
  model_routing_enabled: false,
  supported_model_scopes: [],
  account_count: 2,
  active_account_count: 2,
  rate_limited_account_count: 0,
  sort_order: 1,
  created_at: '2026-09-01T00:00:00Z',
  updated_at: '2026-09-01T00:00:00Z'
}

const channelGroup = {
  ...baseGroup,
  id: 42,
  name: 'claude-pool',
  platform: 'anthropic',
  is_exclusive: false,
  kind: 'channel'
} as unknown as AdminGroup

const managedGroup = {
  ...baseGroup,
  id: 43,
  name: 'acme-claude',
  platform: 'anthropic',
  is_exclusive: true,
  kind: 'managed',
  category: 'team',
  managed_type: 'quota'
} as unknown as AdminGroup

const DataTableStub = defineComponent({
  props: {
    data: { type: Array, default: () => [] },
    columns: { type: Array, default: () => [] },
    loading: { type: Boolean, default: false }
  },
  template: `
    <div>
      <div v-for="row in data" :key="row.id" :data-row="row.id">
        <slot name="cell-name" :row="row" :value="row.name" />
        <slot name="cell-account_count" :row="row" :value="row.account_count" />
        <slot name="cell-actions" :row="row" />
      </div>
    </div>
  `
})

const BaseDialogStub = defineComponent({
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const router = { push: vi.fn(), replace: vi.fn() }
let route = reactive({ query: {} as Record<string, string> })

function mountView() {
  return mount(GroupsView, {
    global: {
      provide: {
        [routerKey as symbol]: router,
        [routeLocationKey as symbol]: route
      },
      stubs: {
        AppLayout: { template: '<main><slot /></main>' },
        TablePageLayout: { template: '<section><slot name="filters" /><slot name="table" /><slot name="pagination" /></section>' },
        DataTable: DataTableStub,
        Pagination: true,
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        EmptyState: true,
        Select: true,
        PlatformIcon: true,
        Icon: true,
        GroupCapacityBadge: true,
        GroupRateMultipliersModal: true,
        GroupRPMOverridesModal: true,
        VueDraggable: true
      }
    }
  })
}

async function openCreateDialog(wrapper: ReturnType<typeof mountView>) {
  const button = wrapper.findAll('button').find(candidate => candidate.text() === 'admin.groups.createGroup')
  expect(button).toBeTruthy()
  await button!.trigger('click')
  await flushPromises()
}

describe('GroupsView group kinds', () => {
  beforeEach(() => {
    authState.isSimpleMode = false
    localStorage.clear()
    route = reactive({ query: {} })
    router.push.mockReset()
    router.replace.mockReset()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    for (const fn of [
      listGroups,
      createGroup,
      updateGroup,
      getModelAllowlistCandidates,
      getUsageSummary,
      getCapacitySummary,
      getLiveCapability,
      showSuccess,
      showError
    ]) {
      fn.mockReset()
    }
    listGroups.mockResolvedValue({ items: [channelGroup, managedGroup], total: 2, page: 1, page_size: 20, pages: 1 })
    createGroup.mockResolvedValue({ ...managedGroup, id: 99 })
    updateGroup.mockResolvedValue(managedGroup)
    getModelAllowlistCandidates.mockResolvedValue([])
    getUsageSummary.mockResolvedValue([])
    getCapacitySummary.mockResolvedValue([])
    getLiveCapability.mockResolvedValue({ supported: false })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('creates a managed group with its category and managed invariants', async () => {
    const wrapper = mountView()
    await flushPromises()
    await openCreateDialog(wrapper)

    expect(wrapper.find('[data-testid="group-category-field"]').exists()).toBe(false)
    await wrapper.get('[data-testid="group-kind-managed"] input').setValue(true)
    await wrapper.get('[data-testid="group-category-team"]').trigger('click')

    // 渠道专用配置对管理分组隐藏
    expect(wrapper.find('[data-tour="group-form-exclusive"]').exists()).toBe(false)

    await wrapper.get('[data-tour="group-form-name"]').setValue('acme-team')
    await wrapper.get('#create-group-form').trigger('submit')
    await flushPromises()

    expect(createGroup).toHaveBeenCalledWith(expect.objectContaining({
      name: 'acme-team',
      kind: 'managed',
      category: 'team',
      is_exclusive: true,
      subscription_type: 'standard',
      fallback_group_id: null,
      fallback_group_id_on_invalid_request: null,
      copy_accounts_from_group_ids: []
    }))
  })

  it('creates channel groups without a category', async () => {
    const wrapper = mountView()
    await flushPromises()
    await openCreateDialog(wrapper)

    await wrapper.get('[data-tour="group-form-name"]').setValue('pool')
    await wrapper.get('#create-group-form').trigger('submit')
    await flushPromises()

    const payload = createGroup.mock.calls[0]?.[0]
    expect(payload).toMatchObject({ name: 'pool', kind: 'channel' })
    expect(payload).not.toHaveProperty('category')
  })

  it('sends kind and category in simple mode', async () => {
    authState.isSimpleMode = true
    const wrapper = mountView()
    await flushPromises()
    await openCreateDialog(wrapper)

    await wrapper.get('[data-testid="group-kind-managed"] input').setValue(true)
    await wrapper.get('[data-tour="group-form-name"]').setValue('acme')
    await wrapper.get('#create-group-form').trigger('submit')
    await flushPromises()

    expect(createGroup).toHaveBeenCalledWith({
      name: 'acme',
      description: '',
      platform: 'anthropic',
      kind: 'managed',
      category: 'enterprise',
      managed_type: 'quota'
    })
  })

  it('creates a subscription group and hides the type for channel groups', async () => {
    const wrapper = mountView()
    await flushPromises()
    await openCreateDialog(wrapper)

    expect(wrapper.find('[data-testid="group-managed-type-field"]').exists()).toBe(false)
    await wrapper.get('[data-testid="group-kind-managed"] input').setValue(true)
    // 默认额度组
    expect((wrapper.get('[data-testid="group-managed-type-quota"] input').element as HTMLInputElement).checked).toBe(true)
    await wrapper.get('[data-testid="group-managed-type-subscription"] input').setValue(true)
    await wrapper.get('[data-tour="group-form-name"]').setValue('acme-pool')
    await wrapper.get('#create-group-form').trigger('submit')
    await flushPromises()

    expect(createGroup).toHaveBeenCalledWith(expect.objectContaining({
      name: 'acme-pool',
      kind: 'managed',
      managed_type: 'subscription'
    }))
  })

  it('marks managed rows and keeps them out of duplication', async () => {
    const wrapper = mountView()
    await flushPromises()

    const managedRow = wrapper.get('[data-row="43"]')
    expect(managedRow.text()).toContain('admin.groups.groupKind.managed')
    expect(managedRow.text()).toContain('admin.groups.groupKind.team')
    expect(managedRow.text()).toContain('admin.groups.groupKind.quota')
    expect(managedRow.find('[data-testid="group-duplicate"]').exists()).toBe(false)

    const channelRow = wrapper.get('[data-row="42"]')
    expect(channelRow.text()).not.toContain('admin.groups.groupKind.managed')
    expect(channelRow.find('[data-testid="group-duplicate"]').exists()).toBe(true)
  })

  it('links rows to the accounts page', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-row="43"] [data-testid="group-add-account"]').trigger('click')
    expect(router.push).toHaveBeenLastCalledWith({
      path: '/admin/accounts',
      query: { create: '1', group: '43' }
    })

    await wrapper.get('[data-row="42"] [data-testid="group-view-accounts"]').trigger('click')
    expect(router.push).toHaveBeenLastCalledWith({
      path: '/admin/accounts',
      query: { group: '42' }
    })
  })

  it('opens the create dialog from the route intent with the requested kind', async () => {
    route = reactive({ query: { create: '1', kind: 'managed', page: '2' } })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('#create-group-form').exists()).toBe(true)
    expect((wrapper.get('[data-testid="group-kind-managed"] input').element as HTMLInputElement).checked).toBe(true)
    expect(wrapper.find('[data-testid="group-category-field"]').exists()).toBe(true)
    expect(router.replace).toHaveBeenCalledWith({ query: { page: '2' } })
  })

  it('passes the kind filter to the list API', async () => {
    const wrapper = mountView()
    await flushPromises()

    const kindFilter = wrapper
      .findAllComponents({ name: 'Select' })
      .find(select => select.attributes('data-testid') === 'group-kind-filter')
    expect(kindFilter).toBeTruthy()
    kindFilter!.vm.$emit('update:modelValue', 'managed')
    kindFilter!.vm.$emit('change', 'managed')
    await flushPromises()

    expect(listGroups).toHaveBeenLastCalledWith(
      1,
      expect.any(Number),
      expect.objectContaining({ kind: 'managed' }),
      expect.anything()
    )
  })

  it('edits the category of a managed group and keeps it exclusive', async () => {
    const wrapper = mountView()
    await flushPromises()

    const editButton = wrapper.get('[data-row="43"]').findAll('button').find(button => button.text() === 'common.edit')
    await editButton!.trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="edit-group-kind"]').text()).toContain('admin.groups.groupKind.managed')
    await wrapper.get('[data-testid="group-category-enterprise"]').trigger('click')
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()

    expect(updateGroup).toHaveBeenCalledWith(43, expect.objectContaining({
      category: 'enterprise',
      is_exclusive: true,
      subscription_type: 'standard',
      fallback_group_id: null
    }))
  })

  it('omits the category when editing a channel group', async () => {
    const wrapper = mountView()
    await flushPromises()

    const editButton = wrapper.get('[data-row="42"]').findAll('button').find(button => button.text() === 'common.edit')
    await editButton!.trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="group-category-field"]').exists()).toBe(false)
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()

    expect(updateGroup.mock.calls[0]?.[1]).not.toHaveProperty('category')
    expect(updateGroup.mock.calls[0]?.[1]).not.toHaveProperty('managed_type')
  })

  it('switches a managed group to a subscription group with a warning', async () => {
    const wrapper = mountView()
    await flushPromises()

    const editButton = wrapper.get('[data-row="43"]').findAll('button').find(button => button.text() === 'common.edit')
    await editButton!.trigger('click')
    await flushPromises()

    const field = wrapper.get('[data-testid="group-managed-type-field"]')
    expect(field.find('[data-testid="group-managed-type-warning"]').exists()).toBe(false)
    await field.get('[data-testid="group-managed-type-subscription"] input').setValue(true)
    expect(wrapper.get('[data-testid="group-managed-type-warning"]').text()).toContain('admin.groups.groupKind.changeToSubscription')
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()

    expect(updateGroup).toHaveBeenCalledWith(43, expect.objectContaining({ managed_type: 'subscription' }))
  })
})
