<template>
  <section class="card flex h-full flex-col">
    <header class="card-section-header">
      <div class="min-w-0">
        <h3 class="card-section-title">
          {{ activeView === 'model_distribution'
            ? t('admin.dashboard.modelDistribution')
            : t('admin.dashboard.spendingRankingTitle') }}
        </h3>
        <p v-if="summaryText" class="card-section-subtitle">{{ summaryText }}</p>
      </div>
      <div class="flex flex-wrap items-center justify-end gap-2">
        <SegmentedControl
          v-if="enableRankingView"
          v-model="activeView"
          size="sm"
          :options="viewOptions"
        />
        <SegmentedControl
          v-if="showSourceToggle && activeView === 'model_distribution'"
          :model-value="source"
          size="sm"
          :options="sourceOptions"
          @update:model-value="(value) => emit('update:source', value)"
        />
        <SegmentedControl
          v-if="showMetricToggle && activeView === 'model_distribution'"
          :model-value="metric"
          size="sm"
          :options="metricOptions"
          @update:model-value="(value) => emit('update:metric', value)"
        />
      </div>
    </header>

    <div class="flex-1 pb-2">
      <DistributionTable
        v-if="activeView === 'model_distribution'"
        :rows="modelRows"
        :name-header="t('admin.dashboard.model')"
        :metric="metric"
        :columns="distributionColumns"
        :loading="loading"
        :expandable="enableBreakdown"
        :expanded-key="expandedKey"
        :breakdown-items="breakdownItems"
        :breakdown-loading="breakdownLoading"
        @toggle="toggleBreakdown"
      />
      <div
        v-else-if="rankingError && !rankingLoading"
        class="flex min-h-[10rem] items-center justify-center text-sm text-gray-500 dark:text-dark-400"
      >
        {{ t('admin.dashboard.failedToLoad') }}
      </div>
      <DistributionTable
        v-else
        :rows="rankingRows"
        :name-header="t('admin.dashboard.spendingRankingUser')"
        metric="actual_cost"
        :columns="['requests', 'tokens', 'actual']"
        :column-labels="rankingColumnLabels"
        :loading="rankingLoading"
        ranked
        clickable
        @select="onRankingSelect"
      />
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import DistributionTable, { type DistributionColumn, type DistributionRow } from './DistributionTable.vue'
import type { ModelStat, UserSpendingRankingItem, UserBreakdownItem } from '@/types'
import { getUserBreakdown } from '@/api/admin/dashboard'
import { formatCompact, formatUSD } from './chartTheme'

const { t } = useI18n()

type DistributionMetric = 'tokens' | 'actual_cost'
type ModelSource = 'requested' | 'upstream' | 'mapping'
type RankingDisplayItem = UserSpendingRankingItem & { isOther?: boolean }
const props = withDefaults(defineProps<{
  modelStats: ModelStat[]
  upstreamModelStats?: ModelStat[]
  mappingModelStats?: ModelStat[]
  source?: ModelSource
  enableRankingView?: boolean
  rankingItems?: UserSpendingRankingItem[]
  rankingTotalActualCost?: number
  rankingTotalRequests?: number
  rankingTotalTokens?: number
  loading?: boolean
  metric?: DistributionMetric
  showSourceToggle?: boolean
  showMetricToggle?: boolean
  enableBreakdown?: boolean
  showAccountCost?: boolean
  rankingLoading?: boolean
  rankingError?: boolean
  startDate?: string
  endDate?: string
  filters?: Record<string, any>
}>(), {
  upstreamModelStats: () => [],
  mappingModelStats: () => [],
  source: 'requested',
  enableRankingView: false,
  rankingItems: () => [],
  rankingTotalActualCost: 0,
  rankingTotalRequests: 0,
  rankingTotalTokens: 0,
  loading: false,
  metric: 'tokens',
  showSourceToggle: false,
  showMetricToggle: false,
  enableBreakdown: true,
  showAccountCost: true,
  rankingLoading: false,
  rankingError: false
})

const emit = defineEmits<{
  'update:metric': [value: DistributionMetric]
  'update:source': [value: ModelSource]
  'ranking-click': [item: UserSpendingRankingItem]
}>()

const activeView = ref<'model_distribution' | 'spending_ranking'>('model_distribution')

const viewOptions = computed(() => [
  { value: 'model_distribution' as const, label: t('admin.dashboard.viewModelDistribution') },
  { value: 'spending_ranking' as const, label: t('admin.dashboard.viewSpendingRanking') }
])

const sourceOptions = computed(() => [
  { value: 'requested' as ModelSource, label: t('usage.requestedModel') },
  { value: 'upstream' as ModelSource, label: t('usage.upstreamModel') },
  { value: 'mapping' as ModelSource, label: t('usage.mapping') }
])

const metricOptions = computed(() => [
  { value: 'tokens' as DistributionMetric, label: t('admin.dashboard.metricTokens') },
  { value: 'actual_cost' as DistributionMetric, label: t('admin.dashboard.metricActualCost') }
])

const distributionColumns = computed<DistributionColumn[]>(() =>
  props.showAccountCost
    ? ['requests', 'tokens', 'actual', 'accountCost', 'standard']
    : ['requests', 'tokens', 'actual', 'standard']
)

const rankingColumnLabels = computed(() => ({
  requests: t('admin.dashboard.spendingRankingRequests'),
  tokens: t('admin.dashboard.spendingRankingTokens'),
  actual: t('admin.dashboard.spendingRankingSpend')
}))

// ==================== 用户明细（展开行） ====================
const expandedKey = ref<string | null>(null)
const breakdownItems = ref<UserBreakdownItem[]>([])
const breakdownLoading = ref(false)

const toggleBreakdown = async (model: string) => {
  if (expandedKey.value === model) {
    expandedKey.value = null
    return
  }
  expandedKey.value = model
  breakdownLoading.value = true
  breakdownItems.value = []
  try {
    const res = await getUserBreakdown({
      ...props.filters,
      start_date: props.startDate,
      end_date: props.endDate,
      model,
      model_source: props.source,
    })
    if (expandedKey.value === model) breakdownItems.value = res.users || []
  } catch {
    if (expandedKey.value === model) breakdownItems.value = []
  } finally {
    if (expandedKey.value === model) breakdownLoading.value = false
  }
}

// ==================== 模型分布 ====================
const displayModelStats = computed(() => {
  const sourceStats = props.source === 'upstream'
    ? props.upstreamModelStats
    : props.source === 'mapping'
      ? props.mappingModelStats
      : props.modelStats
  if (!sourceStats?.length) return []

  const metricKey = props.metric === 'actual_cost' ? 'actual_cost' : 'total_tokens'
  return [...sourceStats].sort((a, b) => toFiniteNumber(b[metricKey]) - toFiniteNumber(a[metricKey]))
})

const modelRows = computed<DistributionRow[]>(() =>
  displayModelStats.value.map((m) => ({
    key: m.model,
    label: m.model,
    requests: toFiniteNumber(m.requests),
    tokens: toFiniteNumber(m.total_tokens),
    actual: toFiniteNumber(m.actual_cost),
    accountCost: toFiniteNumber(m.account_cost),
    standard: toFiniteNumber(m.cost)
  }))
)

// ==================== 用户消费榜 ====================
const otherRankingItem = computed<RankingDisplayItem | null>(() => {
  if (!props.rankingItems?.length) return null

  const rankedActualCost = props.rankingItems.reduce((sum, item) => sum + toFiniteNumber(item.actual_cost), 0)
  const rankedRequests = props.rankingItems.reduce((sum, item) => sum + toFiniteNumber(item.requests), 0)
  const rankedTokens = props.rankingItems.reduce((sum, item) => sum + toFiniteNumber(item.tokens), 0)

  const otherActualCost = Math.max((props.rankingTotalActualCost || 0) - rankedActualCost, 0)
  const otherRequests = Math.max((props.rankingTotalRequests || 0) - rankedRequests, 0)
  const otherTokens = Math.max((props.rankingTotalTokens || 0) - rankedTokens, 0)

  if (otherActualCost <= 0.000001 && otherRequests <= 0 && otherTokens <= 0) return null

  return {
    user_id: 0,
    email: '',
    username: '',
    actual_cost: otherActualCost,
    requests: otherRequests,
    tokens: otherTokens,
    isOther: true
  }
})

const rankingDisplayItems = computed<RankingDisplayItem[]>(() => {
  if (!props.rankingItems?.length) return []
  return otherRankingItem.value
    ? [...props.rankingItems, otherRankingItem.value]
    : [...props.rankingItems]
})

const rankingRows = computed<DistributionRow[]>(() =>
  rankingDisplayItems.value.map((item, index) => ({
    key: item.isOther ? '__others__' : `${item.user_id}-${index}`,
    label: getRankingRowLabel(item),
    requests: toFiniteNumber(item.requests),
    tokens: toFiniteNumber(item.tokens),
    actual: toFiniteNumber(item.actual_cost),
    muted: item.isOther
  }))
)

function onRankingSelect(key: string) {
  const index = rankingRows.value.findIndex((row) => row.key === key)
  const item = rankingDisplayItems.value[index]
  if (item && !item.isOther) emit('ranking-click', item)
}

// ==================== 摘要 ====================
const summaryText = computed(() => {
  if (activeView.value === 'model_distribution') {
    const rows = modelRows.value
    if (!rows.length) return ''
    const total = props.metric === 'actual_cost'
      ? formatUSD(rows.reduce((sum, r) => sum + r.actual, 0))
      : formatCompact(rows.reduce((sum, r) => sum + r.tokens, 0))
    return `${t('admin.dashboard.distribution.summary', { count: rows.length })} · ${total}`
  }
  if (!props.rankingItems?.length) return ''
  return formatUSD(props.rankingTotalActualCost)
})

const getRankingUserLabel = (item: UserSpendingRankingItem): string => {
  if (item.username?.trim()) return item.username.trim()
  if (item.email?.trim()) return item.email.trim()
  return t('admin.redeem.userPrefix', { id: item.user_id })
}

const getRankingRowLabel = (item: RankingDisplayItem): string => {
  if (item.isOther) return t('admin.dashboard.spendingRankingOther')
  return getRankingUserLabel(item)
}

const toFiniteNumber = (value: unknown): number => {
  const numberValue = Number(value)
  return Number.isFinite(numberValue) ? numberValue : 0
}
</script>
