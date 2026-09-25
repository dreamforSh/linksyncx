<template>
  <AppLayout>
    <div class="mx-auto max-w-[1600px] space-y-6">
      <!-- 页面引导：问候 + 快捷操作 -->
      <div class="flex flex-wrap items-end justify-between gap-4">
        <div class="min-w-0">
          <h2 class="text-xl font-semibold tracking-tight text-gray-900 dark:text-white">{{ greetingText }}</h2>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('dashboard.intro') }}</p>
        </div>
        <UserDashboardQuickActions />
      </div>

      <UserDashboardStats
        :stats="stats"
        :balance="user?.balance || 0"
        :is-simple="authStore.isSimpleMode"
        :platform-quotas="platformQuotas"
        :trend="trendData"
        :loading="loading && !stats"
        :balance-action="balanceAction"
      />

      <UserDashboardCharts
        v-model:startDate="startDate"
        v-model:endDate="endDate"
        v-model:granularity="granularity"
        :loading="loadingCharts"
        :trend="trendData"
        :models="modelStats"
        @dateRangeChange="onDateRangeChange"
        @granularityChange="loadCharts"
        @refresh="refreshAll"
      >
        <UserDashboardRecentUsage :data="recentUsage" :loading="loadingUsage" />
      </UserDashboardCharts>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import { usageAPI, type UserDashboardStats as UserStatsType } from '@/api/usage'
import AppLayout from '@/components/layout/AppLayout.vue'
import UserDashboardStats from '@/components/user/dashboard/UserDashboardStats.vue'
import UserDashboardCharts from '@/components/user/dashboard/UserDashboardCharts.vue'
import UserDashboardRecentUsage from '@/components/user/dashboard/UserDashboardRecentUsage.vue'
import UserDashboardQuickActions from '@/components/user/dashboard/UserDashboardQuickActions.vue'
import type { UsageLog, TrendDataPoint, ModelStat, PlatformQuotaItem } from '@/types'
import { getMyPlatformQuotas } from '@/api/user'
import { formatDateLocalInput } from '@/utils/format'
import { FeatureFlags, resolveFeatureFlag } from '@/utils/featureFlags'
import { resolveSiteBillingMode } from '@/utils/siteBillingMode'

const { t } = useI18n()
const authStore = useAuthStore()
const appStore = useAppStore()
const user = computed(() => authStore.user)
const stats = ref<UserStatsType | null>(null)
const loading = ref(false)
const loadingUsage = ref(false)
const loadingCharts = ref(false)
const trendData = ref<TrendDataPoint[]>([])
const modelStats = ref<ModelStat[]>([])
const recentUsage = ref<UsageLog[]>([])
const platformQuotas = ref<PlatformQuotaItem[] | null>(null)

const startDate = ref(formatDateLocalInput(new Date(Date.now() - 6 * 86400000)))
const endDate = ref(formatDateLocalInput(new Date()))
const granularity = ref('day')

// ==================== 引导区 ====================
const greetingText = computed(() => {
  const hour = new Date().getHours()
  const greeting =
    hour >= 5 && hour < 11
      ? t('admin.dashboard.greeting.morning')
      : hour >= 11 && hour < 18
        ? t('admin.dashboard.greeting.afternoon')
        : t('admin.dashboard.greeting.evening')
  const name = user.value?.username || user.value?.email?.split('@')[0] || ''
  return name ? t('admin.dashboard.greeting.withName', { greeting, name }) : greeting
})

// 余额卡入口：开启在线支付且允许余额充值时去“充值”，否则去“兑换”
const balanceAction = computed(() => {
  const settings = appStore.cachedPublicSettings
  const paymentEnabled = resolveFeatureFlag(settings, FeatureFlags.payment)
  if (paymentEnabled && resolveSiteBillingMode(settings) !== 'subscription_only') {
    return { to: '/purchase', label: t('nav.recharge') }
  }
  return { to: '/redeem', label: t('nav.redeem') }
})

// ==================== 数据加载 ====================
const loadStats = async () => {
  loading.value = true
  try {
    await authStore.refreshUser()
    stats.value = await usageAPI.getDashboardStats()
  } catch (error) {
    console.error('Failed to load dashboard stats:', error)
  } finally {
    loading.value = false
  }
}

const loadCharts = async () => {
  loadingCharts.value = true
  try {
    const res = await Promise.all([
      usageAPI.getDashboardTrend({ start_date: startDate.value, end_date: endDate.value, granularity: granularity.value as any }),
      usageAPI.getDashboardModels({ start_date: startDate.value, end_date: endDate.value })
    ])
    trendData.value = res[0].trend || []
    modelStats.value = res[1].models || []
  } catch (error) {
    console.error('Failed to load charts:', error)
  } finally {
    loadingCharts.value = false
  }
}

const loadRecent = async () => {
  loadingUsage.value = true
  try {
    const res = await usageAPI.getByDateRange(startDate.value, endDate.value)
    recentUsage.value = res.items.slice(0, 5)
  } catch (error) {
    console.error('Failed to load recent usage:', error)
  } finally {
    loadingUsage.value = false
  }
}

const loadPlatformQuotas = async () => {
  try {
    const data = await getMyPlatformQuotas()
    platformQuotas.value = data.platform_quotas ?? []
  } catch (error) {
    console.warn('Failed to load platform quotas:', error)
    platformQuotas.value = []
  }
}

// 时间范围变化：一天以内自动切到按小时；“最近使用”也随范围刷新
const onDateRangeChange = (range: { startDate: string; endDate: string; preset: string | null }) => {
  const start = new Date(range.startDate)
  const end = new Date(range.endDate)
  const daysDiff = Math.ceil((end.getTime() - start.getTime()) / 86400000)
  granularity.value = daysDiff <= 1 ? 'hour' : 'day'
  void loadCharts()
  void loadRecent()
}

const refreshAll = () => {
  loadStats()
  loadCharts()
  loadRecent()
  loadPlatformQuotas()
}

onMounted(() => {
  refreshAll()
})
</script>
