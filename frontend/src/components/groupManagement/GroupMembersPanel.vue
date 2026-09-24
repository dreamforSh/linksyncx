<template>
  <section class="card overflow-hidden">
    <header class="flex flex-wrap items-center gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
      <div class="relative min-w-[12rem] flex-1 sm:max-w-xs">
        <Icon
          name="search"
          size="sm"
          class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 dark:text-dark-400"
        />
        <input
          v-model.trim="query"
          type="search"
          class="input py-2 pl-9"
          :placeholder="t('groupManagement.members.search')"
          :aria-label="t('groupManagement.members.search')"
        />
      </div>
      <button type="button" class="btn btn-primary btn-md ml-auto" :disabled="busy" @click="emit('add')">
        <Icon name="userPlus" size="sm" />
        {{ t('groupManagement.members.add') }}
      </button>
    </header>

    <div v-if="!members.length" class="empty-state">
      <div class="mb-4 flex h-14 w-14 items-center justify-center rounded-2xl bg-gray-100 dark:bg-dark-700">
        <Icon name="users" size="lg" class="text-gray-400 dark:text-dark-400" />
      </div>
      <h3 class="empty-state-title text-base">{{ t('groupManagement.members.empty') }}</h3>
      <p class="empty-state-description">{{ t('groupManagement.members.emptyHint') }}</p>
    </div>

    <p v-else-if="!filteredMembers.length" class="px-5 py-10 text-center text-sm text-gray-500 dark:text-dark-400">
      {{ t('groupManagement.members.noMatch') }}
    </p>

    <div v-else class="overflow-x-auto">
      <table class="table min-w-[720px]">
        <thead>
          <tr class="whitespace-nowrap">
            <th scope="col">{{ t('groupManagement.members.member') }}</th>
            <th scope="col">{{ t('groupManagement.members.todayUsage') }}</th>
            <th scope="col">{{ t('groupManagement.members.concurrency') }}</th>
            <th v-if="manualMode" scope="col">{{ t('groupManagement.members.assignedAccounts') }}</th>
            <!-- `.table th` forces text-left with higher specificity than a plain utility. -->
            <th scope="col" class="!text-right">{{ t('groupManagement.members.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="member in filteredMembers" :key="member.user_id">
            <td>
              <div class="flex min-w-0 items-center gap-3">
                <span
                  class="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-primary-100 text-sm font-semibold text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
                  aria-hidden="true"
                >
                  {{ initialOf(memberDisplayName(member)) }}
                </span>
                <div class="min-w-0 max-w-[14rem]">
                  <div class="truncate font-medium text-gray-900 dark:text-white">{{ memberDisplayName(member) }}</div>
                  <div class="truncate text-xs text-gray-500 dark:text-dark-400">{{ memberSubtitle(member) }}</div>
                </div>
              </div>
            </td>
            <td>
              <QuotaBar
                :used="member.daily_used"
                :limit="member.daily_limit"
                :label="`${memberDisplayName(member)} · ${t('groupManagement.members.todayUsage')}`"
              />
            </td>
            <td class="tabular-nums">{{ member.max_concurrent }}</td>
            <td v-if="manualMode">
              <span v-if="assignedCount(member.user_id)" class="badge badge-primary tabular-nums">
                {{ assignedCount(member.user_id) }}
              </span>
              <span v-else class="badge badge-warning whitespace-nowrap" :title="t('groupManagement.members.noAccountsHint')">
                <Icon name="exclamationTriangle" size="xs" />
                {{ t('groupManagement.members.noAccounts') }}
                <span class="sr-only">{{ t('groupManagement.members.noAccountsHint') }}</span>
              </span>
            </td>
            <td class="whitespace-nowrap text-right">
              <div class="inline-flex items-center gap-1">
                <button
                  v-if="manualMode"
                  type="button"
                  :class="[iconButton, defaultHover]"
                  :title="t('groupManagement.members.assign')"
                  :aria-label="`${t('groupManagement.members.assign')} · ${memberDisplayName(member)}`"
                  :disabled="busy"
                  @click="emit('assign', member)"
                >
                  <Icon name="link" size="sm" />
                </button>
                <button
                  type="button"
                  :class="[iconButton, defaultHover]"
                  :title="t('groupManagement.members.edit')"
                  :aria-label="`${t('groupManagement.members.edit')} · ${memberDisplayName(member)}`"
                  :disabled="busy"
                  @click="emit('edit', member)"
                >
                  <Icon name="edit" size="sm" />
                </button>
                <button
                  type="button"
                  :class="[iconButton, dangerHover]"
                  :title="t('groupManagement.members.remove')"
                  :aria-label="`${t('groupManagement.members.remove')} · ${memberDisplayName(member)}`"
                  :disabled="busy"
                  @click="emit('remove', member)"
                >
                  <Icon name="trash" size="sm" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { AllocationMode, GroupManagementAccount, GroupManagementMember } from '@/api/groupManagement'
import QuotaBar from './QuotaBar.vue'
import { initialOf, memberDisplayName, memberSubtitle } from './helpers'

const props = defineProps<{
  members: GroupManagementMember[]
  accounts: GroupManagementAccount[]
  mode: AllocationMode
  busy: boolean
}>()

const emit = defineEmits<{
  (e: 'add'): void
  (e: 'edit', member: GroupManagementMember): void
  (e: 'assign', member: GroupManagementMember): void
  (e: 'remove', member: GroupManagementMember): void
}>()

const { t } = useI18n()

const iconButton = 'inline-flex h-9 w-9 items-center justify-center rounded-lg text-gray-500 transition-colors duration-150 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-dark-300'
const defaultHover = 'hover:bg-gray-100 hover:text-primary-600 dark:hover:bg-dark-700 dark:hover:text-primary-400'
const dangerHover = 'hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-500/10 dark:hover:text-red-400'

const query = ref('')
const manualMode = computed(() => props.mode === 'manual')

const filteredMembers = computed(() => {
  const keyword = query.value.toLowerCase()
  if (!keyword) return props.members
  return props.members.filter(member =>
    [member.username, member.email, String(member.user_id)]
      .some(value => value?.toLowerCase().includes(keyword))
  )
})

const assignedCounts = computed(() => {
  const counts = new Map<number, number>()
  for (const account of props.accounts) {
    for (const userId of account.assigned_user_ids) {
      counts.set(userId, (counts.get(userId) ?? 0) + 1)
    }
  }
  return counts
})

function assignedCount(userId: number) {
  return assignedCounts.value.get(userId) ?? 0
}
</script>
