<template>
  <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
    <StatTile
      :label="t('usage.totalRequests')"
      icon="document"
      :value="formatNumber(stats?.total_requests)"
      :loading="showSkeleton"
      :trend="requestsTrend"
      :trend-label="t('usage.inSelectedRange')"
    >
      {{ t('usage.inSelectedRange') }}
    </StatTile>

    <StatTile
      :label="t('usage.totalTokens')"
      icon="cube"
      :value="formatTokens(stats?.total_tokens || 0)"
      :value-title="formatNumber(stats?.total_tokens)"
      :loading="showSkeleton"
      :trend="tokensTrend"
      :trend-label="t('usage.inSelectedRange')"
    >
      <span class="flex flex-wrap items-center gap-x-1">
        <span>{{ t('usage.in') }}: <span class="tabular-nums text-gray-700 dark:text-gray-300">{{ formatCompact(stats?.total_input_tokens || 0) }}</span></span>
        <span aria-hidden="true">/</span>
        <span>{{ t('usage.out') }}: <span class="tabular-nums text-gray-700 dark:text-gray-300">{{ formatCompact(stats?.total_output_tokens || 0) }}</span></span>
        <span aria-hidden="true">/</span>
        <span class="group relative inline-flex cursor-help items-center gap-0.5 rounded focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/50" tabindex="0">
          <span>{{ cacheLabel() }}: {{ formatCompact(stats?.total_cache_tokens || 0) }}</span>
          <svg
            class="h-3.5 w-3.5 text-gray-400"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
            aria-hidden="true"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
            />
          </svg>
          <!-- 用 hidden 而非 opacity-0：隐藏时不占布局，避免窄屏出现横向滚动 -->
          <span
            class="pointer-events-none absolute left-1/2 top-full z-30 mt-2 hidden w-60 -translate-x-1/2 rounded-lg border border-gray-200 bg-white p-3 text-left text-xs text-gray-700 shadow-lg group-hover:block group-focus:block dark:border-dark-600 dark:bg-dark-800 dark:text-dark-200"
          >
            <span class="mb-2 block font-medium text-gray-900 dark:text-white">
              {{ cacheDetailLabel() }}
            </span>
            <span class="flex items-center justify-between gap-3">
              <span>{{ t('usage.cacheCreationTokensLabel') }}</span>
              <span class="tabular-nums">
                {{ formatTokens(stats?.total_cache_creation_tokens || 0) }}
              </span>
            </span>
            <span class="mt-1 flex items-center justify-between gap-3">
              <span>{{ t('usage.cacheReadTokensLabel') }}</span>
              <span class="tabular-nums">
                {{ formatTokens(stats?.total_cache_read_tokens || 0) }}
              </span>
            </span>
            <span class="mt-2 flex items-center justify-between gap-3 border-t border-gray-100 pt-2 dark:border-dark-700">
              <span>{{ t('admin.dashboard.trend.cacheHitRate') }}</span>
              <span class="font-medium tabular-nums text-gray-900 dark:text-white">{{ cacheHitRateText }}</span>
            </span>
          </span>
        </span>
      </span>
    </StatTile>

    <StatTile
      :label="t('usage.totalCost')"
      icon="dollar"
      :value="formatUSD(stats?.total_actual_cost || 0)"
      :loading="showSkeleton"
      :trend="costTrend"
      :trend-label="t('usage.inSelectedRange')"
    >
      <template v-if="showAccountCost && totalAccountCost != null">
        {{ t('usage.accountCost') }}
        <span class="tabular-nums text-gray-700 dark:text-gray-300">{{ formatUSD(totalAccountCost) }}</span>
        <span class="mx-1" aria-hidden="true">·</span>
      </template>
      {{ t('usage.standardCost') }}
      <span class="tabular-nums text-gray-700 dark:text-gray-300" :class="{ 'line-through': strikeStandardCost }">{{ formatUSD(stats?.total_cost || 0) }}</span>
    </StatTile>

    <StatTile
      :label="t('usage.avgDuration')"
      icon="clock"
      :value="formatDuration(stats?.average_duration_ms || 0)"
      :loading="showSkeleton"
    >
      {{ t('usage.inSelectedRange') }}
    </StatTile>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AdminUsageStatsResponse } from '@/api/admin/usage'
import type { TrendDataPoint, UsageStatsResponse } from '@/types'
import StatTile from '@/components/common/StatTile.vue'
import { formatCompact, formatUSD } from '@/components/charts/chartTheme'

const props = withDefaults(defineProps<{
  stats: (AdminUsageStatsResponse | UsageStatsResponse) | null
  showAccountCost?: boolean
  strikeStandardCost?: boolean
  /** 可选：所选时间范围的趋势点，用于卡片底部的迷你趋势线 */
  trend?: TrendDataPoint[]
  /** 首次加载（尚无数据）时显示骨架 */
  loading?: boolean
}>(), {
  showAccountCost: true,
  strikeStandardCost: false,
  trend: () => [],
  loading: false
})

const { t } = useI18n()

const totalAccountCost = computed(() => {
  const stats = props.stats as (AdminUsageStatsResponse & { total_account_cost?: number }) | null
  return stats?.total_account_cost ?? null
})
const showAccountCost = computed(() => props.showAccountCost)
const strikeStandardCost = computed(() => props.strikeStandardCost)
const showSkeleton = computed(() => props.loading && !props.stats)

const requestsTrend = computed(() => props.trend.map((p) => p.requests))
const tokensTrend = computed(() => props.trend.map((p) => p.total_tokens))
const costTrend = computed(() => props.trend.map((p) => p.actual_cost))

// 命中率 = 缓存读取 / 全部提示 token（输入 + 缓存读取 + 缓存写入）
const cacheHitRateText = computed(() => {
  const s = props.stats
  if (!s) return '0.0%'
  const read = Number(s.total_cache_read_tokens) || 0
  const prompt = (Number(s.total_input_tokens) || 0) + read + (Number(s.total_cache_creation_tokens) || 0)
  return prompt > 0 ? `${((read / prompt) * 100).toFixed(1)}%` : '0.0%'
})

const formatDuration = (ms: number) =>
  ms < 1000 ? `${ms.toFixed(0)}ms` : `${(ms / 1000).toFixed(2)}s`

const formatNumber = (value: number | null | undefined) => (Number(value) || 0).toLocaleString()

const formatTokens = (value: number) => {
  if (value >= 1e9) return (value / 1e9).toFixed(2) + 'B'
  if (value >= 1e6) return (value / 1e6).toFixed(2) + 'M'
  if (value >= 1e3) return (value / 1e3).toFixed(2) + 'K'
  return value.toLocaleString()
}

const cacheLabel = () => t('usage.cacheTotal')
const cacheDetailLabel = () => t('usage.cacheBreakdown')
</script>
