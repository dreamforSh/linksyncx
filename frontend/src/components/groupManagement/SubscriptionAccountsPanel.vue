<template>
  <section class="card overflow-hidden">
    <header class="flex flex-wrap items-center gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
      <p class="flex min-w-0 flex-1 items-start gap-2 text-sm text-gray-600 dark:text-dark-300">
        <Icon name="link" size="sm" class="mt-0.5 shrink-0 text-primary-500" />
        <span>{{ t('groupManagement.accountUsage.managerHint') }}</span>
      </p>
      <div class="flex flex-wrap gap-2">
        <button
          type="button"
          class="btn btn-secondary btn-md"
          :disabled="loading"
          :title="t('groupManagement.accountUsage.reload')"
          @click="emit('reload')"
        >
          <Icon name="refresh" size="sm" :class="loading && 'animate-spin motion-reduce:animate-none'" />
          {{ t('groupManagement.accountUsage.reload') }}
        </button>
        <button
          type="button"
          class="btn btn-primary btn-md"
          :disabled="busy || !members.length || !accounts.length"
          @click="emit('assign')"
        >
          <Icon name="link" size="sm" />
          {{ t('groupManagement.accounts.assign') }}
        </button>
      </div>
    </header>

    <div v-if="loading && !usage.length" class="grid gap-4 p-5 lg:grid-cols-2" role="status" :aria-label="t('common.loading')">
      <div v-for="index in 2" :key="index" class="skeleton h-40 rounded-xl" />
    </div>

    <div v-else-if="!usage.length" class="empty-state">
      <div class="mb-4 flex h-14 w-14 items-center justify-center rounded-2xl bg-gray-100 dark:bg-dark-700">
        <Icon name="server" size="lg" class="text-gray-400 dark:text-dark-400" />
      </div>
      <h3 class="empty-state-title text-base">{{ t('groupManagement.accounts.empty') }}</h3>
      <p class="empty-state-description">{{ t('groupManagement.accounts.emptyHint') }}</p>
    </div>

    <div v-else class="grid gap-4 p-5 lg:grid-cols-2">
      <AccountUsageCard v-for="item in usage" :key="item.account_id" :usage="item">
        <template v-if="item.supports_reset_credit" #actions>
          <div class="flex shrink-0 items-center gap-1">
            <button
              type="button"
              class="inline-flex h-9 w-9 items-center justify-center rounded-lg text-gray-500 transition-colors duration-150 hover:bg-gray-100 hover:text-primary-600 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-dark-300 dark:hover:bg-dark-700 dark:hover:text-primary-400"
              :title="t('groupManagement.accountUsage.refreshQuota')"
              :aria-label="`${t('groupManagement.accountUsage.refreshQuota')} · ${item.name}`"
              :disabled="busy || actionAccountId !== null"
              @click="emit('refresh-quota', item)"
            >
              <Icon
                name="sync"
                size="sm"
                :class="actionAccountId === item.account_id && 'animate-spin motion-reduce:animate-none'"
              />
            </button>
            <button
              v-if="item.can_reset"
              type="button"
              class="btn btn-secondary btn-sm"
              :disabled="busy || actionAccountId !== null || !canUseCredit(item)"
              :title="creditTitle(item)"
              @click="emit('reset-credit', item)"
            >
              <Icon name="creditCard" size="sm" />
              {{ t('groupManagement.accountUsage.useCredit') }}
            </button>
          </div>
        </template>
        <template #footer>
          <div class="mt-3 flex flex-wrap items-center gap-1.5 border-t border-gray-100 pt-3 text-xs dark:border-dark-700">
            <span class="text-gray-500 dark:text-dark-400">{{ t('groupManagement.accounts.assignedTo') }}</span>
            <span v-if="!assignees(item.account_id).length" class="text-gray-400 dark:text-dark-500">
              {{ t('groupManagement.accounts.unassigned') }}
            </span>
            <template v-else>
              <span
                v-for="name in assignees(item.account_id).slice(0, VISIBLE_ASSIGNEES)"
                :key="name"
                class="inline-flex max-w-[10rem] items-center rounded-full bg-primary-50 px-2 py-0.5 font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
                :title="name"
              >
                <span class="truncate">{{ name }}</span>
              </span>
              <span
                v-if="assignees(item.account_id).length > VISIBLE_ASSIGNEES"
                class="badge badge-gray tabular-nums"
                :title="assignees(item.account_id).slice(VISIBLE_ASSIGNEES).join(', ')"
              >
                {{ t('groupManagement.accounts.more', { count: assignees(item.account_id).length - VISIBLE_ASSIGNEES }) }}
              </span>
            </template>
          </div>
        </template>
      </AccountUsageCard>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { GroupAccountUsage, GroupManagementAccount, GroupManagementMember } from '@/api/groupManagement'
import AccountUsageCard from './AccountUsageCard.vue'
import { memberDisplayName } from './helpers'

const props = withDefaults(defineProps<{
  usage: GroupAccountUsage[]
  accounts: GroupManagementAccount[]
  members: GroupManagementMember[]
  loading: boolean
  busy: boolean
  // 正在刷新额度 / 使用重置卡的账号
  actionAccountId?: number | null
}>(), {
  actionAccountId: null
})

const emit = defineEmits<{
  (e: 'assign'): void
  (e: 'reload'): void
  (e: 'refresh-quota', account: GroupAccountUsage): void
  (e: 'reset-credit', account: GroupAccountUsage): void
}>()

const { t } = useI18n()

const VISIBLE_ASSIGNEES = 3
const memberById = computed(() => new Map(props.members.map(member => [member.user_id, member])))
const assigneesByAccount = computed(() => new Map(props.accounts.map(account => [
  account.id,
  account.assigned_user_ids.map(userId => {
    const member = memberById.value.get(userId)
    return member ? memberDisplayName(member) : t('groupManagement.members.unknownMember')
  })
])))

function assignees(accountId: number) {
  return assigneesByAccount.value.get(accountId) ?? []
}

// 与超管账号页一致：卡数未知（快照过期或从未查询）时先刷新额度，确认有卡再使用
function canUseCredit(item: GroupAccountUsage) {
  return (item.reset_credits?.available_count ?? 0) > 0
}

function creditTitle(item: GroupAccountUsage) {
  if (canUseCredit(item)) return t('groupManagement.accountUsage.useCredit')
  return item.reset_credits ? t('groupManagement.accountUsage.noCredit') : t('groupManagement.accountUsage.refreshFirst')
}
</script>
