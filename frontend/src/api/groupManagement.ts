import { apiClient } from './client'

export type AllocationMode = 'auto' | 'manual'

export interface GroupManagementSettings {
  enabled: boolean
  allocation_mode: AllocationMode
  max_concurrent: number
  daily_limit: number
}

export interface GroupManagementGroup extends GroupManagementSettings {
  id: number
  name: string
  member_count: number
  account_count: number
  manager: boolean
  manager_user_ids: number[]
}

export interface GroupManagementMembership {
  user_id: number
  group_id: number
  group_name: string
  email?: string
  username?: string
  max_concurrent: number
  daily_limit: number
  daily_used: number
  daily_window_start: string
}

export interface GroupManagementAssignment {
  group_id: number
  id: number
  name: string
  platform: string
  status: string
  user_id: number
  assignment_mode: AllocationMode
  remaining_quota: number | null
}

export interface GroupManagementAccount {
  group_id: number
  id: number
  name: string
  platform: string
  status: string
  schedulable?: boolean
  remaining_quota: number | null
  assignment_mode?: AllocationMode
  user_id?: number
  assigned_user_ids: number[]
}

export interface GroupManagementOverview {
  role: string
  manageable_groups: GroupManagementGroup[]
  memberships: GroupManagementMembership[]
  assignments: GroupManagementAssignment[]
}

export type GroupManagementMember = GroupManagementMembership
export type MemberLimit = Pick<GroupManagementSettings, 'max_concurrent' | 'daily_limit'>

export interface AdminGroupManagementUser {
  id: number
  email: string
  username: string
  role: string
}

export interface AdminGroupManagementDirectory {
  users: AdminGroupManagementUser[]
  groups: Array<{ id: number; name: string; manager_user_ids: number[] }>
}

export const groupManagementAPI = {
  async getOverview(): Promise<GroupManagementOverview> {
    const { data } = await apiClient.get<GroupManagementOverview>('/group-management/me/overview')
    return data
  },

  async getSettings(groupId: number): Promise<GroupManagementSettings> {
    const { data } = await apiClient.get<GroupManagementSettings>(`/group-management/groups/${groupId}/settings`)
    return data
  },

  async updateSettings(groupId: number, settings: GroupManagementSettings): Promise<GroupManagementSettings> {
    const { data } = await apiClient.put<GroupManagementSettings>(`/group-management/groups/${groupId}/settings`, settings)
    return data
  },

  async getMembers(groupId: number): Promise<GroupManagementMember[]> {
    const { data } = await apiClient.get<GroupManagementMember[]>(`/group-management/groups/${groupId}/members`)
    return data
  },

  async addMember(groupId: number, userId: number): Promise<GroupManagementMember> {
    const { data } = await apiClient.post<GroupManagementMember>(`/group-management/groups/${groupId}/members`, { user_id: userId })
    return data
  },

  async removeMember(groupId: number, userId: number): Promise<void> {
    await apiClient.delete(`/group-management/groups/${groupId}/members/${userId}`)
  },

  async getAccounts(groupId: number): Promise<GroupManagementAccount[]> {
    const { data } = await apiClient.get<GroupManagementAccount[]>(`/group-management/groups/${groupId}/accounts`)
    return data
  },

  async updateMemberLimit(groupId: number, userId: number, input: MemberLimit): Promise<GroupManagementMember> {
    const { data } = await apiClient.put<GroupManagementMember>(
      `/group-management/groups/${groupId}/members/${userId}/limit`, input
    )
    return data
  },

  async setMemberAccounts(groupId: number, userId: number, input: { account_ids: number[]; mode: AllocationMode }): Promise<void> {
    await apiClient.put(`/group-management/groups/${groupId}/members/${userId}/accounts`, input)
  },

  async adminDirectory(): Promise<AdminGroupManagementDirectory> {
    const [usersResponse, groupsResponse] = await Promise.all([
      apiClient.get<AdminGroupManagementUser[]>('/admin/group-management/users'),
      apiClient.get<AdminGroupManagementDirectory['groups']>('/admin/group-management/groups')
    ])
    return { users: usersResponse.data, groups: groupsResponse.data }
  },

  async adminAssignManager(groupId: number, userId: number): Promise<void> {
    await apiClient.put(`/admin/group-management/groups/${groupId}/managers/${userId}`)
  },

  async adminRevokeManager(groupId: number, userId: number): Promise<void> {
    await apiClient.delete(`/admin/group-management/groups/${groupId}/managers/${userId}`)
  }
}

export default groupManagementAPI
