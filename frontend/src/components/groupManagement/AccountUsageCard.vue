<template>
  <article
    class="flex min-w-0 flex-col rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-600 dark:bg-dark-800/40"
    :data-account="usage.account_id"
  >
    <header class="flex items-start gap-3">
      <div class="min-w-0 flex-1">
        <p class="truncate text-sm font-semibold text-gray-900 dark:text-white" :title="usage.name">{{ usage.name }}</p>
        <div class="mt-1.5 flex flex-wrap items-center gap-1.5">
          <PlatformChip :platform="usage.platform" />
          <AccountStatus :status="usage.status" />
          <span v-if="usage.rate_limited" class="badge badge-warning whitespace-nowrap">
            <Icon name="clock" size="xs" />
            {{ rateLimitLabel }}
          </span>
        </div>
      </div>
      <slot name="actions" />
    </header>

    <div class="mt-4 grid gap-3 sm:grid-cols-2">
      <UtilizationBar :label="t('groupManagement.accountUsage.fiveHour')" :window="usage.five_hour" />
      <UtilizationBar :label="t('groupManagement.accountUsage.sevenDay')" :window="usage.seven_day" />
    </div>
    <p v-if="usage.usage_unavailable" class="mt-2 text-xs text-gray-500 dark:text-dark-400">
      {{ t('groupManagement.accountUsage.unavailable') }}
    </p>

    <div
      v-if="usage.supports_reset_credit"
      class="mt-3 flex flex-wrap items-center gap-x-2 gap-y-1 rounded-lg bg-gray-50 px-3 py-2 text-xs text-gray-600 dark:bg-dark-900/40 dark:text-dark-300"
      data-testid="account-reset-credits"
    >
      <Icon name="creditCard" size="sm" class="shrink-0 text-primary-500" />
      <template v-if="usage.reset_credits">
        <span class="font-medium text-gray-900 dark:text-white">
          {{ t('groupManagement.accountUsage.resetCredits', { count: usage.reset_credits.available_count }) }}
        </span>
        <span v-if="nearestExpiry">{{ t('groupManagement.accountUsage.nearestExpiry', { date: nearestExpiry }) }}</span>
      </template>
      <span v-else>
        {{ usage.can_reset ? t('groupManagement.accountUsage.resetCreditsUnknown') : t('groupManagement.accountUsage.resetCreditsPending') }}
      </span>
    </div>

    <slot name="footer" />
  </article>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { GroupAccountUsage } from '@/api/groupManagement'
import { formatCountdown, formatDateTimeToMinute } from '@/utils/format'
import AccountStatus from './AccountStatus.vue'
import PlatformChip from './PlatformChip.vue'
import UtilizationBar from './UtilizationBar.vue'

const props = defineProps<{
  usage: GroupAccountUsage
}>()

const { t } = useI18n()

const rateLimitLabel = computed(() => {
  const countdown = formatCountdown(props.usage.rate_limit_reset_at)
  return countdown
    ? t('groupManagement.accountUsage.rateLimitedFor', { time: countdown })
    : t('groupManagement.accountUsage.rateLimited')
})

// 最早到期的一张卡
const nearestExpiry = computed(() => {
  const dates = (props.usage.reset_credits?.expires_at ?? [])
    .map(value => ({ value, time: new Date(value).getTime() }))
    .filter(item => !Number.isNaN(item.time))
    .sort((a, b) => a.time - b.time)
  return dates.length ? formatDateTimeToMinute(dates[0].value) : ''
})
</script>
