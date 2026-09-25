<template>
  <AppLayout>
    <div class="mx-auto max-w-[1600px] space-y-6">
      <!-- 页面引导：问候 + 统计时效 + 快捷入口 -->
      <div class="flex flex-wrap items-end justify-between gap-4">
        <div class="min-w-0">
          <h2 class="text-xl font-semibold tracking-tight text-gray-900 dark:text-white">{{ greetingText }}</h2>
          <p class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-gray-500 dark:text-dark-400">
            <span>{{ t('admin.dashboard.intro') }}</span>
            <template v-if="updatedAtText">
              <span aria-hidden="true">·</span>
              <span>{{ t('admin.dashboard.updatedAt', { time: updatedAtText }) }}</span>
            </template>
            <span v-if="stats?.stats_stale" class="badge badge-warning">
              <Icon name="exclamationTriangle" size="xs" :stroke-width="2" />
              {{ t('admin.dashboard.statsStale') }}
            </span>
          </p>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <button
            v-if="canUseBatchImage"
            type="button"
            class="btn btn-secondary btn-sm"
            :title="t('admin.dashboard.batchImageDesc')"
            @click="router.push('/batch-image')"
          >
            <Icon name="sparkles" size="sm" />
            {{ t('admin.dashboard.batchImage') }}
          </button>
          <button
            type="button"
            class="btn btn-secondary btn-sm"
            :title="t('admin.dashboard.groupPricingDesc')"
            @click="router.push('/admin/groups')"
          >
            <Icon name="grid" size="sm" />
            {{ t('admin.dashboard.groupPricing') }}
          </button>
        </div>
      </div>

      <!-- 核心指标 -->
      <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatTile
          :label="t('admin.dashboard.todayRequests')"
          icon="chart"
          :value="stats ? formatNumber(stats.today_requests) : '—'"
          :loading="loading && !stats"
          :trend="requestsTrend"
          :trend-label="t('admin.dashboard.kpi.trendHint')"
        >
          <template v-if="stats">{{ t('admin.dashboard.kpi.cumulative', { value: formatCompact(stats.total_requests) }) }}</template>
        </StatTile>
        <StatTile
          :label="t('admin.dashboard.todayTokens')"
          icon="cube"
          :value="stats ? formatCompact(stats.today_tokens) : '—'"
          :value-title="stats ? formatNumber(stats.today_tokens) : undefined"
          :loading="loading && !stats"
          :trend="tokensTrend"
          :trend-label="t('admin.dashboard.kpi.trendHint')"
        >
          <template v-if="stats">
            {{ t('admin.dashboard.kpi.tokensBreakdown', {
              input: formatCompact(stats.today_input_tokens),
              output: formatCompact(stats.today_output_tokens),
              cache: formatCompact((stats.today_cache_read_tokens || 0) + (stats.today_cache_creation_tokens || 0))
            }) }}
          </template>
        </StatTile>
        <StatTile
          :label="t('admin.dashboard.todayCost')"
          icon="dollar"
          :value="stats ? formatUSD(stats.today_actual_cost) : '—'"
          :loading="loading && !stats"
          :trend="costTrend"
          :trend-label="t('admin.dashboard.kpi.trendHint')"
        >
          <template v-if="stats">
            {{ t('admin.dashboard.kpi.accountCost') }} <span class="tabular-nums text-gray-700 dark:text-gray-300">{{ formatUSD(stats.today_account_cost) }}</span>
            <span class="mx-1" aria-hidden="true">·</span>
            {{ t('admin.dashboard.kpi.standardCost') }} <span class="tabular-nums text-gray-700 dark:text-gray-300">{{ formatUSD(stats.today_cost) }}</span>
          </template>
        </StatTile>
        <StatTile
          :label="t('admin.dashboard.kpi.activeUsersToday')"
          icon="users"
          :value="stats ? formatNumber(stats.active_users) : '—'"
          :loading="loading && !stats"
        >
          <template v-if="stats">
            {{ t('admin.dashboard.kpi.newUsers', { count: formatNumber(stats.today_new_users) }) }}
            <span class="mx-1" aria-hidden="true">·</span>
            {{ t('admin.dashboard.kpi.hourlyActive', { count: formatNumber(stats.hourly_active_users) }) }}
          </template>
          <template v-if="stats" #footer>
            <div class="flex items-baseline justify-between text-xs">
              <span class="text-gray-500 dark:text-dark-400">{{ t('admin.dashboard.kpi.totalUsers', { count: formatNumber(stats.total_users) }) }}</span>
              <span class="font-medium tabular-nums text-gray-700 dark:text-gray-200">{{ activeUserShare }}</span>
            </div>
            <div class="mt-1.5 h-1.5 overflow-hidden rounded-full bg-primary-100 dark:bg-primary-900/40" aria-hidden="true">
              <div class="h-full rounded-full bg-primary-600 dark:bg-primary-500" :style="{ width: activeUserShare }" />
            </div>
          </template>
        </StatTile>
      </div>

      <!-- 次要指标 -->
      <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatTile
          compact
          :label="t('admin.dashboard.apiKeys')"
          icon="key"
          :value="stats ? formatNumber(stats.active_api_keys) : '—'"
          :loading="loading && !stats"
        >
          <template v-if="stats">
            {{ t('admin.dashboard.kpi.apiKeysSummary', { active: formatNumber(stats.active_api_keys), total: formatNumber(stats.total_api_keys) }) }}
          </template>
        </StatTile>
        <StatTile
          compact
          :label="t('admin.dashboard.health.title')"
          icon="server"
          :value="stats ? formatNumber(stats.normal_accounts) : '—'"
          :unit="stats ? `/ ${formatNumber(stats.total_accounts)}` : undefined"
          :loading="loading && !stats"
        >
          <template v-if="stats" #footer>
            <AccountHealthMeter
              :total="stats.total_accounts"
              :normal="stats.normal_accounts"
              :ratelimit="stats.ratelimit_accounts"
              :overload="stats.overload_accounts"
              :error="stats.error_accounts"
            />
          </template>
        </StatTile>
        <StatTile
          compact
          :label="t('admin.dashboard.kpi.throughput')"
          icon="bolt"
          :value="stats ? formatCompact(stats.rpm) : '—'"
          unit="RPM"
          :loading="loading && !stats"
        >
          <template v-if="stats">
            <span class="tabular-nums text-gray-700 dark:text-gray-300">{{ formatCompact(stats.tpm) }}</span> TPM
            <span class="mx-1" aria-hidden="true">·</span>
            {{ t('admin.dashboard.kpi.throughputHint') }}
          </template>
        </StatTile>
        <StatTile
          compact
          :label="t('admin.dashboard.avgResponse')"
          icon="clock"
          :value="stats ? formatDurationMs(stats.average_duration_ms) : '—'"
          :loading="loading && !stats"
        >
          <template v-if="stats && uptimeText">{{ t('admin.dashboard.kpi.uptime', { value: uptimeText }) }}</template>
        </StatTile>
      </div>

      <!-- 用量分析：筛选放在它所控制的图表正上方 -->
      <section class="space-y-4" :aria-label="t('admin.dashboard.analytics.title')">
        <div class="flex flex-wrap items-end justify-between gap-3">
          <div>
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.dashboard.analytics.title') }}</h2>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.dashboard.analytics.subtitle') }}</p>
          </div>
          <div class="flex flex-wrap items-center gap-2">
            <DateRangePicker
              v-model:start-date="startDate"
              v-model:end-date="endDate"
              @change="onDateRangeChange"
            />
            <SegmentedControl
              v-model="granularity"
              :options="granularityOptions"
              :aria-label="t('admin.dashboard.granularity')"
              @change="loadChartData"
            />
            <button
              type="button"
              class="icon-btn border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900"
              :disabled="chartsLoading"
              :title="t('common.refresh')"
              :aria-label="t('common.refresh')"
              @click="loadDashboardStats"
            >
              <Icon name="refresh" size="sm" :class="chartsLoading ? 'animate-spin' : ''" />
            </button>
          </div>
        </div>

        <TokenUsageTrend :trend-data="trendData" :loading="chartsLoading" />

        <div class="grid grid-cols-1 gap-4 xl:grid-cols-2">
          <ModelDistributionChart
            :model-stats="modelStats"
            :enable-ranking-view="true"
            :ranking-items="rankingItems"
            :ranking-total-actual-cost="rankingTotalActualCost"
            :ranking-total-requests="rankingTotalRequests"
            :ranking-total-tokens="rankingTotalTokens"
            :loading="chartsLoading"
            :ranking-loading="rankingLoading"
            :ranking-error="rankingError"
            :start-date="startDate"
            :end-date="endDate"
            @ranking-click="goToUserUsage"
          />
          <TopUsersCard :trend="userTrend" :loading="userTrendLoading" @select="goToUserUsageById">
            <template #actions>
              <router-link
                to="/admin/usage"
                class="inline-flex items-center gap-1 text-xs font-medium text-primary-700 hover:text-primary-800 dark:text-primary-300 dark:hover:text-primary-200"
              >
                {{ t('admin.dashboard.topUsers.viewAll') }}
                <Icon name="arrowRight" size="xs" class="h-3.5 w-3.5" :stroke-width="2" />
              </router-link>
            </template>
          </TopUsersCard>
        </div>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'

const { t } = useI18n()
import { adminAPI } from '@/api/admin'
import type {
  DashboardStats,
  TrendDataPoint,
  ModelStat,
  UserUsageTrendPoint,
  UserSpendingRankingItem
} from '@/types'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import StatTile from '@/components/common/StatTile.vue'
import ModelDistributionChart from '@/components/charts/ModelDistributionChart.vue'
import TokenUsageTrend from '@/components/charts/TokenUsageTrend.vue'
import AccountHealthMeter from '@/components/admin/dashboard/AccountHealthMeter.vue'
import TopUsersCard from '@/components/admin/dashboard/TopUsersCard.vue'
import { formatCompact, formatDurationMs, formatUSD } from '@/components/charts/chartTheme'
import { useBatchImageAccess } from '@/composables/useBatchImageAccess'

const appStore = useAppStore()
const authStore = useAuthStore()
const router = useRouter()
const { canUseBatchImage, refreshBatchImageAccess } = useBatchImageAccess()
const stats = ref<DashboardStats | null>(null)
const loading = ref(false)
const chartsLoading = ref(false)
const userTrendLoading = ref(false)
const rankingLoading = ref(false)
const rankingError = ref(false)

// Chart data
const trendData = ref<TrendDataPoint[]>([])
const modelStats = ref<ModelStat[]>([])
const userTrend = ref<UserUsageTrendPoint[]>([])
const rankingItems = ref<UserSpendingRankingItem[]>([])
const rankingTotalActualCost = ref(0)
const rankingTotalRequests = ref(0)
const rankingTotalTokens = ref(0)
let chartLoadSeq = 0
let usersTrendLoadSeq = 0
let rankingLoadSeq = 0
const rankingLimit = 12

// Helper function to format date in local timezone
const formatLocalDate = (date: Date): string => {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}

const getLast24HoursRangeDates = (): { start: string; end: string } => {
  const end = new Date()
  const start = new Date(end.getTime() - 24 * 60 * 60 * 1000)
  return {
    start: formatLocalDate(start),
    end: formatLocalDate(end)
  }
}

// Date range
const granularity = ref<'day' | 'hour'>('hour')
const defaultRange = getLast24HoursRangeDates()
const startDate = ref(defaultRange.start)
const endDate = ref(defaultRange.end)

const granularityOptions = computed(() => [
  { value: 'hour' as const, label: t('admin.dashboard.hour') },
  { value: 'day' as const, label: t('admin.dashboard.day') }
])

// ==================== 引导区 ====================
const greetingText = computed(() => {
  const hour = new Date().getHours()
  const greeting =
    hour >= 5 && hour < 11
      ? t('admin.dashboard.greeting.morning')
      : hour >= 11 && hour < 18
        ? t('admin.dashboard.greeting.afternoon')
        : t('admin.dashboard.greeting.evening')
  const user = authStore.user
  const name = user?.username || user?.email?.split('@')[0] || ''
  return name ? t('admin.dashboard.greeting.withName', { greeting, name }) : greeting
})

const updatedAtText = computed(() => {
  const raw = stats.value?.stats_updated_at
  if (!raw) return ''
  const date = new Date(raw)
  if (Number.isNaN(date.getTime())) return ''
  return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
})

// ==================== 指标卡 ====================
const requestsTrend = computed(() => trendData.value.map((p) => p.requests))
const tokensTrend = computed(() => trendData.value.map((p) => p.total_tokens))
const costTrend = computed(() => trendData.value.map((p) => p.actual_cost))

const activeUserShare = computed(() => {
  const total = stats.value?.total_users ?? 0
  if (!total) return '0%'
  const share = Math.min(100, ((stats.value?.active_users ?? 0) / total) * 100)
  return `${share.toFixed(share >= 10 ? 0 : 1)}%`
})

const uptimeText = computed(() => {
  const seconds = Number(stats.value?.uptime ?? 0)
  if (!Number.isFinite(seconds) || seconds <= 0) return ''
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  if (days > 0) return t('admin.dashboard.kpi.uptimeDays', { days, hours })
  if (hours > 0) return t('admin.dashboard.kpi.uptimeHours', { hours, minutes })
  return t('admin.dashboard.kpi.uptimeMinutes', { minutes })
})

const toFiniteNumber = (value: unknown): number => {
  const numberValue = Number(value)
  return Number.isFinite(numberValue) ? numberValue : 0
}

const formatNumber = (value: number | null | undefined): string => {
  return toFiniteNumber(value).toLocaleString()
}

const goToUserUsageById = (userId: number) => {
  void router.push({
    path: '/admin/usage',
    query: {
      user_id: String(userId),
      start_date: startDate.value,
      end_date: endDate.value
    }
  })
}

const goToUserUsage = (item: UserSpendingRankingItem) => goToUserUsageById(item.user_id)

// Date range change handler
const onDateRangeChange = (range: {
  startDate: string
  endDate: string
  preset: string | null
}) => {
  // Auto-select granularity based on date range
  const start = new Date(range.startDate)
  const end = new Date(range.endDate)
  const daysDiff = Math.ceil((end.getTime() - start.getTime()) / (1000 * 60 * 60 * 24))

  // If range is 1 day, use hourly granularity
  if (daysDiff <= 1) {
    granularity.value = 'hour'
  } else {
    granularity.value = 'day'
  }

  loadChartData()
}

// Load data
const loadDashboardSnapshot = async (includeStats: boolean) => {
  const currentSeq = ++chartLoadSeq
  if (includeStats && !stats.value) {
    loading.value = true
  }
  chartsLoading.value = true
  try {
    const response = await adminAPI.dashboard.getSnapshotV2({
      start_date: startDate.value,
      end_date: endDate.value,
      granularity: granularity.value,
      include_stats: includeStats,
      include_trend: true,
      include_model_stats: true,
      include_group_stats: false,
      include_users_trend: false
    })
    if (currentSeq !== chartLoadSeq) return
    if (includeStats && response.stats) {
      stats.value = response.stats
    }
    trendData.value = response.trend || []
    modelStats.value = response.models || []
  } catch (error) {
    if (currentSeq !== chartLoadSeq) return
    appStore.showError(t('admin.dashboard.failedToLoad'))
    console.error('Error loading dashboard snapshot:', error)
  } finally {
    if (currentSeq === chartLoadSeq) {
      loading.value = false
      chartsLoading.value = false
    }
  }
}

const loadUsersTrend = async () => {
  const currentSeq = ++usersTrendLoadSeq
  userTrendLoading.value = true
  try {
    const response = await adminAPI.dashboard.getUserUsageTrend({
      start_date: startDate.value,
      end_date: endDate.value,
      granularity: granularity.value,
      limit: 12
    })
    if (currentSeq !== usersTrendLoadSeq) return
    userTrend.value = response.trend || []
  } catch (error) {
    if (currentSeq !== usersTrendLoadSeq) return
    console.error('Error loading users trend:', error)
    userTrend.value = []
  } finally {
    if (currentSeq === usersTrendLoadSeq) {
      userTrendLoading.value = false
    }
  }
}

const loadUserSpendingRanking = async () => {
  const currentSeq = ++rankingLoadSeq
  rankingLoading.value = true
  rankingError.value = false
  try {
    const response = await adminAPI.dashboard.getUserSpendingRanking({
      start_date: startDate.value,
      end_date: endDate.value,
      limit: rankingLimit
    })
    if (currentSeq !== rankingLoadSeq) return
    rankingItems.value = response.ranking || []
    rankingTotalActualCost.value = response.total_actual_cost || 0
    rankingTotalRequests.value = response.total_requests || 0
    rankingTotalTokens.value = response.total_tokens || 0
  } catch (error) {
    if (currentSeq !== rankingLoadSeq) return
    console.error('Error loading user spending ranking:', error)
    rankingItems.value = []
    rankingTotalActualCost.value = 0
    rankingTotalRequests.value = 0
    rankingTotalTokens.value = 0
    rankingError.value = true
  } finally {
    if (currentSeq === rankingLoadSeq) {
      rankingLoading.value = false
    }
  }
}

const loadDashboardStats = async () => {
  await Promise.all([
    loadDashboardSnapshot(true),
    loadUsersTrend(),
    loadUserSpendingRanking()
  ])
}

const loadChartData = async () => {
  await Promise.all([
    loadDashboardSnapshot(false),
    loadUsersTrend(),
    loadUserSpendingRanking()
  ])
}

onMounted(() => {
  void refreshBatchImageAccess()
  loadDashboardStats()
})
</script>
