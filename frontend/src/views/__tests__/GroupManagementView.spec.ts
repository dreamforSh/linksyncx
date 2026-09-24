import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import GroupManagementView from '../GroupManagementView.vue'

const api = vi.hoisted(() => ({
  getOverview: vi.fn(), adminDirectory: vi.fn(), getMembers: vi.fn(), getAccounts: vi.fn(), getSettings: vi.fn(),
  updateSettings: vi.fn(), addMember: vi.fn(), removeMember: vi.fn(), updateMemberLimit: vi.fn(),
  setMemberAccounts: vi.fn(), adminAssignManager: vi.fn(), adminRevokeManager: vi.fn()
}))
vi.mock('@/api/groupManagement', () => ({ groupManagementAPI: api }))
vi.mock('vue-i18n', async importOriginal => ({ ...(await importOriginal<typeof import('vue-i18n')>()), useI18n: () => ({ t: (key: string) => key }) }))
const group = { id: 2, name: 'Operations', manager: true, member_count: 1, account_count: 2, enabled: true, allocation_mode: 'manual', max_concurrent: 3, daily_limit: 100 }
const member = { user_id: 7, group_id: 2, group_name: 'Operations', username: 'Sam', email: 'sam@example.org', daily_limit: 10, daily_used: 2, max_concurrent: 2, daily_window_start: '' }
const pool = [
  { group_id: 2, id: 10, name: 'Zero', platform: 'OpenAI', status: 'active', schedulable: false, remaining_quota: 0, assigned_user_ids: [7], assignment_mode: 'manual' },
  { group_id: 2, id: 11, name: 'Unlimited', platform: 'Claude', status: 'active', schedulable: true, remaining_quota: null, assigned_user_ids: [] }
]
function render() {
  return mount(GroupManagementView, { global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, Icon: { template: '<span />' }, RouterLink: { template: '<a><slot /></a>' } } } })
}

beforeEach(() => {
  vi.clearAllMocks()
  api.getOverview.mockResolvedValue({ role: 'group_manager', manageable_groups: [group], memberships: [member], assignments: [] })
  api.getMembers.mockResolvedValue([member])
  api.getAccounts.mockResolvedValue(pool)
  api.getSettings.mockResolvedValue(group)
  api.updateSettings.mockResolvedValue(group)
  api.setMemberAccounts.mockResolvedValue(undefined)
})

describe('GroupManagementView', () => {
  it('shows the entire manager pool without pretending it has per-account quota, and saves group defaults', async () => {
    const wrapper = render()
    await flushPromises()
    expect(wrapper.text()).toContain('Zero')
    expect(wrapper.text()).toContain('Unlimited')
    expect(wrapper.findAll('tbody tr')[1].text()).toContain('-')
    expect(wrapper.findAll('tbody tr')[2].text()).toContain('-')
    expect(wrapper.find('form').element.textContent).not.toContain('Sam')
    const form = wrapper.findAll('form')[0]
    await form.trigger('submit')
    await flushPromises()
    expect(api.updateSettings).toHaveBeenCalledWith(2, { enabled: true, allocation_mode: 'manual', max_concurrent: 3, daily_limit: 100 })
    wrapper.unmount()
  })

  it('saves manual assignments without suggesting a per-member automatic mode', async () => {
    const wrapper = render()
    await flushPromises()
    await wrapper.find('select.input.min-w-48').setValue('7')
    await flushPromises()
    expect(wrapper.find('input[aria-label="Zero"]').element).toHaveProperty('checked', true)
    await wrapper.findAll('button').find(button => button.text() === 'common.save' && button.attributes('type') === 'button')!.trigger('click')
    await flushPromises()
    expect(api.setMemberAccounts).toHaveBeenCalledWith(2, 7, { account_ids: [10], mode: 'manual' })
    wrapper.unmount()
  })

  it('shows only the member assignments and never requests the manager pool', async () => {
    api.getOverview.mockResolvedValue({ role: 'user', manageable_groups: [{ ...group, manager: false }], memberships: [member], assignments: [{ group_id: 2, id: 10, name: 'Own', platform: 'OpenAI', status: 'active', user_id: 7, assignment_mode: 'auto', remaining_quota: 0 }] })
    const wrapper = render()
    await flushPromises()
    expect(wrapper.text()).toContain('Own')
    expect(wrapper.text()).toContain('2 / 10')
    expect(wrapper.text()).not.toContain('Unlimited')
    expect(wrapper.text()).not.toContain('groupManagement.groups.accountPool')
    expect(api.getAccounts).not.toHaveBeenCalled()
    expect(api.getMembers).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('adds members by ID without user discovery and grants only existing manager roles', async () => {
    api.getOverview.mockResolvedValue({ role: 'admin', manageable_groups: [group], memberships: [], assignments: [] })
    api.adminDirectory.mockResolvedValue({
      users: [{ id: 8, username: 'Manager', email: 'manager@example.org', role: 'group_manager' }, { id: 9, username: 'User', email: 'user@example.org', role: 'user' }],
      groups: [{ id: 2, name: 'Operations', manager_user_ids: [8] }]
    })
    api.addMember.mockResolvedValue(member)
    api.adminAssignManager.mockResolvedValue(undefined)
    const wrapper = render()
    await flushPromises()
    expect(wrapper.findAll('option').map(option => option.text())).not.toContain('User · #9')
    await wrapper.findAll('form')[1].find('input[type="number"]').setValue('9')
    await wrapper.findAll('form')[1].trigger('submit')
    await flushPromises()
    expect(api.addMember).toHaveBeenCalledWith(2, 9)
    await wrapper.findAll('select').at(-1)!.setValue('8')
    await wrapper.findAll('button').find(button => button.text() === 'groupManagement.admin.assign')!.trigger('click')
    await flushPromises()
    expect(api.adminAssignManager).toHaveBeenCalledWith(2, 8)
    expect(api.adminDirectory).toHaveBeenCalled()
    wrapper.unmount()
  })
})
