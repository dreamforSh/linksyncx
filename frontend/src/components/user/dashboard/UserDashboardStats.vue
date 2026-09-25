<template>
  <!-- Row 1: 核心指标 -->
  <div class="grid grid-cols-1 gap-4 sm:grid-cols-2" :class="isSimple ? 'xl:grid-cols-3' : 'xl:grid-cols-4'">
    <!-- Balance -->
    <StatTile
      v-if="!isSimple"
      :label="t('dashboard.balance')"
      icon="creditCard"
      :value="formatUSD(balance)"
      :loading="loading"
    >
      {{ t('common.available') }}
      <template v-if="balanceAction" #badge>
        <router-link
          :to="balanceAction.to"
          class="inline-flex items-center gap-0.5 rounded-md px-1.5 py-0.5 text-xs font-medium text-primary-700 transition-colors hover:bg-primary-50 dark:text-primary-300 dark:hover:bg-primary-500/10"
        >
          {{ balanceAction.label }}
          <Icon name="arrowRight" size="xs" class="h-3 w-3" :stroke-width="2" />
        </router-link>
      </template>
    </StatTile>

    <!-- Today Requests -->
    <StatTile
      :label="t('dashboard.todayRequests')"
      icon="chart"
      :value="stats ? formatNumber(stats.today_requests || 0) : '—'"
      :loading="loading"
      :trend="requestsTrend"
      :trend-label="t('admin.dashboard.kpi.trendHint')"
    >
      <template v-if="stats">{{ t('admin.dashboard.kpi.cumulative', { value: formatNumber(stats.total_requests || 0) }) }}</template>
    </StatTile>

    <!-- Today Cost -->
    <StatTile
      :label="t('dashboard.todayCost')"
      icon="dollar"
      :value="stats ? formatUSD(stats.today_actual_cost || 0) : '—'"
      :loading="loading"
      :trend="costTrend"
      :trend-label="t('admin.dashboard.kpi.trendHint')"
    >
      <template v-if="stats">
        {{ t('admin.dashboard.kpi.standardCost') }}
        <span class="tabular-nums text-gray-700 dark:text-gray-300">{{ formatUSD(stats.today_cost || 0) }}</span>
        <span class="mx-1" aria-hidden="true">·</span>
        {{ t('admin.dashboard.kpi.cumulative', { value: formatUSD(stats.total_actual_cost || 0) }) }}
      </template>
    </StatTile>

    <!-- Today Tokens -->
    <StatTile
      :label="t('dashboard.todayTokens')"
      icon="cube"
      :value="stats ? formatCompact(stats.today_tokens || 0) : '—'"
      :value-title="stats ? formatNumber(stats.today_tokens || 0) : undefined"
      :loading="loading"
      :trend="tokensTrend"
      :trend-label="t('admin.dashboard.kpi.trendHint')"
    >
      <template v-if="stats">
        {{ t('admin.dashboard.kpi.tokensBreakdown', {
          input: formatCompact(stats.today_input_tokens || 0),
          output: formatCompact(stats.today_output_tokens || 0),
          cache: formatCompact((stats.today_cache_creation_tokens || 0) + (stats.today_cache_read_tokens || 0))
        }) }}
      </template>
    </StatTile>
  </div>

  <!-- Row 2: 次要指标 -->
  <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
    <StatTile
      compact
      :label="t('dashboard.apiKeys')"
      icon="key"
      :value="stats ? formatNumber(stats.total_api_keys || 0) : '—'"
      :loading="loading"
    >
      <template v-if="stats">
        {{ t('admin.dashboard.kpi.apiKeysSummary', { active: formatNumber(stats.active_api_keys || 0), total: formatNumber(stats.total_api_keys || 0) }) }}
      </template>
    </StatTile>

    <StatTile
      compact
      :label="t('dashboard.totalTokens')"
      icon="database"
      :value="stats ? formatCompact(stats.total_tokens || 0) : '—'"
      :value-title="stats ? formatNumber(stats.total_tokens || 0) : undefined"
      :loading="loading"
    >
      <template v-if="stats">
        {{ t('admin.dashboard.kpi.tokensBreakdown', {
          input: formatCompact(stats.total_input_tokens || 0),
          output: formatCompact(stats.total_output_tokens || 0),
          cache: formatCompact((stats.total_cache_creation_tokens || 0) + (stats.total_cache_read_tokens || 0))
        }) }}
      </template>
    </StatTile>

    <StatTile
      compact
      :label="t('admin.dashboard.kpi.throughput')"
      icon="bolt"
      :value="stats ? formatCompact(stats.rpm || 0) : '—'"
      unit="RPM"
      :loading="loading"
    >
      <template v-if="stats">
        <span class="tabular-nums text-gray-700 dark:text-gray-300">{{ formatCompact(stats.tpm || 0) }}</span> TPM
        <span class="mx-1" aria-hidden="true">·</span>
        {{ t('admin.dashboard.kpi.throughputHint') }}
      </template>
    </StatTile>

    <StatTile
      compact
      :label="t('dashboard.avgResponse')"
      icon="clock"
      :value="stats ? formatDurationMs(stats.average_duration_ms || 0) : '—'"
      :loading="loading"
    />
  </div>

  <!-- Row 3: 按平台拆分 -->
  <section v-if="!isSimple && platformCards.length > 0" class="card">
    <header class="card-section-header">
      <div class="min-w-0">
        <h3 class="card-section-title">{{ t('dashboard.platformBreakdown') }}</h3>
        <p class="card-section-subtitle">{{ t('dashboard.platformCount', { count: platformCount }) }}</p>
      </div>
    </header>
    <div class="grid grid-cols-1 gap-3 px-5 pb-5 sm:grid-cols-2 xl:grid-cols-4">
      <div
        v-for="item in platformCards"
        :key="item.platform"
        data-testid="platform-card"
        :data-platform="item.platform"
        :class="[
          'flex flex-col rounded-lg border p-3.5',
          item.isOther
            ? 'border-dashed border-gray-300 bg-gray-50/70 dark:border-dark-600 dark:bg-dark-800/40'
            : 'border-gray-200 dark:border-dark-700'
        ]"
      >
        <div class="flex items-center gap-2">
          <span
            class="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-gray-200"
            aria-hidden="true"
          >
            <Icon v-if="item.isOther" name="grid" size="sm" />
            <PlatformIcon v-else :platform="item.platform as GroupPlatform" size="md" />
          </span>
          <span class="truncate text-sm font-semibold text-gray-900 dark:text-white">
            {{ item.isOther ? t('dashboard.platformOther') : platformLabel(item.platform) }}
          </span>
        </div>

        <p class="mt-3 text-lg font-semibold leading-7 text-gray-900 dark:text-white" :title="t('dashboard.actual')">
          {{ formatUSD(item.total_actual_cost) }}
        </p>
        <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('dashboard.platformTotalSpend') }}</p>

        <dl class="mt-3 space-y-1 border-t border-gray-100 pt-2.5 text-xs dark:border-dark-700">
          <div class="flex items-center justify-between">
            <dt class="text-gray-500 dark:text-dark-400">{{ t('dashboard.todayCost') }}</dt>
            <dd class="tabular-nums text-gray-900 dark:text-white">{{ formatUSD(item.today_actual_cost) }}</dd>
          </div>
          <div class="flex items-center justify-between">
            <dt class="text-gray-500 dark:text-dark-400">{{ t('dashboard.requests') }}</dt>
            <dd class="tabular-nums text-gray-700 dark:text-gray-300">
              {{ item.total_requests > 0 ? formatNumber(item.total_requests) : '-' }}
            </dd>
          </div>
          <div class="flex items-center justify-between">
            <dt class="text-gray-500 dark:text-dark-400">{{ t('dashboard.tokens') }}</dt>
            <dd class="tabular-nums text-gray-700 dark:text-gray-300">
              {{ item.total_tokens > 0 ? formatCompact(item.total_tokens) : '-' }}
            </dd>
          </div>
        </dl>

        <!-- Quota 区：仅当 quota 配置存在、非 __other__ 且至少有一个窗口配了 limit 时显示 -->
        <div v-if="hasAnyLimit(item.quota) && !item.isOther" class="mt-3 space-y-2 border-t border-gray-100 pt-2.5 dark:border-dark-700">
          <p class="text-[11px] font-medium text-gray-500 dark:text-dark-400">
            {{ t('dashboard.platformQuota.title') }}
          </p>
          <template v-for="w in (['daily', 'weekly', 'monthly'] as const)" :key="w">
            <div v-if="quotaVal(item.quota, `${w}_limit_usd`) != null" class="space-y-1">
              <!-- limit=0：完全禁用 -->
              <template v-if="(quotaVal(item.quota, `${w}_limit_usd`) as number) === 0">
                <div class="flex items-center justify-between text-xs">
                  <span class="text-gray-600 dark:text-gray-300">{{ t(`dashboard.platformQuota.${w}`) }}</span>
                  <span class="inline-flex items-center gap-1 font-medium text-red-600 dark:text-red-400">
                    <Icon name="ban" size="xs" class="h-3 w-3" :stroke-width="2" />
                    {{ t('dashboard.platformQuota.disabled') }}
                  </span>
                </div>
                <div class="h-1.5 w-full overflow-hidden rounded-full bg-red-100 dark:bg-red-500/15">
                  <div class="h-full w-full rounded-full bg-red-500" />
                </div>
              </template>
              <!-- limit>0：用量进度条，颜色随用量升高提示风险，同时给出数值 -->
              <template v-else>
                <div class="flex items-center justify-between gap-2 text-xs">
                  <span class="text-gray-600 dark:text-gray-300">{{ t(`dashboard.platformQuota.${w}`) }}</span>
                  <span class="tabular-nums text-gray-700 dark:text-gray-200">
                    ${{ formatUsd((quotaVal(item.quota, `${w}_usage_usd`) as number) ?? 0) }} / ${{ formatUsd(quotaVal(item.quota, `${w}_limit_usd`) as number) }}
                  </span>
                </div>
                <div
                  class="h-1.5 w-full overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700"
                  role="meter"
                  :aria-valuenow="calcPercent((quotaVal(item.quota, `${w}_usage_usd`) as number) ?? 0, quotaVal(item.quota, `${w}_limit_usd`) as number)"
                  aria-valuemin="0"
                  aria-valuemax="100"
                  :aria-label="t(`dashboard.platformQuota.${w}`)"
                >
                  <div
                    class="h-full rounded-full transition-all"
                    :class="quotaBarClass(calcPercent((quotaVal(item.quota, `${w}_usage_usd`) as number) ?? 0, quotaVal(item.quota, `${w}_limit_usd`) as number))"
                    :style="{ width: calcPercent((quotaVal(item.quota, `${w}_usage_usd`) as number) ?? 0, quotaVal(item.quota, `${w}_limit_usd`) as number) + '%' }"
                  />
                </div>
                <p v-if="quotaVal(item.quota, `${w}_window_resets_at`)" class="text-[11px] text-gray-500 dark:text-dark-400">
                  {{ t('dashboard.platformQuota.resetsAt', { time: formatResetTime(quotaVal(item.quota, `${w}_window_resets_at`) as string) }) }}
                </p>
              </template>
            </div>
          </template>
        </div>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import StatTile from '@/components/common/StatTile.vue'
import { formatCompact, formatDurationMs, formatUSD } from '@/components/charts/chartTheme'
import type { PlatformDashboardStats, UserDashboardStats as UserStatsType } from '@/api/usage'
import type { GroupPlatform, PlatformQuotaItem, TrendDataPoint } from '@/types'

interface FusedPlatformCard {
  platform: string
  total_actual_cost: number
  today_actual_cost: number
  total_requests: number
  total_tokens: number
  isOther?: boolean
  quota?: PlatformQuotaItem
}

const props = withDefaults(
  defineProps<{
    stats: UserStatsType | null
    balance: number
    isSimple: boolean
    platformQuotas?: PlatformQuotaItem[] | null
    /** 所选时间范围的趋势点，用于指标卡底部的迷你趋势线 */
    trend?: TrendDataPoint[]
    /** 首次加载（尚无数据）时显示骨架 */
    loading?: boolean
    /** 余额卡右上角的入口（如“充值”），由页面按站点功能开关决定 */
    balanceAction?: { to: string; label: string } | null
  }>(),
  {
    platformQuotas: null,
    trend: () => [],
    loading: false,
    balanceAction: null
  }
)
const { t } = useI18n()

const requestsTrend = computed(() => props.trend.map((p) => p.requests))
const costTrend = computed(() => props.trend.map((p) => p.actual_cost))
const tokensTrend = computed(() => props.trend.map((p) => p.total_tokens))

const PLATFORM_LABELS: Record<string, string> = {
  anthropic: 'Claude',
  openai: 'OpenAI',
  gemini: 'Gemini',
  antigravity: 'Antigravity',
  grok: 'Grok',
  kimi: 'Kimi',
  zhipu: 'Zhipu GLM',
  deepseek: 'DeepSeek',
  minimax: 'MiniMax',
}

const platformLabel = (p: string) => PLATFORM_LABELS[p] ?? p

// 处理"各平台之和 < 总值"的差值：后端按平台聚合时过滤了无法归属平台的行
// （group 与 account 都缺 platform）。这里把差值作为"其他"卡片显式展示，
// 避免 Row 1 总值与 Row 3 平台拆分加总对不上、用户困惑。
const OTHER_THRESHOLD = 0.0001
const platformCards = computed<FusedPlatformCard[]>(() => {
  // 建立 by_platform Map
  const byPlat = new Map<string, PlatformDashboardStats>()
  for (const item of props.stats?.by_platform ?? []) byPlat.set(item.platform, item)

  // 建立 quota Map。三档全空的记录不产生卡片，挂到卡片上也不渲染配额区。
  const byQuota = new Map<string, PlatformQuotaItem>()
  for (const q of props.platformQuotas ?? []) byQuota.set(q.platform, q)

  // 卡片集合 = 有用量的平台 ∪ 至少配置了一档限额的平台。
  // 三档全空的限额记录等价于不限额，不单独产生卡片。
  // 后端 by_platform / quota 接口均不会返回 platform='__other__'，
  // 无需显式排除；__other__ 由下方差值补差逻辑单独追加。
  const platforms = new Set<string>(byPlat.keys())
  for (const [platform, q] of byQuota) {
    if (hasAnyLimit(q)) platforms.add(platform)
  }

  const PLATFORM_ORDER = ['anthropic', 'openai', 'gemini', 'antigravity', 'grok']
  const cards: FusedPlatformCard[] = []

  for (const p of platforms) {
    const stat = byPlat.get(p)
    cards.push({
      platform: p,
      total_actual_cost: stat?.total_actual_cost ?? 0,
      today_actual_cost: stat?.today_actual_cost ?? 0,
      total_requests: stat?.total_requests ?? 0,
      total_tokens: stat?.total_tokens ?? 0,
      quota: byQuota.get(p),
    })
  }

  // 排序：按 PLATFORM_ORDER，未知平台按名称排序
  cards.sort((a, b) => {
    const ai = PLATFORM_ORDER.indexOf(a.platform)
    const bi = PLATFORM_ORDER.indexOf(b.platform)
    if (ai === -1 && bi === -1) return a.platform.localeCompare(b.platform)
    if (ai === -1) return 1
    if (bi === -1) return -1
    return ai - bi
  })

  // __other__ 补差逻辑：只对 by_platform 有 usage 数据的总和计算
  const total = props.stats?.total_actual_cost ?? 0
  const today = props.stats?.today_actual_cost ?? 0
  const sumTotal = cards.reduce((s, c) => s + c.total_actual_cost, 0)
  const sumToday = cards.reduce((s, c) => s + c.today_actual_cost, 0)
  const diffTotal = Math.max(0, total - sumTotal)
  const diffToday = Math.max(0, today - sumToday)

  if (diffTotal > OTHER_THRESHOLD || diffToday > OTHER_THRESHOLD) {
    cards.push({
      platform: '__other__',
      total_actual_cost: diffTotal,
      today_actual_cost: diffToday,
      total_requests: 0,
      total_tokens: 0,
      isOther: true,
    })
  }

  return cards
})

// 标题右侧的平台计数 = 实际渲染的平台卡片数，不含"其他"差额卡。
const platformCount = computed(() => platformCards.value.filter((c) => !c.isOther).length)

// Quota helpers

type QuotaWindow = 'daily' | 'weekly' | 'monthly'
type QuotaField = `${QuotaWindow}_limit_usd` | `${QuotaWindow}_usage_usd` | `${QuotaWindow}_window_resets_at`

function quotaVal(q: PlatformQuotaItem | undefined, key: QuotaField): PlatformQuotaItem[QuotaField] {
  return q?.[key]
}

function hasAnyLimit(q: PlatformQuotaItem | undefined): boolean {
  if (!q) return false
  return q.daily_limit_usd != null || q.weekly_limit_usd != null || q.monthly_limit_usd != null
}

function calcPercent(usage: number, limit: number): number {
  if (!limit || limit <= 0) return 0
  return Math.min(100, Math.max(0, Math.round((usage / limit) * 100)))
}

// 仪表：常态用品牌色，接近上限时依次提示警告/危险（数值始终以文字给出，不单靠颜色）
function quotaBarClass(p: number): string {
  if (p >= 95) return 'bg-red-500'
  if (p >= 75) return 'bg-amber-500'
  return 'bg-primary-600 dark:bg-primary-500'
}

// 与 formatBalance 一致使用 Intl.NumberFormat 做半偶舍入，避免 toFixed 在不同 JS 引擎
// 下偶发截断而非四舍五入（与后端展示精度不一致）。
const usdFormatter = new Intl.NumberFormat('en-US', {
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
})
function formatUsd(n: number): string {
  if (!Number.isFinite(n)) return '0.00'
  return usdFormatter.format(n)
}

function formatResetTime(iso: string | null | undefined): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString(undefined, {
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  })
}

const formatNumber = (n: number) => n.toLocaleString()
</script>
