<template>
  <section class="card flex h-full flex-col">
    <header class="card-section-header">
      <div class="min-w-0">
        <h3 class="card-section-title">{{ t('dashboard.recentUsage') }}</h3>
        <p class="card-section-subtitle">{{ t('dashboard.recentUsageSubtitle') }}</p>
      </div>
      <router-link
        to="/usage"
        class="inline-flex items-center gap-1 text-xs font-medium text-primary-700 hover:text-primary-800 dark:text-primary-300 dark:hover:text-primary-200"
      >
        {{ t('dashboard.viewAllUsage') }}
        <Icon name="arrowRight" size="xs" class="h-3.5 w-3.5" :stroke-width="2" />
      </router-link>
    </header>

    <div class="flex-1 pb-2">
      <!-- 首次加载骨架；刷新时保留旧列表并降低不透明度 -->
      <ul v-if="loading && data.length === 0" class="space-y-1 px-5 py-2" aria-hidden="true">
        <li v-for="i in 5" :key="i" class="flex items-center gap-3 py-2">
          <div class="skeleton h-8 w-8 rounded-lg" />
          <div class="flex-1 space-y-1.5">
            <div class="skeleton h-3.5 w-40 rounded" />
            <div class="skeleton h-3 w-28 rounded" />
          </div>
          <div class="skeleton h-3.5 w-16 rounded" />
        </li>
      </ul>

      <div v-else-if="data.length === 0" class="px-5 py-6">
        <EmptyState :title="t('dashboard.noUsageRecords')" :description="t('dashboard.startUsingApi')" />
      </div>

      <ul v-else class="transition-opacity" :class="loading ? 'opacity-50' : ''">
        <li
          v-for="log in data"
          :key="log.id"
          class="flex items-center gap-3 border-t border-gray-100 px-5 py-2.5 first:border-t-0 dark:border-dark-800"
        >
          <span
            class="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-gray-50 ring-1 ring-inset ring-gray-200 dark:bg-dark-800 dark:ring-dark-700"
            aria-hidden="true"
          >
            <ModelIcon :model="log.model" size="18px" />
          </span>
          <div class="min-w-0 flex-1">
            <p class="truncate text-sm font-medium text-gray-900 dark:text-white" :title="log.model">{{ log.model }}</p>
            <p class="truncate text-xs text-gray-500 dark:text-dark-400">
              <span :title="formatDateTime(log.created_at)">{{ formatRelativeTime(log.created_at) }}</span>
              <span class="mx-1" aria-hidden="true">·</span>
              <span class="tabular-nums">{{ (log.input_tokens + log.output_tokens).toLocaleString() }}</span> tokens
            </p>
          </div>
          <div class="shrink-0 text-right">
            <p class="text-sm font-semibold tabular-nums text-gray-900 dark:text-white" :title="t('dashboard.actual')">
              {{ formatRequestCost(log.actual_cost) }}
            </p>
            <p class="text-xs tabular-nums text-gray-500 dark:text-dark-400" :title="t('dashboard.standard')">
              {{ t('dashboard.standard') }} {{ formatRequestCost(log.total_cost) }}
            </p>
          </div>
        </li>
      </ul>
    </div>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import EmptyState from '@/components/common/EmptyState.vue'
import ModelIcon from '@/components/common/ModelIcon.vue'
import Icon from '@/components/icons/Icon.vue'
import { formatDateTime, formatRelativeTime } from '@/utils/format'
import { formatUSD } from '@/components/charts/chartTheme'
import type { UsageLog } from '@/types'

defineProps<{
  data: UsageLog[]
  loading: boolean
}>()
const { t } = useI18n()

// 单次请求金额通常很小：不足 $1 时保留 4 位小数，避免被四舍五入成相同的数
const formatRequestCost = (value: number) => {
  const v = Number(value) || 0
  return Math.abs(v) >= 1 ? formatUSD(v) : `$${v.toFixed(4)}`
}
</script>
