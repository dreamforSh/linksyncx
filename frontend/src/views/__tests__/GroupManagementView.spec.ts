import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type DOMWrapper, type VueWrapper } from '@vue/test-utils'
import Select from '@/components/common/Select.vue'
import GroupManagementView from '../GroupManagementView.vue'

const api = vi.hoisted(() => ({
  getOverview: vi.fn(), adminDirectory: vi.fn(), getMembers: vi.fn(), getAccounts: vi.fn(), getSettings: vi.fn(),
  updateSettings: vi.fn(), addMember: vi.fn(), removeMember: vi.fn(), updateMemberLimit: vi.fn(),
  setMemberAccounts: vi.fn(), adminAssignManager: vi.fn(), adminRevokeManager: vi.fn(),
  updateCategory: vi.fn(), getOwnedUsers: vi.fn(), createGroupUser: vi.fn(), setGroupUserStatus: vi.fn(),
  resetGroupUserPassword: vi.fn(), transferBalance: vi.fn(), listTransfers: vi.fn()
}))
const appStore = vi.hoisted(() => ({ showSuccess: vi.fn(), showError: vi.fn() }))

vi.mock('@/api/groupManagement', () => ({ groupManagementAPI: api }))
vi.mock('@/stores/app', () => ({ useAppStore: () => appStore }))
vi.mock('vue-i18n', async importOriginal => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key, te: () => false })
}))

const group = { id: 2, name: 'Operations', manager: true, manager_user_ids: [], member_count: 1, account_count: 2, enabled: true, allocation_mode: 'manual', max_concurrent: 3, daily_limit: 100 }
const settings = { enabled: true, allocation_mode: 'manual', max_concurrent: 3, daily_limit: 100 }
const member = { user_id: 7, group_id: 2, group_name: 'Operations', username: 'Sam', email: 'sam@example.org', daily_limit: 10, daily_used: 2, max_concurrent: 2, daily_window_start: '' }
const pool = [
  { group_id: 2, id: 10, name: 'Zero', platform: 'openai', status: 'active', remaining_quota: null, assigned_user_ids: [7], assignment_mode: 'manual' },
  { group_id: 2, id: 11, name: 'Unlimited', platform: 'anthropic', status: 'inactive', remaining_quota: null, assigned_user_ids: [] }
]

function render() {
  return mount(GroupManagementView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        Icon: { template: '<span />' },
        RouterLink: { template: '<a><slot /></a>' },
        teleport: true
      }
    }
  })
}

function buttonIn(scope: VueWrapper | DOMWrapper<Element>, text: string) {
  const button = scope.findAll('button').find(item => item.text() === text)
  if (!button) throw new Error(`button "${text}" not found`)
  return button
}

beforeEach(() => {
  vi.clearAllMocks()
  api.getOverview.mockResolvedValue({ role: 'group_manager', manageable_groups: [group], memberships: [], assignments: [] })
  api.getMembers.mockResolvedValue([member])
  api.getAccounts.mockResolvedValue(pool)
  api.getSettings.mockResolvedValue({ group_id: 2, ...settings })
  api.updateSettings.mockResolvedValue(settings)
  api.addMember.mockResolvedValue(member)
  api.removeMember.mockResolvedValue(undefined)
  api.setMemberAccounts.mockResolvedValue(undefined)
  api.adminAssignManager.mockResolvedValue(undefined)
  api.adminRevokeManager.mockResolvedValue(undefined)
})

describe('GroupManagementView', () => {
  it('shows the manager pool without per-account quota and saves group defaults once edited', async () => {
    const wrapper = render()
    await flushPromises()

    const accountsPanel = wrapper.get('#gm-panel-accounts')
    expect(accountsPanel.findAll('th').map(th => th.text())).toEqual([
      'groupManagement.accounts.account',
      'groupManagement.accounts.platform',
      'groupManagement.accounts.status',
      'groupManagement.accounts.assignedTo'
    ])
    const rows = accountsPanel.findAll('tbody tr')
    expect(rows[0].text()).toContain('Zero')
    expect(rows[0].text()).toContain('Sam')
    expect(rows[1].text()).toContain('Unlimited')
    expect(rows[1].text()).toContain('groupManagement.accounts.unassigned')

    const form = wrapper.get('#gm-panel-settings form')
    const submit = form.get('button[type="submit"]')
    expect(submit.attributes('disabled')).toBeDefined()
    await form.get('#group-settings-2-daily').setValue('200')
    expect(submit.attributes('disabled')).toBeUndefined()
    await form.trigger('submit')
    await flushPromises()

    expect(api.updateSettings).toHaveBeenCalledWith(2, { enabled: true, allocation_mode: 'manual', max_concurrent: 3, daily_limit: 200 })
    expect(appStore.showSuccess).toHaveBeenCalledWith('groupManagement.toast.settingsSaved')
    wrapper.unmount()
  })

  it('asks for confirmation before switching to automatic allocation clears assignments', async () => {
    const wrapper = render()
    await flushPromises()

    const form = wrapper.get('#gm-panel-settings form')
    await form.get('input[type="radio"][value="auto"]').setValue(true)
    expect(form.text()).toContain('groupManagement.settings.toAutoWarning')

    await form.trigger('submit')
    await flushPromises()
    expect(api.updateSettings).not.toHaveBeenCalled()
    expect(wrapper.get('[role="dialog"]').text()).toContain('groupManagement.settings.confirmToAuto')

    await buttonIn(wrapper.get('[role="dialog"]'), 'groupManagement.settings.save').trigger('click')
    await flushPromises()
    expect(api.updateSettings).toHaveBeenCalledWith(2, { enabled: true, allocation_mode: 'auto', max_concurrent: 3, daily_limit: 100 })
    wrapper.unmount()
  })

  it('saves manual assignments from the member row', async () => {
    const wrapper = render()
    await flushPromises()

    const row = wrapper.get('#gm-panel-members tbody tr')
    await row.get('button[aria-label="groupManagement.members.assign · Sam"]').trigger('click')

    const dialog = wrapper.get('[role="dialog"]')
    expect(dialog.get('input[aria-label="Zero"]').element).toHaveProperty('checked', true)
    expect(dialog.get('input[aria-label="Unlimited"]').element).toHaveProperty('checked', false)
    await dialog.get('input[aria-label="Unlimited"]').setValue(true)
    await buttonIn(dialog, 'groupManagement.assign.submit').trigger('click')
    await flushPromises()

    expect(api.setMemberAccounts).toHaveBeenCalledWith(2, 7, { account_ids: [10, 11], mode: 'manual' })
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('removes a member only after confirmation', async () => {
    const wrapper = render()
    await flushPromises()

    await wrapper.get('button[aria-label="groupManagement.members.remove · Sam"]').trigger('click')
    expect(api.removeMember).not.toHaveBeenCalled()

    const dialog = wrapper.get('[role="dialog"]')
    expect(dialog.text()).toContain('groupManagement.members.confirmRemove')
    await buttonIn(dialog, 'groupManagement.members.remove').trigger('click')
    await flushPromises()

    expect(api.removeMember).toHaveBeenCalledWith(2, 7)
    expect(appStore.showSuccess).toHaveBeenCalledWith('groupManagement.toast.memberRemoved')
    wrapper.unmount()
  })

  it('shows only the member assignments and never requests the manager pool', async () => {
    api.getOverview.mockResolvedValue({
      role: 'user',
      manageable_groups: [{ ...group, manager: false, member_count: 0, account_count: 0, max_concurrent: 0, daily_limit: 0 }],
      memberships: [member],
      assignments: [{ group_id: 2, id: 10, name: 'Own', platform: 'openai', status: 'active', user_id: 7, assignment_mode: 'manual', remaining_quota: 8 }]
    })
    const wrapper = render()
    await flushPromises()

    expect(wrapper.text()).toContain('Own')
    expect(wrapper.text()).toContain('2 / 10')
    expect(wrapper.text()).not.toContain('Unlimited')
    expect(wrapper.text()).not.toContain('groupManagement.summary.accounts')
    expect(wrapper.find('[role="tablist"]').exists()).toBe(false)
    expect(api.getAccounts).not.toHaveBeenCalled()
    expect(api.getMembers).not.toHaveBeenCalled()
    expect(api.getSettings).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('adds members by ID without user discovery and only authorizes existing group managers', async () => {
    api.getOverview.mockResolvedValue({ role: 'admin', manageable_groups: [group], memberships: [], assignments: [] })
    api.adminDirectory.mockResolvedValue({
      users: [
        { id: 8, username: 'Manager', email: 'manager@example.org', role: 'group_manager' },
        { id: 9, username: 'User', email: 'user@example.org', role: 'user' },
        { id: 12, username: 'Lead', email: 'lead@example.org', role: 'group_manager' }
      ],
      groups: [{ id: 2, name: 'Operations', manager_user_ids: [8] }]
    })
    const wrapper = render()
    await flushPromises()

    await buttonIn(wrapper.get('#gm-panel-members'), 'groupManagement.members.add').trigger('click')
    let dialog = wrapper.get('[role="dialog"]')
    await dialog.get('input[type="number"]').setValue('7')
    await dialog.get('form').trigger('submit')
    expect(dialog.text()).toContain('groupManagement.addMember.exists')
    expect(api.addMember).not.toHaveBeenCalled()

    await dialog.get('input[type="number"]').setValue('9')
    await dialog.get('form').trigger('submit')
    await flushPromises()
    expect(api.addMember).toHaveBeenCalledWith(2, 9)

    await wrapper.get('#gm-tab-managers').trigger('click')
    const panel = wrapper.get('#gm-panel-managers')
    expect(panel.text()).toContain('Manager')
    const select = wrapper.findComponent(Select)
    expect(select.props('options')).toEqual([{ value: 12, label: 'Lead · #12' }])
    select.vm.$emit('update:modelValue', 12)
    await flushPromises()
    await buttonIn(panel, 'groupManagement.managers.assign').trigger('click')
    await flushPromises()
    expect(api.adminAssignManager).toHaveBeenCalledWith(2, 12)

    await buttonIn(wrapper.get('#gm-panel-managers'), 'groupManagement.managers.revoke').trigger('click')
    dialog = wrapper.get('[role="dialog"]')
    await buttonIn(dialog, 'groupManagement.managers.revoke').trigger('click')
    await flushPromises()
    expect(api.adminRevokeManager).toHaveBeenCalledWith(2, 8)
    wrapper.unmount()
  })

  it('keeps the dialog open and reports the error when adding a member fails', async () => {
    api.addMember.mockRejectedValue({ status: 400, reason: 'GROUP_MANAGEMENT_BAD_INPUT', message: 'invalid group management request' })
    const wrapper = render()
    await flushPromises()

    await buttonIn(wrapper.get('#gm-panel-members'), 'groupManagement.members.add').trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    await dialog.get('input[type="number"]').setValue('42')
    await dialog.get('form').trigger('submit')
    await flushPromises()

    expect(api.addMember).toHaveBeenCalledWith(2, 42)
    expect(appStore.showError).toHaveBeenCalledTimes(1)
    expect(appStore.showSuccess).not.toHaveBeenCalled()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(true)
    wrapper.unmount()
  })
})

describe('GroupManagementView managed groups', () => {
  const managedGroup = { ...group, kind: 'managed', category: 'team' }
  const groupUser = { ...member, owned: true, status: 'active', balance: 3, reclaimable: 2.5 }

  beforeEach(() => {
    api.getOverview.mockResolvedValue({ role: 'group_manager', balance: 50, manageable_groups: [managedGroup], memberships: [], assignments: [] })
    api.getMembers.mockResolvedValue([groupUser])
    api.getSettings.mockResolvedValue({ group_id: 2, managed: true, ...settings })
    api.createGroupUser.mockResolvedValue({ ...groupUser, user_id: 9 })
    api.transferBalance.mockResolvedValue({ id: 1 })
    api.setGroupUserStatus.mockResolvedValue(undefined)
    api.resetGroupUserPassword.mockResolvedValue(undefined)
    api.updateCategory.mockResolvedValue(undefined)
    api.getOwnedUsers.mockResolvedValue([
      { user_id: 9, email: 'kim@example.org', username: 'Kim', status: 'active', group_id: 5, group_name: 'Sales' }
    ])
    api.listTransfers.mockResolvedValue({
      items: [{ id: 1, group_id: 2, manager_id: 3, manager_name: 'Boss', member_id: 7, member_name: 'Sam', direction: 'grant', amount: 5, notes: 'monthly', created_at: '2026-09-24T08:00:00Z' }],
      total: 1, page: 1, page_size: 20, pages: 1
    })
  })

  it('labels the member tab as group users, shows balances and locks enforcement', async () => {
    const wrapper = render()
    await flushPromises()

    expect(wrapper.get('#gm-tab-members').text()).toContain('groupManagement.tabs.groupUsers')
    expect(wrapper.find('#gm-tab-transfers').exists()).toBe(true)
    const panel = wrapper.get('#gm-panel-members')
    expect(panel.findAll('th').map(th => th.text())).toContain('groupManagement.members.balance')
    expect(panel.text()).toContain('groupManagement.members.owned')
    expect(wrapper.get('#gm-panel-settings button[role="switch"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('#gm-panel-settings').text()).toContain('groupManagement.settings.managedLocked')
    wrapper.unmount()
  })

  it('creates a group user and shows the one-time credentials', async () => {
    const wrapper = render()
    await flushPromises()

    await buttonIn(wrapper.get('#gm-panel-members'), 'groupManagement.members.createUser').trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    await dialog.get('input[type="email"]').setValue('new@example.org')
    const password = (dialog.get('#group-create-user-form-password').element as HTMLInputElement).value
    expect(password).toHaveLength(14)
    await dialog.get('#group-create-user-form-amount').setValue('10')
    await dialog.get('form').trigger('submit')
    await flushPromises()

    expect(api.createGroupUser).toHaveBeenCalledWith(2, {
      email: 'new@example.org',
      username: undefined,
      password,
      max_concurrent: 3,
      daily_limit: 100,
      initial_amount: 10
    })
    expect(wrapper.get('[data-testid="credentials-password"]').text()).toBe(password)
    expect(appStore.showSuccess).toHaveBeenCalledWith('groupManagement.toast.userCreated')
    wrapper.unmount()
  })

  it('rejects an initial transfer above the manager balance', async () => {
    const wrapper = render()
    await flushPromises()

    await buttonIn(wrapper.get('#gm-panel-members'), 'groupManagement.members.createUser').trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    await dialog.get('input[type="email"]').setValue('new@example.org')
    await dialog.get('#group-create-user-form-amount').setValue('80')
    await dialog.get('form').trigger('submit')
    await flushPromises()

    expect(dialog.text()).toContain('groupManagement.createUser.invalidAmount')
    expect(api.createGroupUser).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('transfers balance to a group user within the available balance', async () => {
    const wrapper = render()
    await flushPromises()

    await wrapper.get('button[aria-label="groupManagement.members.transfer \u00b7 Sam"]').trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    await dialog.get('#group-transfer-form-amount').setValue('60')
    expect(dialog.text()).toContain('groupManagement.transfer.exceedsMine')
    await dialog.get('form').trigger('submit')
    expect(api.transferBalance).not.toHaveBeenCalled()

    await dialog.get('[data-testid="transfer-direction-reclaim"]').trigger('click')
    // 只能收回本分组划拨的净额（2.5），不能动 TA 自己的余额
    await dialog.get('#group-transfer-form-amount').setValue('3')
    expect(dialog.text()).toContain('groupManagement.transfer.exceedsReclaimable')
    await dialog.get('#group-transfer-form-amount').setValue('2')
    await dialog.get('#group-transfer-form-notes').setValue('unused')
    await dialog.get('form').trigger('submit')
    await flushPromises()

    expect(api.transferBalance).toHaveBeenCalledWith(2, 7, { direction: 'reclaim', amount: 2, notes: 'unused' })
    expect(appStore.showSuccess).toHaveBeenCalledWith('groupManagement.toast.transferReclaimed')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('disables an owned group user only after confirmation', async () => {
    const wrapper = render()
    await flushPromises()

    await wrapper.get('button[aria-label="groupManagement.members.disable \u00b7 Sam"]').trigger('click')
    expect(api.setGroupUserStatus).not.toHaveBeenCalled()
    const dialog = wrapper.get('[role="dialog"]')
    expect(dialog.text()).toContain('groupManagement.members.confirmDisable')
    await buttonIn(dialog, 'groupManagement.members.disable').trigger('click')
    await flushPromises()

    expect(api.setGroupUserStatus).toHaveBeenCalledWith(2, 7, 'disabled')
    wrapper.unmount()
  })

  it('resets a group user password and shows the new one', async () => {
    const wrapper = render()
    await flushPromises()

    await wrapper.get('button[aria-label="groupManagement.members.resetPassword \u00b7 Sam"]').trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    const password = (dialog.get('#group-reset-password-form-password').element as HTMLInputElement).value
    await dialog.get('form').trigger('submit')
    await flushPromises()

    expect(api.resetGroupUserPassword).toHaveBeenCalledWith(2, 7, password)
    expect(wrapper.get('[data-testid="credentials-password"]').text()).toBe(password)
    wrapper.unmount()
  })

  it('switches the group category from the summary card', async () => {
    const wrapper = render()
    await flushPromises()

    expect(wrapper.get('[data-testid="group-category-team"]').attributes('aria-checked')).toBe('true')
    await wrapper.get('[data-testid="group-category-enterprise"]').trigger('click')
    await flushPromises()

    expect(api.updateCategory).toHaveBeenCalledWith(2, 'enterprise')
    expect(appStore.showSuccess).toHaveBeenCalledWith('groupManagement.toast.categorySaved')
    wrapper.unmount()
  })

  it('adds an existing group user owned by another managed group', async () => {
    const wrapper = render()
    await flushPromises()

    await buttonIn(wrapper.get('#gm-panel-members'), 'groupManagement.members.addOwned').trigger('click')
    await flushPromises()
    expect(api.getOwnedUsers).toHaveBeenCalledWith(2)
    const dialog = wrapper.get('[role="dialog"]')
    expect(dialog.text()).toContain('Kim')
    await dialog.get('input[type="radio"][value="9"]').setValue(true)
    await buttonIn(dialog, 'groupManagement.addOwned.submit').trigger('click')
    await flushPromises()

    expect(api.addMember).toHaveBeenCalledWith(2, 9)
    wrapper.unmount()
  })

  it('lists balance transfers when the transfers tab opens', async () => {
    const wrapper = render()
    await flushPromises()
    expect(api.listTransfers).not.toHaveBeenCalled()

    await wrapper.get('#gm-tab-transfers').trigger('click')
    await flushPromises()

    expect(api.listTransfers).toHaveBeenCalledWith(2, { page: 1, page_size: 20 })
    const panel = wrapper.get('#gm-panel-transfers')
    expect(panel.text()).toContain('Sam')
    expect(panel.text()).toContain('Boss')
    expect(panel.text()).toContain('monthly')
    wrapper.unmount()
  })

  it('does not offer balance transfers to super admins', async () => {
    api.getOverview.mockResolvedValue({ role: 'admin', balance: 0, manageable_groups: [managedGroup], memberships: [], assignments: [] })
    api.adminDirectory.mockResolvedValue({ users: [], groups: [{ id: 2, name: 'Operations', manager_user_ids: [] }] })
    const wrapper = render()
    await flushPromises()

    const panel = wrapper.get('#gm-panel-members')
    expect(panel.find('button[aria-label="groupManagement.members.transfer \u00b7 Sam"]').exists()).toBe(false)
    expect(panel.find('button[aria-label="groupManagement.members.resetPassword \u00b7 Sam"]').exists()).toBe(true)
    expect(panel.text()).toContain('groupManagement.members.addById')
    expect(panel.text()).toContain('groupManagement.members.adminTransferHint')
    wrapper.unmount()
  })
})
