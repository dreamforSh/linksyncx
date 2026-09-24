<template>
  <AppLayout>
    <main class="mx-auto max-w-6xl space-y-6 text-gray-800 dark:text-gray-200">
      <header class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-200 pb-4 dark:border-dark-700">
        <h1 class="text-xl font-semibold text-gray-900 dark:text-white">{{ t('groupManagement.title') }}</h1>
        <button type="button" class="btn btn-secondary" :disabled="loading" @click="refresh">
          <Icon name="refresh" size="sm" :class="{ 'animate-spin': loading }" />
          {{ t('common.refresh') }}
        </button>
      </header>

      <div v-if="errorMessage" role="alert" class="border-l-4 border-red-500 bg-red-50 px-4 py-3 text-sm text-red-800 dark:bg-red-950/30 dark:text-red-300">{{ errorMessage }}</div>
      <p v-if="loading && !overview" role="status" class="py-10 text-sm text-gray-500">{{ t('common.loading') }}</p>
      <template v-else-if="overview">
        <div v-if="overview.manageable_groups.length" class="flex flex-wrap items-center gap-3">
          <label for="managed-group" class="text-sm font-medium">{{ t('groupManagement.groups.title') }}</label>
          <select id="managed-group" v-model.number="selectedGroupId" class="input w-full sm:w-72">
            <option v-for="group in overview.manageable_groups" :key="group.id" :value="group.id">{{ group.name }}</option>
          </select>
          <span v-if="selectedGroup?.manager" class="text-sm text-gray-500 dark:text-gray-400">
            {{ selectedGroup.member_count }} {{ t('groupManagement.groups.members') }} ·
            {{ selectedGroup.account_count }} {{ t('groupManagement.groups.accountPool') }}
          </span>
        </div>
        <p v-else class="py-10 text-sm text-gray-500">{{ t('groupManagement.emptyGroups') }}</p>

        <template v-if="selectedGroup">
          <p v-if="detailsLoading && selectedGroup.manager" role="status" class="text-sm text-gray-500">{{ t('common.loading') }}</p>
          <template v-if="!selectedGroup.manager || !detailsLoading">
            <section v-if="selectedGroup.manager" class="border-t border-gray-200 pt-5 dark:border-dark-700">
              <h2 class="mb-4 text-base font-semibold text-gray-900 dark:text-white">{{ t('groupManagement.settings.title') }}</h2>
              <form class="flex flex-wrap items-end gap-4" @submit.prevent="saveSettings">
                <label class="flex items-center gap-2 pb-2 text-sm">
                  <input v-model="settingsForm.enabled" type="checkbox" />{{ t('groupManagement.settings.enabled') }}
                </label>
                <fieldset class="text-sm">
                  <legend class="mb-1">{{ t('groupManagement.settings.mode') }}</legend>
                  <div class="inline-flex overflow-hidden rounded border border-gray-300 dark:border-dark-600">
                    <label v-for="mode in modes" :key="mode" class="cursor-pointer px-3 py-2" :class="settingsForm.allocation_mode === mode ? 'bg-primary-600 text-white' : 'bg-white dark:bg-dark-800'">
                      <input v-model="settingsForm.allocation_mode" type="radio" :value="mode" class="sr-only" />
                      {{ modeLabel(mode) }}
                    </label>
                  </div>
                </fieldset>
                <label class="text-sm">{{ t('groupManagement.settings.maxConcurrent') }}
                  <input v-model.number="settingsForm.max_concurrent" type="number" min="1" step="1" required class="input mt-1 block w-32" />
                </label>
                <label class="text-sm">{{ t('groupManagement.settings.dailyLimit') }}
                  <input v-model.number="settingsForm.daily_limit" type="number" min="0" step="1" required class="input mt-1 block w-32" />
                </label>
                <button class="btn btn-primary" :disabled="savingSettings || detailsLoading">{{ t('common.save') }}</button>
              </form>
            </section>

            <section class="border-t border-gray-200 pt-5 dark:border-dark-700">
              <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
                <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('groupManagement.members.title') }}</h2>
                <form v-if="selectedGroup.manager" class="flex items-end gap-2" @submit.prevent="addMember">
                  <label class="text-sm">{{ t('groupManagement.members.userId') }}
                    <input v-model.number="newMemberId" type="number" min="1" step="1" required class="input mt-1 block w-32" />
                  </label>
                  <button class="btn btn-secondary" :disabled="!validId(newMemberId) || savingMember">
                    <Icon name="plus" size="sm" />{{ t('groupManagement.members.add') }}
                  </button>
                </form>
              </div>
              <div v-if="!shownMembers.length" class="py-5 text-sm text-gray-500">{{ t('groupManagement.emptyMembers') }}</div>
              <div v-else class="overflow-x-auto">
                <table class="w-full min-w-[520px] text-left text-sm">
                  <thead class="border-b border-gray-200 text-xs text-gray-500 dark:border-dark-700">
                    <tr><th class="py-2 pr-4">{{ t('groupManagement.members.member') }}</th><th class="py-2 pr-4">{{ t('groupManagement.members.quota') }}</th><th class="py-2 pr-4">{{ t('groupManagement.settings.maxConcurrent') }}</th><th v-if="selectedGroup.manager" class="py-2 text-right">{{ t('groupManagement.members.actions') }}</th></tr>
                  </thead>
                  <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                    <tr v-for="member in shownMembers" :key="member.user_id">
                      <td class="py-3 pr-4"><div class="font-medium">{{ member.username || member.email || member.user_id }}</div><div v-if="selectedGroup.manager" class="text-xs text-gray-500">{{ member.email }} · #{{ member.user_id }}</div></td>
                      <td class="py-3 pr-4">{{ formatQuota(member.daily_used, member.daily_limit) }}</td>
                      <td class="py-3 pr-4">{{ member.max_concurrent }}</td>
                      <td v-if="selectedGroup.manager" class="whitespace-nowrap py-3 text-right">
                        <button type="button" class="mr-3 text-primary-600" :title="t('common.edit')" :aria-label="t('common.edit')" @click="editMember(member)"><Icon name="edit" size="sm" /></button>
                        <button type="button" class="text-red-600" :title="t('groupManagement.members.remove')" :aria-label="t('groupManagement.members.remove')" :disabled="savingMember" @click="removeMember(member)"><Icon name="trash" size="sm" /></button>
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>
              <form v-if="editingMember && selectedGroup.manager" class="mt-4 flex flex-wrap items-end gap-3 border-t border-gray-200 pt-4 dark:border-dark-700" @submit.prevent="saveMember">
                <span class="w-full text-sm font-medium">{{ t('groupManagement.editor.title', { name: editingMember.username || editingMember.email || editingMember.user_id }) }}</span>
                <label class="text-sm">{{ t('groupManagement.settings.maxConcurrent') }}<input v-model.number="memberForm.max_concurrent" type="number" min="1" step="1" required class="input mt-1 block w-32" /></label>
                <label class="text-sm">{{ t('groupManagement.settings.dailyLimit') }}<input v-model.number="memberForm.daily_limit" type="number" min="0" step="1" required class="input mt-1 block w-32" /></label>
                <button class="btn btn-primary" :disabled="savingMember">{{ t('common.save') }}</button>
                <button type="button" class="btn btn-secondary" @click="editingMember = null">{{ t('common.cancel') }}</button>
              </form>
            </section>

            <section class="border-t border-gray-200 pt-5 dark:border-dark-700">
              <h2 class="mb-4 text-base font-semibold text-gray-900 dark:text-white">{{ t('groupManagement.accounts.title') }}</h2>
              <template v-if="selectedGroup.manager">
                <div v-if="settingsForm.allocation_mode === 'manual'" class="mb-4 flex flex-wrap items-end gap-3">
                  <label class="text-sm">{{ t('groupManagement.accounts.selectMember') }}
                    <select v-model.number="assignmentMemberId" class="input mt-1 block w-full min-w-48">
                      <option :value="null">{{ t('groupManagement.accounts.selectMember') }}</option>
                      <option v-for="member in members" :key="member.user_id" :value="member.user_id">{{ member.username || member.email || member.user_id }}</option>
                    </select>
                  </label>
                  <button v-if="assignmentMemberId !== null" type="button" class="btn btn-primary" :disabled="savingAssignments" @click="saveAssignments">{{ t('common.save') }}</button>
                </div>
              </template>
              <p v-if="!visibleAccounts.length" class="py-5 text-sm text-gray-500">{{ t('groupManagement.emptyAccounts') }}</p>
              <div v-else class="overflow-x-auto">
                <table class="w-full min-w-[520px] text-left text-sm">
                  <thead class="border-b border-gray-200 text-xs text-gray-500 dark:border-dark-700"><tr><th v-if="selectedGroup.manager && settingsForm.allocation_mode === 'manual' && assignmentMemberId !== null" class="py-2 pr-3">{{ t('groupManagement.accounts.select') }}</th><th class="py-2 pr-4">{{ t('groupManagement.accounts.account') }}</th><th class="py-2 pr-4">{{ t('groupManagement.accounts.status') }}</th><th class="py-2 pr-4">{{ t('groupManagement.accounts.remaining') }}</th><th v-if="selectedGroup.manager" class="py-2">{{ t('groupManagement.accounts.assignedTo') }}</th></tr></thead>
                  <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                    <tr v-for="account in visibleAccounts" :key="account.id">
                      <td v-if="selectedGroup.manager && settingsForm.allocation_mode === 'manual' && assignmentMemberId !== null" class="py-3 pr-3"><input v-model="assignmentAccountIds" type="checkbox" :value="account.id" :aria-label="account.name" /></td>
                      <td class="py-3 pr-4"><div class="font-medium">{{ account.name }}</div><div class="text-xs text-gray-500">{{ account.platform }}</div></td>
                      <td class="py-3 pr-4">{{ account.status }}<span v-if="selectedGroup.manager && 'schedulable' in account && !account.schedulable" class="ml-2 text-amber-700">{{ t('groupManagement.accounts.unschedulable') }}</span></td>
                      <td class="py-3 pr-4">{{ selectedGroup.manager ? '-' : formatRemaining(account.remaining_quota) }}</td>
                      <td v-if="selectedGroup.manager" class="py-3">{{ assignedUsers(account) }}</td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </section>
          </template>
        </template>

        <section v-if="overview.role === 'admin'" class="border-t border-gray-200 pt-5 dark:border-dark-700">
          <div class="mb-4 flex flex-wrap items-center gap-4">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('groupManagement.admin.title') }}</h2>
            <router-link to="/admin/users" class="text-sm text-primary-600 hover:underline">{{ t('groupManagement.admin.changeRole') }}</router-link>
          </div>
          <div class="flex flex-wrap items-end gap-3">
            <label class="text-sm">{{ t('groupManagement.admin.selectUser') }}
              <select v-model.number="adminManagerUserId" class="input mt-1 block w-full min-w-48">
                <option :value="null">{{ t('groupManagement.admin.selectUser') }}</option>
                <option v-for="user in assignableUsers" :key="user.id" :value="user.id">{{ user.username || user.email }} · #{{ user.id }}</option>
              </select>
            </label>
            <button type="button" class="btn btn-primary" :disabled="!selectedGroupId || !adminManagerUserId || savingManager" @click="assignManager">{{ t('groupManagement.admin.assign') }}</button>
          </div>
          <ul v-if="currentManagers.length" class="mt-4 divide-y divide-gray-100 text-sm dark:divide-dark-700">
            <li v-for="userId in currentManagers" :key="userId" class="flex items-center justify-between gap-4 py-2">
              <span>{{ managerName(userId) }} · #{{ userId }}</span>
              <button type="button" class="text-red-600" :disabled="savingManager" @click="revokeManager(userId)">{{ t('groupManagement.admin.revoke') }}</button>
            </li>
          </ul>
        </section>
      </template>
    </main>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import { groupManagementAPI, type AllocationMode, type AdminGroupManagementDirectory, type GroupManagementAccount, type GroupManagementAssignment, type GroupManagementMember, type GroupManagementOverview, type GroupManagementSettings, type MemberLimit } from '@/api/groupManagement'

const { t } = useI18n()
const modes: AllocationMode[] = ['auto', 'manual']
const overview = ref<GroupManagementOverview | null>(null)
const directory = ref<AdminGroupManagementDirectory | null>(null)
const loading = ref(false)
const detailsLoading = ref(false)
const errorMessage = ref('')
const selectedGroupId = ref<number | null>(null)
const members = ref<GroupManagementMember[]>([])
const accounts = ref<GroupManagementAccount[]>([])
const settingsForm = ref<GroupManagementSettings>({ enabled: true, allocation_mode: 'auto', max_concurrent: 1, daily_limit: 0 })
const memberForm = ref<MemberLimit>({ max_concurrent: 1, daily_limit: 0 })
const editingMember = ref<GroupManagementMember | null>(null)
const newMemberId = ref<number | null>(null)
const assignmentMemberId = ref<number | null>(null)
const assignmentAccountIds = ref<number[]>([])
const adminManagerUserId = ref<number | null>(null)
const savingSettings = ref(false)
const savingMember = ref(false)
const savingAssignments = ref(false)
const savingManager = ref(false)

const selectedGroup = computed(() => overview.value?.manageable_groups.find(group => group.id === selectedGroupId.value) ?? null)
const ownMemberships = computed(() => overview.value?.memberships.filter(member => member.group_id === selectedGroupId.value) ?? [])
const shownMembers = computed(() => selectedGroup.value?.manager ? members.value : ownMemberships.value)
const visibleAccounts = computed(() => selectedGroup.value?.manager
  ? accounts.value
  : (overview.value?.assignments.filter(account => account.group_id === selectedGroupId.value) ?? []))
const assignableUsers = computed(() => directory.value?.users.filter(user => user.role === 'group_manager') ?? [])
const currentManagers = computed(() => directory.value?.groups.find(group => group.id === selectedGroupId.value)?.manager_user_ids ?? [])

function validId(value: number | null): value is number { return value !== null && Number.isSafeInteger(value) && value > 0 }
function validLimit(value: number, min: number) { return Number.isSafeInteger(value) && value >= min }
function modeLabel(mode: AllocationMode) { return t(`groupManagement.settings.${mode}`) }
function formatQuota(used: number, limit: number) { return `${used} / ${limit === 0 ? '∞' : limit}` }
function formatRemaining(quota: number | null) { return quota === null ? '∞' : quota }
function managerName(id: number) { const user = directory.value?.users.find(item => item.id === id); return user?.username || user?.email || String(id) }
function fail(error: unknown) { errorMessage.value = error instanceof Error ? error.message : t('groupManagement.saveFailed') }
function assignedUsers(account: GroupManagementAccount | GroupManagementAssignment) {
  if (!('assigned_user_ids' in account) || !account.assigned_user_ids.length) return t('groupManagement.accounts.unassigned')
  return account.assigned_user_ids.join(', ')
}

async function loadOverview() {
  loading.value = true
  errorMessage.value = ''
  try {
    const data = await groupManagementAPI.getOverview()
    overview.value = data
    if (data.role === 'admin') directory.value = await groupManagementAPI.adminDirectory()
    if (!data.manageable_groups.some(group => group.id === selectedGroupId.value)) {
      selectedGroupId.value = data.manageable_groups[0]?.id ?? null
    }
  } catch (error) { fail(error) } finally { loading.value = false }
}

let detailsRequest = 0
async function loadGroupDetails() {
  const id = selectedGroupId.value
  const request = ++detailsRequest
  members.value = []
  accounts.value = []
  editingMember.value = null
  assignmentMemberId.value = null
  if (!id || !selectedGroup.value?.manager) { detailsLoading.value = false; return }
  detailsLoading.value = true
  try {
    const [groupMembers, pool, settings] = await Promise.all([
      groupManagementAPI.getMembers(id), groupManagementAPI.getAccounts(id), groupManagementAPI.getSettings(id)
    ])
    if (request !== detailsRequest) return
    members.value = groupMembers
    accounts.value = pool
    settingsForm.value = { enabled: settings.enabled, allocation_mode: settings.allocation_mode, max_concurrent: settings.max_concurrent, daily_limit: settings.daily_limit }
  } catch (error) { if (request === detailsRequest) fail(error) }
  finally { if (request === detailsRequest) detailsLoading.value = false }
}

async function refresh() { await loadOverview(); await loadGroupDetails() }
async function refreshGroup() { await loadOverview(); await loadGroupDetails() }

async function saveSettings() {
  if (!selectedGroupId.value || !validLimit(settingsForm.value.max_concurrent, 1) || !validLimit(settingsForm.value.daily_limit, 0)) return
  savingSettings.value = true
  errorMessage.value = ''
  try { await groupManagementAPI.updateSettings(selectedGroupId.value, settingsForm.value); await refreshGroup() }
  catch (error) { fail(error) } finally { savingSettings.value = false }
}

async function addMember() {
  if (!selectedGroupId.value || !validId(newMemberId.value)) return
  savingMember.value = true
  errorMessage.value = ''
  try { await groupManagementAPI.addMember(selectedGroupId.value, newMemberId.value); newMemberId.value = null; await refreshGroup() }
  catch (error) { fail(error) } finally { savingMember.value = false }
}

async function removeMember(member: GroupManagementMember) {
  if (!selectedGroupId.value || !window.confirm(t('groupManagement.members.confirmRemove', { name: member.username || member.email || member.user_id }))) return
  savingMember.value = true
  errorMessage.value = ''
  try { await groupManagementAPI.removeMember(selectedGroupId.value, member.user_id); await refreshGroup() }
  catch (error) { fail(error) } finally { savingMember.value = false }
}

function editMember(member: GroupManagementMember) {
  editingMember.value = member
  memberForm.value = { max_concurrent: member.max_concurrent, daily_limit: member.daily_limit }
}
async function saveMember() {
  if (!selectedGroupId.value || !editingMember.value || !validLimit(memberForm.value.max_concurrent, 1) || !validLimit(memberForm.value.daily_limit, 0)) return
  savingMember.value = true
  errorMessage.value = ''
  try { await groupManagementAPI.updateMemberLimit(selectedGroupId.value, editingMember.value.user_id, memberForm.value); await refreshGroup() }
  catch (error) { fail(error) } finally { savingMember.value = false }
}

async function saveAssignments() {
  if (!selectedGroupId.value || !validId(assignmentMemberId.value) || settingsForm.value.allocation_mode !== 'manual') return
  const memberId = assignmentMemberId.value
  savingAssignments.value = true
  errorMessage.value = ''
  try {
    await groupManagementAPI.setMemberAccounts(selectedGroupId.value, memberId, {
      account_ids: assignmentAccountIds.value, mode: 'manual'
    })
    await refreshGroup()
    assignmentMemberId.value = memberId
  } catch (error) { fail(error) } finally { savingAssignments.value = false }
}

async function assignManager() {
  if (!selectedGroupId.value || !adminManagerUserId.value || !assignableUsers.value.some(user => user.id === adminManagerUserId.value)) return
  savingManager.value = true
  errorMessage.value = ''
  try { await groupManagementAPI.adminAssignManager(selectedGroupId.value, adminManagerUserId.value); adminManagerUserId.value = null; await refreshGroup() }
  catch (error) { fail(error) } finally { savingManager.value = false }
}
async function revokeManager(userId: number) {
  if (!selectedGroupId.value || !window.confirm(t('groupManagement.admin.confirmRevoke', { name: managerName(userId) }))) return
  savingManager.value = true
  errorMessage.value = ''
  try { await groupManagementAPI.adminRevokeManager(selectedGroupId.value, userId); await refreshGroup() }
  catch (error) { fail(error) } finally { savingManager.value = false }
}

watch(selectedGroupId, () => { void loadGroupDetails() })
watch(assignmentMemberId, id => {
  assignmentAccountIds.value = id === null ? [] : accounts.value.filter(account => account.assigned_user_ids.includes(id)).map(account => account.id)
})
onMounted(() => { void refresh() })
</script>
