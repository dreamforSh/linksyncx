<template>
  <AppLayout>
    <div class="mx-auto max-w-7xl space-y-6">
      <MyInvitationsCard
        v-if="myInvitations.length"
        :invitations="myInvitations"
        :busy-id="invitationBusyId"
        @accept="respondInvitation($event, true)"
        @decline="respondInvitation($event, false)"
      />

      <div
        v-if="loadError"
        role="alert"
        class="flex flex-wrap items-start gap-3 rounded-2xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-800 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-200"
      >
        <Icon name="exclamationCircle" size="md" class="shrink-0" />
        <div class="min-w-0 flex-1">
          <p class="font-medium">{{ t('groupManagement.loadFailed') }}</p>
          <p class="mt-0.5 break-words opacity-90">{{ loadError }}</p>
        </div>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading || detailsLoading" @click="refresh">
          {{ t('groupManagement.retry') }}
        </button>
      </div>

      <div
        v-if="loading && !overview"
        class="grid gap-6 min-[1400px]:grid-cols-[17rem_minmax(0,1fr)]"
        role="status"
        :aria-label="t('common.loading')"
      >
        <div class="card hidden space-y-2 p-3 min-[1400px]:block">
          <div v-for="index in 4" :key="index" class="skeleton h-14 rounded-xl" />
        </div>
        <div class="space-y-6">
          <div class="card p-6">
            <div class="flex items-center gap-4">
              <div class="skeleton h-12 w-12 rounded-2xl" />
              <div class="flex-1 space-y-2">
                <div class="skeleton h-5 w-40" />
                <div class="skeleton h-4 w-64 max-w-full" />
              </div>
            </div>
            <div class="mt-6 grid grid-cols-2 gap-4 sm:grid-cols-4">
              <div v-for="index in 4" :key="index" class="skeleton h-12 rounded-lg" />
            </div>
          </div>
          <div class="card space-y-3 p-6">
            <div v-for="index in 4" :key="index" class="skeleton h-10 rounded-lg" />
          </div>
        </div>
      </div>

      <div v-else-if="overview && !groups.length && !myInvitations.length" class="card">
        <EmptyState :title="t('groupManagement.empty.title')" :description="t('groupManagement.empty.description')">
          <template #icon>
            <Icon name="users" size="xl" class="text-gray-300 dark:text-dark-500" />
          </template>
        </EmptyState>
      </div>

      <div
        v-else-if="overview && selectedGroup"
        class="grid items-start gap-6"
        :class="groups.length > 1 && 'min-[1400px]:grid-cols-[17rem_minmax(0,1fr)]'"
      >
        <GroupNavList
          v-if="groups.length > 1"
          :groups="groups"
          :model-value="selectedGroupId"
          @update:model-value="selectGroup"
        />

        <div class="min-w-0 space-y-6">
          <GroupSummaryCard
            :group="selectedGroup"
            :stats="summaryStats"
            :refreshing="loading || detailsLoading"
            :category-editable="isManagedGroup && selectedGroup.manager"
            :category-saving="categorySaving"
            @refresh="refresh"
            @open-settings="activeTab = 'settings'"
            @update-category="updateCategory"
          />

          <template v-if="selectedGroup.manager">
            <div
              role="tablist"
              :aria-label="t('groupManagement.tabs.label')"
              class="tabs inline-flex max-w-full overflow-x-auto scrollbar-hide"
              @keydown="onTabKeydown"
            >
              <button
                v-for="tab in tabs"
                :id="`gm-tab-${tab.key}`"
                :key="tab.key"
                type="button"
                role="tab"
                :aria-selected="activeTab === tab.key"
                :aria-controls="`gm-panel-${tab.key}`"
                :tabindex="activeTab === tab.key ? 0 : -1"
                class="tab inline-flex shrink-0 items-center gap-2 whitespace-nowrap px-3 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/50 sm:px-4"
                :class="activeTab === tab.key && 'tab-active'"
                @click="activeTab = tab.key"
              >
                <Icon :name="tab.icon" size="sm" />
                {{ tab.label }}
                <span
                  v-if="tab.count !== undefined"
                  class="rounded-full px-1.5 py-px text-[11px] font-semibold tabular-nums"
                  :class="activeTab === tab.key
                    ? 'bg-primary-100 text-primary-700 dark:bg-primary-900/40 dark:text-primary-300'
                    : 'bg-gray-200/70 text-gray-600 dark:bg-dark-600 dark:text-dark-200'"
                >
                  {{ tab.count }}
                </span>
              </button>
            </div>

            <div
              v-if="!detailsReady && detailsLoading"
              class="card space-y-4 p-5"
              role="status"
              :aria-label="t('common.loading')"
            >
              <div v-for="index in 4" :key="index" class="flex items-center gap-3">
                <div class="skeleton h-9 w-9 rounded-full" />
                <div class="flex-1 space-y-2">
                  <div class="skeleton h-3 w-40" />
                  <div class="skeleton h-3 w-24" />
                </div>
                <div class="skeleton hidden h-3 w-28 sm:block" />
              </div>
            </div>

            <template v-else-if="detailsReady && settings">
              <div v-show="activeTab === 'members'" id="gm-panel-members" role="tabpanel" aria-labelledby="gm-tab-members">
                <GroupMembersPanel
                  :members="members"
                  :accounts="accounts"
                  :mode="settings.allocation_mode"
                  :busy="mutating"
                  :managed="isManagedGroup"
                  :managed-type="managedType"
                  :can-transfer="canTransfer"
                  :can-manage-users="canManageGroupUsers"
                  :can-invite="canInvite"
                  :can-add-owned="canAddOwned"
                  :can-add-by-id="isAdmin"
                  :show-ids="isAdmin"
                  @add="addMemberOpen = true"
                  @add-owned="openAddOwned"
                  @invite="inviteOpen = true"
                  @create-user="openCreateUser"
                  @edit="openLimit"
                  @assign="openAssign"
                  @remove="requestRemoveMember"
                  @transfer="openTransfer"
                  @reset-password="openResetPassword"
                  @toggle-status="requestToggleStatus"
                />
              </div>
              <div v-show="activeTab === 'accounts'" id="gm-panel-accounts" role="tabpanel" aria-labelledby="gm-tab-accounts">
                <SubscriptionAccountsPanel
                  v-if="isSubscriptionGroup"
                  :usage="visibleAccountUsage"
                  :accounts="accounts"
                  :members="members"
                  :loading="accountUsageLoading"
                  :busy="mutating"
                  :action-account-id="accountActionId"
                  @assign="openAssign()"
                  @reload="loadAccountUsage"
                  @refresh-quota="refreshAccountQuota"
                  @reset-credit="requestResetCredit"
                />
                <GroupAccountsPanel
                  v-else
                  :accounts="accounts"
                  :members="members"
                  :mode="settings.allocation_mode"
                  :busy="mutating"
                  @assign="openAssign()"
                />
              </div>
              <div v-show="activeTab === 'invitations'" id="gm-panel-invitations" role="tabpanel" aria-labelledby="gm-tab-invitations">
                <GroupInvitationsPanel
                  :invitations="groupInvitations"
                  :loading="detailsLoading"
                  :busy="mutating"
                  @invite="inviteOpen = true"
                  @revoke="requestRevokeInvitation"
                />
              </div>
              <div
                v-if="isQuotaGroup && activeTab === 'transfers'"
                id="gm-panel-transfers"
                role="tabpanel"
                aria-labelledby="gm-tab-transfers"
              >
                <GroupTransfersPanel
                  :group-id="selectedGroup.id"
                  :members="members"
                  :refresh-key="transfersRefreshKey"
                  @error="onPanelError"
                />
              </div>
              <div v-show="activeTab === 'settings'" id="gm-panel-settings" role="tabpanel" aria-labelledby="gm-tab-settings">
                <GroupSettingsPanel
                  :group-id="selectedGroup.id"
                  :settings="settings"
                  :saving="mutating"
                  @save="requestSaveSettings"
                />
              </div>
              <div
                v-if="isAdmin"
                v-show="activeTab === 'managers'"
                id="gm-panel-managers"
                role="tabpanel"
                aria-labelledby="gm-tab-managers"
              >
                <GroupManagersPanel
                  :group-id="selectedGroup.id"
                  :managers="currentManagers"
                  :candidates="managerCandidates"
                  :busy="mutating"
                  @assign="assignManager"
                  @revoke="requestRevokeManager"
                />
              </div>
            </template>
          </template>

          <template v-else>
            <section v-if="isQuotaGroup" class="card p-5 sm:p-6" aria-labelledby="gm-mine-quota" data-testid="my-quota-usage">
              <div class="flex flex-wrap items-baseline justify-between gap-2">
                <h3 id="gm-mine-quota" class="text-sm font-semibold text-gray-900 dark:text-white">
                  {{ t('groupManagement.mine.usdQuotaTitle') }}
                </h3>
                <span class="text-xs text-gray-500 dark:text-dark-400">{{ t('groupManagement.mine.usdResetHint') }}</span>
              </div>
              <template v-if="ownMembership">
                <div class="mt-4 grid gap-5 sm:grid-cols-2">
                  <QuotaBar
                    size="lg"
                    :used="ownMembership.usage_5h_usd ?? 0"
                    :limit="ownMembership.limit_5h_usd ?? 0"
                    :format="formatUsdCompact"
                    :hint="resetHint(ownMembership.reset_5h_at)"
                    :label="t('groupManagement.members.usage5h')"
                  />
                  <QuotaBar
                    size="lg"
                    :used="ownMembership.usage_7d_usd ?? 0"
                    :limit="ownMembership.limit_7d_usd ?? 0"
                    :format="formatUsdCompact"
                    :hint="resetHint(ownMembership.reset_7d_at)"
                    :label="t('groupManagement.members.usage7d')"
                  />
                </div>
                <div class="mt-3 flex flex-wrap gap-x-6 gap-y-1 text-sm text-gray-600 dark:text-dark-300">
                  <span>{{ t('groupManagement.mine.balance', { amount: formatCurrency(ownBalance) }) }}</span>
                  <span>
                    {{ ownMembership.daily_limit > 0
                      ? t('groupManagement.mine.remaining', { count: ownRemaining })
                      : t('groupManagement.mine.unlimited') }}
                  </span>
                  <span>{{ t('groupManagement.mine.concurrency', { count: ownMembership.max_concurrent }) }}</span>
                </div>
              </template>
            </section>

            <section v-else class="card p-5 sm:p-6" aria-labelledby="gm-mine-quota">
              <div class="flex flex-wrap items-baseline justify-between gap-2">
                <h3 id="gm-mine-quota" class="text-sm font-semibold text-gray-900 dark:text-white">
                  {{ t('groupManagement.mine.quotaTitle') }}
                </h3>
                <span class="text-xs text-gray-500 dark:text-dark-400">{{ t('groupManagement.mine.resetHint') }}</span>
              </div>
              <template v-if="ownMembership">
                <div class="mt-4">
                  <QuotaBar
                    size="lg"
                    :used="ownMembership.daily_used"
                    :limit="ownMembership.daily_limit"
                    :label="t('groupManagement.mine.quotaTitle')"
                  />
                </div>
                <div class="mt-3 flex flex-wrap gap-x-6 gap-y-1 text-sm text-gray-600 dark:text-dark-300">
                  <span>
                    {{ ownMembership.daily_limit > 0
                      ? t('groupManagement.mine.remaining', { count: ownRemaining })
                      : t('groupManagement.mine.unlimited') }}
                  </span>
                  <span>{{ t('groupManagement.mine.concurrency', { count: ownMembership.max_concurrent }) }}</span>
                </div>
              </template>
            </section>

            <section v-if="isSubscriptionGroup" class="card overflow-hidden" aria-labelledby="gm-mine-accounts">
              <header class="flex items-center justify-between gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
                <div class="min-w-0">
                  <h3 id="gm-mine-accounts" class="text-sm font-semibold text-gray-900 dark:text-white">
                    {{ t('groupManagement.mine.accountsTitle') }}
                  </h3>
                  <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">{{ t('groupManagement.mine.subscriptionHint') }}</p>
                </div>
                <span class="badge badge-gray tabular-nums">{{ visibleAccountUsage.length }}</span>
              </header>
              <div v-if="accountUsageLoading && !visibleAccountUsage.length" class="grid gap-4 p-5 lg:grid-cols-2" role="status" :aria-label="t('common.loading')">
                <div v-for="index in 2" :key="index" class="skeleton h-36 rounded-xl" />
              </div>
              <div v-else-if="visibleAccountUsage.length" class="grid gap-4 p-5 lg:grid-cols-2">
                <AccountUsageCard v-for="item in visibleAccountUsage" :key="item.account_id" :usage="item" />
              </div>
              <div v-else class="empty-state py-10">
                <div class="mb-4 flex h-14 w-14 items-center justify-center rounded-2xl bg-gray-100 dark:bg-dark-700">
                  <Icon name="server" size="lg" class="text-gray-400 dark:text-dark-400" />
                </div>
                <h4 class="empty-state-title text-base">{{ t('groupManagement.mine.emptyAccounts') }}</h4>
                <p class="empty-state-description">{{ t('groupManagement.mine.emptyManual') }}</p>
              </div>
            </section>

            <section v-else class="card overflow-hidden" aria-labelledby="gm-mine-accounts">
              <header class="flex items-center justify-between gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
                <h3 id="gm-mine-accounts" class="text-sm font-semibold text-gray-900 dark:text-white">
                  {{ t('groupManagement.mine.accountsTitle') }}
                </h3>
                <span class="badge badge-gray tabular-nums">{{ ownAccounts.length }}</span>
              </header>
              <ul v-if="ownAccounts.length" class="divide-y divide-gray-100 dark:divide-dark-700">
                <li v-for="account in ownAccounts" :key="account.id" class="flex flex-wrap items-center gap-3 px-5 py-3">
                  <div class="min-w-0 flex-1">
                    <p class="truncate text-sm font-medium text-gray-900 dark:text-white">{{ account.name }}</p>
                    <p class="text-xs text-gray-500 dark:text-dark-400">#{{ account.id }}</p>
                  </div>
                  <PlatformChip :platform="account.platform" />
                  <AccountStatus :status="account.status" />
                </li>
              </ul>
              <div v-else class="empty-state py-10">
                <div class="mb-4 flex h-14 w-14 items-center justify-center rounded-2xl bg-gray-100 dark:bg-dark-700">
                  <Icon name="server" size="lg" class="text-gray-400 dark:text-dark-400" />
                </div>
                <h4 class="empty-state-title text-base">{{ t('groupManagement.mine.emptyAccounts') }}</h4>
                <p v-if="ownEmptyHint" class="empty-state-description">{{ ownEmptyHint }}</p>
              </div>
            </section>
          </template>
        </div>
      </div>
    </div>

    <AddMemberDialog
      :show="addMemberOpen"
      :saving="mutating"
      :defaults="memberDefaults"
      :existing-ids="memberIds"
      @close="addMemberOpen = false"
      @submit="submitAddMember"
    />
    <InviteMemberDialog
      :show="inviteOpen"
      :saving="mutating"
      :member-emails="memberEmails"
      @close="inviteOpen = false"
      @submit="submitInvite"
    />
    <CreateGroupUserDialog
      :show="createUserOpen"
      :saving="mutating"
      :defaults="memberDefaults"
      :can-transfer="canTransfer"
      :available-balance="ownBalance"
      :created="createdCredentials"
      @close="closeCreateUser"
      @submit="submitCreateUser"
    />
    <TransferBalanceDialog
      :show="transferOpen"
      :saving="mutating"
      :member="transferMember"
      :available-balance="ownBalance"
      @close="transferOpen = false"
      @submit="submitTransfer"
    />
    <ResetPasswordDialog
      :show="resetOpen"
      :saving="mutating"
      :member="resetMember"
      :reset-password="resetDonePassword"
      @close="closeResetPassword"
      @submit="submitResetPassword"
    />
    <AddOwnedMemberDialog
      :show="ownedOpen"
      :saving="mutating"
      :loading="ownedLoading"
      :candidates="ownedCandidates"
      @close="ownedOpen = false"
      @submit="submitAddOwned"
    />
    <MemberLimitDialog
      :show="limitOpen"
      :saving="mutating"
      :member="limitMember"
      :defaults="memberDefaults"
      :usd-limits="isQuotaGroup"
      @close="limitOpen = false"
      @submit="submitLimit"
    />
    <AssignAccountsDialog
      :show="assignOpen"
      :saving="mutating"
      :members="members"
      :accounts="accounts"
      :initial-member-id="assignMemberId"
      @close="assignOpen = false"
      @submit="submitAssign"
    />
    <ConfirmDialog
      :show="confirmOpen"
      :title="confirmState.title"
      :message="confirmState.message"
      :confirm-text="confirmState.confirmText"
      :danger="confirmState.danger"
      @confirm="runConfirmed"
      @cancel="confirmOpen = false"
    >
      <ul v-if="confirmState.details.length" class="list-disc space-y-1.5 pl-5 text-sm text-gray-600 dark:text-gray-400">
        <li v-for="detail in confirmState.details" :key="detail">{{ detail }}</li>
      </ul>
    </ConfirmDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import AccountStatus from '@/components/groupManagement/AccountStatus.vue'
import AccountUsageCard from '@/components/groupManagement/AccountUsageCard.vue'
import AddMemberDialog from '@/components/groupManagement/AddMemberDialog.vue'
import AddOwnedMemberDialog from '@/components/groupManagement/AddOwnedMemberDialog.vue'
import AssignAccountsDialog from '@/components/groupManagement/AssignAccountsDialog.vue'
import CreateGroupUserDialog from '@/components/groupManagement/CreateGroupUserDialog.vue'
import GroupAccountsPanel from '@/components/groupManagement/GroupAccountsPanel.vue'
import GroupInvitationsPanel from '@/components/groupManagement/GroupInvitationsPanel.vue'
import GroupManagersPanel from '@/components/groupManagement/GroupManagersPanel.vue'
import GroupMembersPanel from '@/components/groupManagement/GroupMembersPanel.vue'
import GroupNavList from '@/components/groupManagement/GroupNavList.vue'
import GroupSettingsPanel from '@/components/groupManagement/GroupSettingsPanel.vue'
import GroupSummaryCard, { type SummaryStat } from '@/components/groupManagement/GroupSummaryCard.vue'
import GroupTransfersPanel from '@/components/groupManagement/GroupTransfersPanel.vue'
import InviteMemberDialog from '@/components/groupManagement/InviteMemberDialog.vue'
import MemberLimitDialog from '@/components/groupManagement/MemberLimitDialog.vue'
import MyInvitationsCard from '@/components/groupManagement/MyInvitationsCard.vue'
import PlatformChip from '@/components/groupManagement/PlatformChip.vue'
import QuotaBar from '@/components/groupManagement/QuotaBar.vue'
import ResetPasswordDialog from '@/components/groupManagement/ResetPasswordDialog.vue'
import SubscriptionAccountsPanel from '@/components/groupManagement/SubscriptionAccountsPanel.vue'
import TransferBalanceDialog from '@/components/groupManagement/TransferBalanceDialog.vue'
import { formatUsdCompact, memberDisplayName } from '@/components/groupManagement/helpers'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { useGroupManagementStore } from '@/stores/groupManagement'
import { extractI18nErrorMessage } from '@/utils/apiError'
import { formatCountdown, formatCurrency } from '@/utils/format'
import type { GroupCategory } from '@/types'
import {
  groupManagementAPI,
  type AdminGroupManagementDirectory,
  type CreateGroupUserInput,
  type GroupAccountUsage,
  type GroupInvitation,
  type GroupManagementAccount,
  type GroupManagementMember,
  type GroupManagementOverview,
  type GroupManagementSettings,
  type GroupOwnedUser,
  type GroupTransferDirection,
  type MemberLimit
} from '@/api/groupManagement'

type TabKey = 'members' | 'accounts' | 'invitations' | 'transfers' | 'settings' | 'managers'
type TabIcon = 'users' | 'server' | 'mail' | 'dollar' | 'cog' | 'shield'

interface ConfirmState {
  title: string
  message: string
  details: string[]
  confirmText: string
  danger: boolean
  run: () => Promise<unknown>
}

const { t } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()
const groupManagementStore = useGroupManagementStore()

const overview = ref<GroupManagementOverview | null>(null)
const directory = ref<AdminGroupManagementDirectory | null>(null)
const loading = ref(false)
const loadError = ref('')
const selectedGroupId = ref<number | null>(null)
const activeTab = ref<TabKey>('members')

const detailsLoading = ref(false)
const detailsGroupId = ref<number | null>(null)
const members = ref<GroupManagementMember[]>([])
const accounts = ref<GroupManagementAccount[]>([])
const settings = ref<GroupManagementSettings | null>(null)
const mutating = ref(false)

const addMemberOpen = ref(false)
const limitOpen = ref(false)
const limitMember = ref<GroupManagementMember | null>(null)
const assignOpen = ref(false)
const assignMemberId = ref<number | null>(null)
const confirmOpen = ref(false)
const confirmState = ref<ConfirmState>({ title: '', message: '', details: [], confirmText: '', danger: false, run: async () => {} })

// 管理分组：组用户、余额划拨与分类
const createUserOpen = ref(false)
const createdCredentials = ref<{ email: string; password: string } | null>(null)
const transferOpen = ref(false)
const transferMember = ref<GroupManagementMember | null>(null)
const resetOpen = ref(false)
const resetMember = ref<GroupManagementMember | null>(null)
const resetDonePassword = ref<string | null>(null)
const ownedOpen = ref(false)
const ownedLoading = ref(false)
const ownedCandidates = ref<GroupOwnedUser[]>([])
const categorySaving = ref(false)
const transfersRefreshKey = ref(0)

// 邮箱邀请：组管理员发出的邀请与当前用户收到的邀请
const inviteOpen = ref(false)
const groupInvitations = ref<GroupInvitation[]>([])
const myInvitations = ref<GroupInvitation[]>([])
const invitationBusyId = ref<number | null>(null)

// 订阅组：账号限额与重置卡
const accountUsage = ref<GroupAccountUsage[]>([])
const accountUsageGroupId = ref<number | null>(null)
const accountUsageLoading = ref(false)
const accountActionId = ref<number | null>(null)

const groups = computed(() => overview.value?.manageable_groups ?? [])
const selectedGroup = computed(() => groups.value.find(group => group.id === selectedGroupId.value) ?? null)
const isAdmin = computed(() => overview.value?.role === 'admin')
const isGroupManagerRole = computed(() => overview.value?.role === 'group_manager')
const isManagedGroup = computed(() => selectedGroup.value?.kind === 'managed')
const managedType = computed(() => isManagedGroup.value ? (selectedGroup.value?.managed_type ?? null) : null)
const isQuotaGroup = computed(() => managedType.value === 'quota')
const isSubscriptionGroup = computed(() => managedType.value === 'subscription')
// 划拨从组管理员自己的余额出，只在额度组可用（订阅组不扣余额），超管不参与；
// 停用 / 重置密码组管理员与超管都可以
const canTransfer = computed(() => isQuotaGroup.value && isGroupManagerRole.value && Boolean(selectedGroup.value?.manager))
const canManageGroupUsers = computed(() => isManagedGroup.value && Boolean(selectedGroup.value?.manager))
const canInvite = computed(() => Boolean(selectedGroup.value?.manager))
const canAddOwned = computed(() => isManagedGroup.value && isGroupManagerRole.value)
const ownBalance = computed(() => overview.value?.balance ?? 0)
const detailsReady = computed(() => selectedGroupId.value !== null && detailsGroupId.value === selectedGroupId.value)
const visibleAccountUsage = computed(() => accountUsageGroupId.value === selectedGroupId.value ? accountUsage.value : [])
const pendingInvitationCount = computed(() => groupInvitations.value.filter(item => item.status === 'pending').length)

const ownMembership = computed(() => overview.value?.memberships.find(member => member.group_id === selectedGroupId.value) ?? null)
const ownAccounts = computed(() => overview.value?.assignments.filter(account => account.group_id === selectedGroupId.value) ?? [])
const ownRemaining = computed(() => ownMembership.value ? Math.max(ownMembership.value.daily_limit - ownMembership.value.daily_used, 0) : 0)
const ownEmptyHint = computed(() => {
  if (!selectedGroup.value?.enabled) return t('groupManagement.mine.emptyDisabled')
  return selectedGroup.value.allocation_mode === 'manual' ? t('groupManagement.mine.emptyManual') : ''
})

const memberIds = computed(() => members.value.map(member => member.user_id))
const memberEmails = computed(() => members.value.map(member => member.email ?? '').filter(Boolean))
const memberDefaults = computed<MemberLimit>(() => ({
  max_concurrent: settings.value?.max_concurrent ?? 1,
  daily_limit: settings.value?.daily_limit ?? 0,
  limit_5h_usd: settings.value?.default_limit_5h_usd ?? 0,
  limit_7d_usd: settings.value?.default_limit_7d_usd ?? 0
}))

const currentManagerIds = computed(() => directory.value?.groups.find(group => group.id === selectedGroupId.value)?.manager_user_ids ?? [])
const currentManagers = computed(() => currentManagerIds.value.map(id => {
  const user = directory.value?.users.find(item => item.id === id)
  return { id, name: user?.username || user?.email || `#${id}`, email: user?.email ?? '' }
}))
const managerCandidates = computed(() => (directory.value?.users ?? [])
  .filter(user => user.role === 'group_manager' && !currentManagerIds.value.includes(user.id))
  .map(user => ({ value: user.id, label: `${user.username || user.email} · #${user.id}` })))

const tabs = computed(() => {
  const group = selectedGroup.value
  const list: Array<{ key: TabKey; label: string; icon: TabIcon; count?: number }> = [
    {
      key: 'members',
      label: isManagedGroup.value ? t('groupManagement.tabs.groupUsers') : t('groupManagement.tabs.members'),
      icon: 'users',
      count: group?.member_count
    },
    { key: 'accounts', label: t('groupManagement.tabs.accounts'), icon: 'server', count: group?.account_count },
    { key: 'invitations', label: t('groupManagement.tabs.invitations'), icon: 'mail', count: pendingInvitationCount.value || undefined }
  ]
  if (isQuotaGroup.value) {
    list.push({ key: 'transfers', label: t('groupManagement.tabs.transfers'), icon: 'dollar' })
  }
  list.push({ key: 'settings', label: t('groupManagement.tabs.settings'), icon: 'cog' })
  if (isAdmin.value) {
    list.push({ key: 'managers', label: t('groupManagement.tabs.managers'), icon: 'shield', count: currentManagerIds.value.length })
  }
  return list
})

const summaryStats = computed<SummaryStat[]>(() => {
  const group = selectedGroup.value
  if (!group) return []
  const unlimited = t('groupManagement.unlimited')
  if (group.manager) {
    return [
      { key: 'members', label: t('groupManagement.summary.members'), value: group.member_count },
      { key: 'accounts', label: t('groupManagement.summary.accounts'), value: group.account_count },
      { key: 'concurrency', label: t('groupManagement.summary.defaultConcurrency'), value: group.max_concurrent },
      { key: 'daily', label: t('groupManagement.summary.defaultDailyLimit'), value: group.daily_limit > 0 ? group.daily_limit : unlimited }
    ]
  }
  const membership = ownMembership.value
  if (isQuotaGroup.value) {
    return [
      { key: 'usage5h', label: t('groupManagement.summary.usage5h'), value: formatCurrency(membership?.usage_5h_usd ?? 0) },
      { key: 'usage7d', label: t('groupManagement.summary.usage7d'), value: formatCurrency(membership?.usage_7d_usd ?? 0) },
      { key: 'used', label: t('groupManagement.summary.todayUsed'), value: membership?.daily_used ?? 0 },
      { key: 'balance', label: t('groupManagement.summary.myBalance'), value: formatCurrency(ownBalance.value) }
    ]
  }
  return [
    { key: 'used', label: t('groupManagement.summary.todayUsed'), value: membership?.daily_used ?? 0 },
    { key: 'daily', label: t('groupManagement.summary.dailyLimit'), value: membership && membership.daily_limit > 0 ? membership.daily_limit : unlimited },
    { key: 'concurrency', label: t('groupManagement.summary.concurrency'), value: membership?.max_concurrent ?? '-' },
    {
      key: 'accounts',
      label: t('groupManagement.summary.myAccounts'),
      value: isSubscriptionGroup.value ? visibleAccountUsage.value.length : ownAccounts.value.length
    }
  ]
})

watch(tabs, list => {
  if (!list.some(tab => tab.key === activeTab.value)) activeTab.value = 'members'
})

function errorMessage(error: unknown, fallbackKey: string) {
  return extractI18nErrorMessage(error, t, 'groupManagement.errors', t(fallbackKey))
}

function resetHint(resetAt?: string) {
  const countdown = formatCountdown(resetAt)
  return countdown ? t('groupManagement.members.resetsIn', { time: countdown }) : ''
}

async function loadOverview() {
  loading.value = true
  loadError.value = ''
  try {
    // 收到的邀请不影响控制台其他部分：接口异常时按没有邀请处理
    const [data, invitations] = await Promise.all([
      groupManagementAPI.getOverview(),
      groupManagementAPI.myInvitations().catch(() => [] as GroupInvitation[])
    ])
    overview.value = data
    myInvitations.value = invitations
    directory.value = data.role === 'admin' ? await groupManagementAPI.adminDirectory() : null
    if (!data.manageable_groups.some(group => group.id === selectedGroupId.value)) {
      selectedGroupId.value = data.manageable_groups[0]?.id ?? null
    }
  } catch (error) {
    loadError.value = errorMessage(error, 'groupManagement.loadFailed')
  } finally {
    loading.value = false
  }
}

let detailsRequest = 0
async function loadGroupDetails() {
  const id = selectedGroupId.value
  const request = ++detailsRequest
  if (!id || !selectedGroup.value?.manager) {
    members.value = []
    accounts.value = []
    settings.value = null
    groupInvitations.value = []
    detailsGroupId.value = null
    detailsLoading.value = false
    void loadAccountUsage()
    return
  }
  detailsLoading.value = true
  try {
    const [groupMembers, pool, groupSettings, invitations] = await Promise.all([
      groupManagementAPI.getMembers(id),
      groupManagementAPI.getAccounts(id),
      groupManagementAPI.getSettings(id),
      groupManagementAPI.listInvitations(id)
    ])
    if (request !== detailsRequest) return
    members.value = groupMembers
    accounts.value = pool
    groupInvitations.value = invitations
    settings.value = {
      enabled: groupSettings.enabled,
      allocation_mode: groupSettings.allocation_mode,
      max_concurrent: groupSettings.max_concurrent,
      daily_limit: groupSettings.daily_limit,
      managed: groupSettings.managed ?? false,
      managed_type: groupSettings.managed_type,
      default_limit_5h_usd: groupSettings.default_limit_5h_usd ?? 0,
      default_limit_7d_usd: groupSettings.default_limit_7d_usd ?? 0
    }
    detailsGroupId.value = id
    void loadAccountUsage()
  } catch (error) {
    if (request === detailsRequest) loadError.value = errorMessage(error, 'groupManagement.loadFailed')
  } finally {
    if (request === detailsRequest) detailsLoading.value = false
  }
}

// 订阅组的账号限额：组管理员看分组全部账号，组用户只看分配给自己的
let usageRequest = 0
async function loadAccountUsage() {
  const id = selectedGroupId.value
  const request = ++usageRequest
  if (!id || !isSubscriptionGroup.value) {
    accountUsage.value = []
    accountUsageGroupId.value = null
    accountUsageLoading.value = false
    return
  }
  accountUsageLoading.value = true
  try {
    const items = await groupManagementAPI.getAccountUsage(id)
    if (request !== usageRequest) return
    accountUsage.value = items
    accountUsageGroupId.value = id
  } catch (error) {
    if (request === usageRequest) appStore.showError(errorMessage(error, 'groupManagement.accountUsage.loadFailed'))
  } finally {
    if (request === usageRequest) accountUsageLoading.value = false
  }
}

async function refresh() {
  await loadOverview()
  await loadGroupDetails()
}

function selectGroup(id: number) {
  if (id === selectedGroupId.value) return
  selectedGroupId.value = id
  void loadGroupDetails()
}

// Stay busy until the refreshed data arrives so forms cannot resubmit stale drafts.
async function mutate(action: () => Promise<unknown>, successKey: string): Promise<boolean> {
  mutating.value = true
  try {
    await action()
    appStore.showSuccess(t(successKey))
    await refresh()
    return true
  } catch (error) {
    appStore.showError(errorMessage(error, 'groupManagement.saveFailed'))
    return false
  } finally {
    mutating.value = false
  }
}

function openConfirm(state: Omit<ConfirmState, 'details'> & { details?: string[] }) {
  confirmState.value = { ...state, details: state.details ?? [] }
  confirmOpen.value = true
}

async function runConfirmed() {
  confirmOpen.value = false
  await confirmState.value.run()
}

function onTabKeydown(event: KeyboardEvent) {
  const keys = tabs.value.map(tab => tab.key)
  const index = keys.indexOf(activeTab.value)
  let next: number
  if (event.key === 'ArrowRight') next = (index + 1) % keys.length
  else if (event.key === 'ArrowLeft') next = (index - 1 + keys.length) % keys.length
  else if (event.key === 'Home') next = 0
  else if (event.key === 'End') next = keys.length - 1
  else return
  event.preventDefault()
  activeTab.value = keys[next]
  void nextTick(() => document.getElementById(`gm-tab-${keys[next]}`)?.focus())
}

async function submitAddMember(userId: number) {
  const groupId = selectedGroupId.value
  if (!groupId) return
  if (await mutate(() => groupManagementAPI.addMember(groupId, userId), 'groupManagement.toast.memberAdded')) {
    addMemberOpen.value = false
  }
}

async function submitInvite(email: string) {
  const groupId = selectedGroupId.value
  if (!groupId) return
  if (await mutate(() => groupManagementAPI.inviteMember(groupId, email), 'groupManagement.toast.invitationSent')) {
    inviteOpen.value = false
  }
}

function requestRevokeInvitation(invitation: GroupInvitation) {
  const groupId = selectedGroupId.value
  if (!groupId) return
  openConfirm({
    title: t('groupManagement.invitations.revokeTitle'),
    message: t('groupManagement.invitations.confirmRevoke', { email: invitation.email }),
    confirmText: t('groupManagement.invitations.revoke'),
    danger: true,
    run: () => mutate(() => groupManagementAPI.revokeInvitation(groupId, invitation.id), 'groupManagement.toast.invitationRevoked')
  })
}

async function respondInvitation(invitation: GroupInvitation, accept: boolean) {
  if (invitationBusyId.value !== null) return
  invitationBusyId.value = invitation.id
  try {
    if (accept) await groupManagementAPI.acceptInvitation(invitation.id)
    else await groupManagementAPI.declineInvitation(invitation.id)
    appStore.showSuccess(t(accept ? 'groupManagement.toast.invitationAccepted' : 'groupManagement.toast.invitationDeclined'))
    if (accept) selectedGroupId.value = invitation.group_id
    await refresh()
    // 侧栏入口按所属分组与待处理邀请显示，处理完立即刷新
    void groupManagementStore.ensureSummary(authStore.user?.id, true)
  } catch (error) {
    appStore.showError(errorMessage(error, 'groupManagement.saveFailed'))
  } finally {
    invitationBusyId.value = null
  }
}

function openLimit(member: GroupManagementMember) {
  limitMember.value = member
  limitOpen.value = true
}

async function submitLimit(limit: MemberLimit) {
  const groupId = selectedGroupId.value
  const member = limitMember.value
  if (!groupId || !member) return
  if (await mutate(() => groupManagementAPI.updateMemberLimit(groupId, member.user_id, limit), 'groupManagement.toast.limitSaved')) {
    limitOpen.value = false
  }
}

function openAssign(member?: GroupManagementMember) {
  assignMemberId.value = member?.user_id ?? null
  assignOpen.value = true
}

async function submitAssign(payload: { userId: number; accountIds: number[] }) {
  const groupId = selectedGroupId.value
  if (!groupId) return
  const saved = await mutate(
    () => groupManagementAPI.setMemberAccounts(groupId, payload.userId, { account_ids: payload.accountIds, mode: 'manual' }),
    'groupManagement.toast.assignmentsSaved'
  )
  if (saved) assignOpen.value = false
}

function requestRemoveMember(member: GroupManagementMember) {
  const groupId = selectedGroupId.value
  if (!groupId) return
  openConfirm({
    title: t('groupManagement.members.removeTitle'),
    message: isManagedGroup.value
      ? t('groupManagement.members.confirmRemoveManaged', { name: memberDisplayName(member) })
      : t('groupManagement.members.confirmRemove', { name: memberDisplayName(member) }),
    confirmText: t('groupManagement.members.remove'),
    danger: true,
    run: () => mutate(() => groupManagementAPI.removeMember(groupId, member.user_id), 'groupManagement.toast.memberRemoved')
  })
}

function requestSaveSettings(next: GroupManagementSettings) {
  const groupId = selectedGroupId.value
  const current = settings.value
  if (!groupId || !current) return
  const save = () => mutate(() => groupManagementAPI.updateSettings(groupId, next), 'groupManagement.toast.settingsSaved')
  const clearsAssignments = next.allocation_mode === 'auto' && current.allocation_mode === 'manual' &&
    accounts.value.some(account => account.assigned_user_ids.length > 0)
  const details: string[] = []
  if (next.enabled && !current.enabled) details.push(t('groupManagement.settings.confirmEnable'))
  if (clearsAssignments) details.push(t('groupManagement.settings.confirmToAuto'))
  if (next.allocation_mode === 'manual' && current.allocation_mode === 'auto' && next.enabled) {
    details.push(t('groupManagement.settings.confirmToManual'))
  }
  if (!details.length) {
    void save()
    return
  }
  openConfirm({
    title: t('groupManagement.settings.confirmTitle'),
    message: t('groupManagement.settings.confirmMessage'),
    details,
    confirmText: t('groupManagement.settings.save'),
    danger: clearsAssignments,
    run: save
  })
}

async function assignManager(userId: number) {
  const groupId = selectedGroupId.value
  if (!groupId) return
  await mutate(() => groupManagementAPI.adminAssignManager(groupId, userId), 'groupManagement.toast.managerAssigned')
}

function requestRevokeManager(userId: number) {
  const groupId = selectedGroupId.value
  const manager = currentManagers.value.find(item => item.id === userId)
  if (!groupId) return
  openConfirm({
    title: t('groupManagement.managers.revokeTitle'),
    message: t('groupManagement.managers.confirmRevoke', { name: manager?.name ?? `#${userId}` }),
    confirmText: t('groupManagement.managers.revoke'),
    danger: true,
    run: () => mutate(() => groupManagementAPI.adminRevokeManager(groupId, userId), 'groupManagement.toast.managerRevoked')
  })
}

function onPanelError(error: unknown) {
  appStore.showError(errorMessage(error, 'groupManagement.loadFailed'))
}

function replaceAccountUsage(updated: GroupAccountUsage) {
  accountUsage.value = accountUsage.value.map(item => item.account_id === updated.account_id ? updated : item)
}

async function refreshAccountQuota(item: GroupAccountUsage) {
  const groupId = selectedGroupId.value
  if (!groupId || accountActionId.value !== null) return
  accountActionId.value = item.account_id
  try {
    replaceAccountUsage(await groupManagementAPI.refreshAccountQuota(groupId, item.account_id))
    appStore.showSuccess(t('groupManagement.toast.quotaRefreshed'))
  } catch (error) {
    appStore.showError(errorMessage(error, 'groupManagement.accountUsage.refreshFailed'))
  } finally {
    accountActionId.value = null
  }
}

function requestResetCredit(item: GroupAccountUsage) {
  openConfirm({
    title: t('groupManagement.accountUsage.resetTitle'),
    message: t('groupManagement.accountUsage.confirmReset', { name: item.name, count: item.reset_credits?.available_count ?? 0 }),
    details: [t('groupManagement.accountUsage.resetDetail')],
    confirmText: t('groupManagement.accountUsage.useCredit'),
    danger: false,
    run: () => resetAccountCredit(item)
  })
}

async function resetAccountCredit(item: GroupAccountUsage) {
  const groupId = selectedGroupId.value
  if (!groupId || accountActionId.value !== null) return
  accountActionId.value = item.account_id
  try {
    const result = await groupManagementAPI.resetAccountCredit(groupId, item.account_id)
    if (result.account) replaceAccountUsage(result.account)
    if (result.warning_code) appStore.showWarning(t('groupManagement.toast.creditUsedWithWarning'))
    else appStore.showSuccess(t('groupManagement.toast.creditUsed'))
    if (!result.account) await loadAccountUsage()
  } catch (error) {
    appStore.showError(errorMessage(error, 'groupManagement.accountUsage.resetFailed'))
  } finally {
    accountActionId.value = null
  }
}

async function updateCategory(category: GroupCategory) {
  const groupId = selectedGroupId.value
  if (!groupId || categorySaving.value) return
  categorySaving.value = true
  try {
    await groupManagementAPI.updateCategory(groupId, category)
    appStore.showSuccess(t('groupManagement.toast.categorySaved'))
    await loadOverview()
  } catch (error) {
    appStore.showError(errorMessage(error, 'groupManagement.saveFailed'))
  } finally {
    categorySaving.value = false
  }
}

function openCreateUser() {
  createdCredentials.value = null
  createUserOpen.value = true
}

function closeCreateUser() {
  createUserOpen.value = false
  createdCredentials.value = null
}

async function submitCreateUser(input: CreateGroupUserInput) {
  const groupId = selectedGroupId.value
  if (!groupId) return
  const created = await mutate(() => groupManagementAPI.createGroupUser(groupId, input), 'groupManagement.toast.userCreated')
  // 保持对话框打开，展示一次性的登录信息
  if (created) createdCredentials.value = { email: input.email, password: input.password }
}

function openTransfer(member: GroupManagementMember) {
  transferMember.value = member
  transferOpen.value = true
}

async function submitTransfer(payload: { direction: GroupTransferDirection; amount: number; notes: string }) {
  const groupId = selectedGroupId.value
  const member = transferMember.value
  if (!groupId || !member) return
  const toast = payload.direction === 'grant' ? 'groupManagement.toast.transferGranted' : 'groupManagement.toast.transferReclaimed'
  if (await mutate(() => groupManagementAPI.transferBalance(groupId, member.user_id, payload), toast)) {
    transferOpen.value = false
    transfersRefreshKey.value += 1
  }
}

function openResetPassword(member: GroupManagementMember) {
  resetMember.value = member
  resetDonePassword.value = null
  resetOpen.value = true
}

function closeResetPassword() {
  resetOpen.value = false
  resetDonePassword.value = null
}

async function submitResetPassword(password: string) {
  const groupId = selectedGroupId.value
  const member = resetMember.value
  if (!groupId || !member) return
  if (await mutate(() => groupManagementAPI.resetGroupUserPassword(groupId, member.user_id, password), 'groupManagement.toast.passwordReset')) {
    resetDonePassword.value = password
  }
}

function requestToggleStatus(member: GroupManagementMember) {
  const groupId = selectedGroupId.value
  if (!groupId) return
  const enabling = member.status === 'disabled'
  const name = memberDisplayName(member)
  openConfirm({
    title: enabling ? t('groupManagement.members.enableTitle') : t('groupManagement.members.disableTitle'),
    message: enabling
      ? t('groupManagement.members.confirmEnable', { name })
      : t('groupManagement.members.confirmDisable', { name }),
    confirmText: enabling ? t('groupManagement.members.enable') : t('groupManagement.members.disable'),
    danger: !enabling,
    run: () => mutate(
      () => groupManagementAPI.setGroupUserStatus(groupId, member.user_id, enabling ? 'active' : 'disabled'),
      enabling ? 'groupManagement.toast.userEnabled' : 'groupManagement.toast.userDisabled'
    )
  })
}

async function openAddOwned() {
  const groupId = selectedGroupId.value
  if (!groupId) return
  ownedCandidates.value = []
  ownedOpen.value = true
  ownedLoading.value = true
  try {
    ownedCandidates.value = await groupManagementAPI.getOwnedUsers(groupId)
  } catch (error) {
    ownedOpen.value = false
    appStore.showError(errorMessage(error, 'groupManagement.loadFailed'))
  } finally {
    ownedLoading.value = false
  }
}

async function submitAddOwned(userId: number) {
  const groupId = selectedGroupId.value
  if (!groupId) return
  if (await mutate(() => groupManagementAPI.addMember(groupId, userId), 'groupManagement.toast.memberAdded')) {
    ownedOpen.value = false
  }
}

onMounted(() => { void refresh() })
</script>
