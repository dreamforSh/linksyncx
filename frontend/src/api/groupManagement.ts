import { apiClient } from './client'
import type { GroupCategory, GroupKind, ManagedGroupType } from '@/types'

export type AllocationMode = 'auto' | 'manual'
export type GroupTransferDirection = 'grant' | 'reclaim'
export type GroupUserStatus = 'active' | 'disabled'
export type GroupInvitationStatus = 'pending' | 'accepted' | 'declined' | 'revoked'

export interface GroupManagementSettings {
  enabled: boolean
  allocation_mode: AllocationMode
  max_concurrent: number
  daily_limit: number
  // 管理分组始终启用管控（只读）
  managed?: boolean
  // 管理分组类型（只读）：分配方式由类型决定
  managed_type?: ManagedGroupType
  // 新成员默认的组内 5h / 7d 美元上限（额度组使用，0 表示不限）
  default_limit_5h_usd?: number
  default_limit_7d_usd?: number
}

export interface GroupManagementGroup extends GroupManagementSettings {
  id: number
  name: string
  kind?: GroupKind
  category?: GroupCategory
  managed_type?: ManagedGroupType
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
  // 由本分组组管理员创建的组用户
  owned?: boolean
  status?: GroupUserStatus
  // 仅管理分组返回；reclaimable 为可回收上限（本分组划拨净额与当前余额的较小值）
  balance?: number
  reclaimable?: number
  max_concurrent: number
  daily_limit: number
  daily_used: number
  daily_window_start: string
  // 额度组：组内 5h / 7d 美元上限（0 表示不限）与当前窗口用量；reset_* 为当前窗口结束时间
  limit_5h_usd?: number
  limit_7d_usd?: number
  usage_5h_usd?: number
  usage_7d_usd?: number
  reset_5h_at?: string
  reset_7d_at?: string
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
  // 当前用户自己的余额
  balance?: number
  manageable_groups: GroupManagementGroup[]
  memberships: GroupManagementMembership[]
  assignments: GroupManagementAssignment[]
}

export type GroupManagementMember = GroupManagementMembership

export interface MemberLimit {
  max_concurrent: number
  daily_limit: number
  // 额度组：组内 5h / 7d 美元上限，省略表示不修改
  limit_5h_usd?: number
  limit_7d_usd?: number
}

export interface GroupManagementSummary {
  role: string
  managed_group_count: number
  membership_count: number
  pending_invitation_count?: number
}

// 分组邀请：组管理员与被邀请人看到的都是邮箱，不含用户 ID
export interface GroupInvitation {
  id: number
  group_id: number
  group_name: string
  category?: GroupCategory
  managed_type?: ManagedGroupType
  email: string
  inviter_name: string
  status: GroupInvitationStatus
  created_at: string
  responded_at?: string
}

export interface GroupAccountWindow {
  utilization: number
  resets_at?: string
  remaining_seconds: number
}

export interface GroupAccountResetCredits {
  available_count: number
  expires_at: string[]
}

// 订阅组账号的限额视图
export interface GroupAccountUsage {
  account_id: number
  name: string
  platform: string
  status: string
  rate_limited: boolean
  rate_limit_reset_at?: string
  five_hour?: GroupAccountWindow
  seven_day?: GroupAccountWindow
  usage_updated_at?: string
  usage_unavailable: boolean
  supports_reset_credit: boolean
  reset_credits?: GroupAccountResetCredits
  can_reset: boolean
  assigned_user_count: number
}

export interface GroupAccountResetResult {
  code: string
  windows_reset: number
  warning_code?: string
  account?: GroupAccountUsage
}

export interface CreateGroupUserInput {
  email: string
  username?: string
  password: string
  max_concurrent?: number
  daily_limit?: number
  initial_amount?: number
}

export interface GroupOwnedUser {
  user_id: number
  email: string
  username: string
  status: GroupUserStatus
  group_id: number
  group_name: string
}

export interface GroupBalanceTransfer {
  id: number
  group_id: number
  manager_id: number | null
  manager_name: string
  member_id: number | null
  member_name: string
  direction: GroupTransferDirection
  amount: number
  notes: string
  created_at: string
}

export interface GroupTransferPage {
  items: GroupBalanceTransfer[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface GroupRef {
  id: number
  name: string
  category?: GroupCategory
}

export interface UserGroupSummary {
  user_id: number
  owned_group?: GroupRef
  managed_groups: GroupRef[]
}

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

  async getSummary(): Promise<GroupManagementSummary> {
    const { data } = await apiClient.get<GroupManagementSummary>('/group-management/me/summary')
    return data
  },

  async updateCategory(groupId: number, category: GroupCategory): Promise<void> {
    await apiClient.put(`/group-management/groups/${groupId}/category`, { category })
  },

  async getOwnedUsers(groupId: number): Promise<GroupOwnedUser[]> {
    const { data } = await apiClient.get<GroupOwnedUser[]>(`/group-management/groups/${groupId}/owned-users`)
    return data
  },

  async createGroupUser(groupId: number, input: CreateGroupUserInput): Promise<GroupManagementMember> {
    const { data } = await apiClient.post<GroupManagementMember>(`/group-management/groups/${groupId}/users`, input)
    return data
  },

  async setGroupUserStatus(groupId: number, userId: number, status: GroupUserStatus): Promise<void> {
    await apiClient.put(`/group-management/groups/${groupId}/users/${userId}/status`, { status })
  },

  async resetGroupUserPassword(groupId: number, userId: number, password: string): Promise<void> {
    await apiClient.put(`/group-management/groups/${groupId}/users/${userId}/password`, { password })
  },

  async transferBalance(
    groupId: number,
    userId: number,
    input: { direction: GroupTransferDirection; amount: number; notes?: string }
  ): Promise<GroupBalanceTransfer> {
    const { data } = await apiClient.post<GroupBalanceTransfer>(
      `/group-management/groups/${groupId}/users/${userId}/balance-transfers`, input
    )
    return data
  },

  async listTransfers(groupId: number, params: { page?: number; page_size?: number; user_id?: number } = {}): Promise<GroupTransferPage> {
    const { data } = await apiClient.get<GroupTransferPage>(`/group-management/groups/${groupId}/balance-transfers`, { params })
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

  async listInvitations(groupId: number): Promise<GroupInvitation[]> {
    const { data } = await apiClient.get<GroupInvitation[]>(`/group-management/groups/${groupId}/invitations`)
    return data
  },

  async inviteMember(groupId: number, email: string): Promise<GroupInvitation> {
    const { data } = await apiClient.post<GroupInvitation>(`/group-management/groups/${groupId}/invitations`, { email })
    return data
  },

  async revokeInvitation(groupId: number, invitationId: number): Promise<void> {
    await apiClient.delete(`/group-management/groups/${groupId}/invitations/${invitationId}`)
  },

  async myInvitations(): Promise<GroupInvitation[]> {
    const { data } = await apiClient.get<GroupInvitation[]>('/group-management/me/invitations')
    return data
  },

  async acceptInvitation(invitationId: number): Promise<GroupInvitation> {
    const { data } = await apiClient.post<GroupInvitation>(`/group-management/me/invitations/${invitationId}/accept`)
    return data
  },

  async declineInvitation(invitationId: number): Promise<GroupInvitation> {
    const { data } = await apiClient.post<GroupInvitation>(`/group-management/me/invitations/${invitationId}/decline`)
    return data
  },

  async getAccountUsage(groupId: number): Promise<GroupAccountUsage[]> {
    const { data } = await apiClient.get<GroupAccountUsage[]>(`/group-management/groups/${groupId}/account-usage`)
    return data
  },

  async refreshAccountQuota(groupId: number, accountId: number): Promise<GroupAccountUsage> {
    const { data } = await apiClient.post<GroupAccountUsage>(`/group-management/groups/${groupId}/accounts/${accountId}/quota-refresh`)
    return data
  },

  async resetAccountCredit(groupId: number, accountId: number): Promise<GroupAccountResetResult> {
    const { data } = await apiClient.post<GroupAccountResetResult>(`/group-management/groups/${groupId}/accounts/${accountId}/reset-credit`)
    return data
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
  },

  async adminUserGroups(userIds: number[]): Promise<UserGroupSummary[]> {
    const { data } = await apiClient.get<UserGroupSummary[]>('/admin/group-management/user-groups', {
      params: { user_ids: userIds.join(',') }
    })
    return data
  }
}

export default groupManagementAPI
