<template>
  <section class="card overflow-hidden">
    <header class="flex flex-wrap items-center gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
      <p class="flex min-w-0 flex-1 items-start gap-2 text-sm text-gray-600 dark:text-dark-300">
        <Icon :name="manualMode ? 'link' : 'sync'" size="sm" class="mt-0.5 shrink-0 text-primary-500" />
        <span>{{ manualMode ? t('groupManagement.accounts.manualHint') : t('groupManagement.accounts.autoHint') }}</span>
      </p>
      <button
        v-if="manualMode"
        type="button"
        class="btn btn-primary btn-md"
        :disabled="busy || !members.length || !accounts.length"
        @click="emit('assign')"
      >
        <Icon name="link" size="sm" />
        {{ t('groupManagement.accounts.assign') }}
      </button>
    </header>

    <div v-if="!accounts.length" class="empty-state">
      <div class="mb-4 flex h-14 w-14 items-center justify-center rounded-2xl bg-gray-100 dark:bg-dark-700">
        <Icon name="server" size="lg" class="text-gray-400 dark:text-dark-400" />
      </div>
      <h3 class="empty-state-title text-base">{{ t('groupManagement.accounts.empty') }}</h3>
      <p class="empty-state-description">{{ t('groupManagement.accounts.emptyHint') }}</p>
    </div>

    <div v-else class="overflow-x-auto">
      <table class="table min-w-[640px]">
        <thead>
          <tr class="whitespace-nowrap">
            <th scope="col">{{ t('groupManagement.accounts.account') }}</th>
            <th scope="col">{{ t('groupManagement.accounts.platform') }}</th>
            <th scope="col">{{ t('groupManagement.accounts.status') }}</th>
            <th scope="col">{{ t('groupManagement.accounts.assignedTo') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="account in accounts" :key="account.id">
            <td>
              <div class="font-medium text-gray-900 dark:text-white">{{ account.name }}</div>
              <div class="text-xs text-gray-500 dark:text-dark-400">#{{ account.id }}</div>
            </td>
            <td><PlatformChip :platform="account.platform" /></td>
            <td><AccountStatus :status="account.status" /></td>
            <td>
              <span v-if="!manualMode" class="text-sm text-gray-500 dark:text-dark-400">
                {{ t('groupManagement.accounts.allMembers') }}
              </span>
              <span v-else-if="!account.assigned_user_ids.length" class="text-sm text-gray-400 dark:text-dark-500">
                {{ t('groupManagement.accounts.unassigned') }}
              </span>
              <div v-else class="flex flex-wrap items-center gap-1.5">
                <span
                  v-for="userId in account.assigned_user_ids.slice(0, VISIBLE_ASSIGNEES)"
                  :key="userId"
                  class="inline-flex max-w-[10rem] items-center rounded-full bg-primary-50 px-2 py-0.5 text-xs font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
                  :title="assigneeName(userId)"
                >
                  <span class="truncate">{{ assigneeName(userId) }}</span>
                </span>
                <span
                  v-if="account.assigned_user_ids.length > VISIBLE_ASSIGNEES"
                  class="badge badge-gray tabular-nums"
                  :title="account.assigned_user_ids.slice(VISIBLE_ASSIGNEES).map(assigneeName).join(', ')"
                >
                  {{ t('groupManagement.accounts.more', { count: account.assigned_user_ids.length - VISIBLE_ASSIGNEES }) }}
                </span>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { AllocationMode, GroupManagementAccount, GroupManagementMember } from '@/api/groupManagement'
import AccountStatus from './AccountStatus.vue'
import PlatformChip from './PlatformChip.vue'
import { memberDisplayName } from './helpers'

const props = defineProps<{
  accounts: GroupManagementAccount[]
  members: GroupManagementMember[]
  mode: AllocationMode
  busy: boolean
}>()

const emit = defineEmits<{
  (e: 'assign'): void
}>()

const { t } = useI18n()

const VISIBLE_ASSIGNEES = 3
const manualMode = computed(() => props.mode === 'manual')
const memberById = computed(() => new Map(props.members.map(member => [member.user_id, member])))

function assigneeName(userId: number) {
  const member = memberById.value.get(userId)
  return member ? memberDisplayName(member) : `#${userId}`
}
</script>
