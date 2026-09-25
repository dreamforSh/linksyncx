<template>
  <section class="card flex h-full flex-col">
    <header class="card-section-header">
      <div class="min-w-0">
        <h3 class="card-section-title">{{ t('admin.dashboard.groupDistribution') }}</h3>
        <p v-if="summaryText" class="card-section-subtitle">{{ summaryText }}</p>
      </div>
      <SegmentedControl
        v-if="showMetricToggle"
        :model-value="metric"
        size="sm"
        :options="metricOptions"
        @update:model-value="(value) => emit('update:metric', value)"
      />
    </header>

    <div class="flex-1 pb-2">
      <DistributionTable
        :rows="groupRows"
        :name-header="t('admin.dashboard.group')"
        :metric="metric"
        :columns="distributionColumns"
        :loading="loading"
        :expandable="enableBreakdown"
        :expanded-key="expandedKey"
        :breakdown-items="breakdownItems"
        :breakdown-loading="breakdownLoading"
        @toggle="toggleBreakdown"
      />
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import DistributionTable, { type DistributionColumn, type DistributionRow } from './DistributionTable.vue'
import type { GroupStat, UserBreakdownItem } from '@/types'
import { getUserBreakdown } from '@/api/admin/dashboard'
import { formatCompact, formatUSD } from './chartTheme'

const { t } = useI18n()

type DistributionMetric = 'tokens' | 'actual_cost'

const props = withDefaults(defineProps<{
  groupStats: GroupStat[]
  loading?: boolean
  metric?: DistributionMetric
  showMetricToggle?: boolean
  enableBreakdown?: boolean
  showAccountCost?: boolean
  startDate?: string
  endDate?: string
  filters?: Record<string, any>
}>(), {
  loading: false,
  metric: 'tokens',
  showMetricToggle: false,
  enableBreakdown: true,
  showAccountCost: true,
})

const emit = defineEmits<{
  'update:metric': [value: DistributionMetric]
}>()

const metricOptions = computed(() => [
  { value: 'tokens' as DistributionMetric, label: t('admin.dashboard.metricTokens') },
  { value: 'actual_cost' as DistributionMetric, label: t('admin.dashboard.metricActualCost') }
])

const distributionColumns = computed<DistributionColumn[]>(() =>
  props.showAccountCost
    ? ['requests', 'tokens', 'actual', 'accountCost', 'standard']
    : ['requests', 'tokens', 'actual', 'standard']
)

const expandedKey = ref<string | null>(null)
const breakdownItems = ref<UserBreakdownItem[]>([])
const breakdownLoading = ref(false)

const toggleBreakdown = async (key: string) => {
  if (expandedKey.value === key) {
    expandedKey.value = null
    return
  }
  expandedKey.value = key
  breakdownLoading.value = true
  breakdownItems.value = []
  try {
    const res = await getUserBreakdown({
      ...props.filters,
      start_date: props.startDate,
      end_date: props.endDate,
      group_id: Number(key),
    })
    if (expandedKey.value === key) breakdownItems.value = res.users || []
  } catch {
    if (expandedKey.value === key) breakdownItems.value = []
  } finally {
    if (expandedKey.value === key) breakdownLoading.value = false
  }
}

const displayGroupStats = computed(() => {
  if (!props.groupStats?.length) return []

  const metricKey = props.metric === 'actual_cost' ? 'actual_cost' : 'total_tokens'
  return [...props.groupStats].sort((a, b) => toFiniteNumber(b[metricKey]) - toFiniteNumber(a[metricKey]))
})

const groupRows = computed<DistributionRow[]>(() =>
  displayGroupStats.value.map((g) => ({
    key: String(g.group_id),
    label: g.group_name || t('admin.dashboard.noGroup'),
    requests: toFiniteNumber(g.requests),
    tokens: toFiniteNumber(g.total_tokens),
    actual: toFiniteNumber(g.actual_cost),
    accountCost: toFiniteNumber(g.account_cost),
    standard: toFiniteNumber(g.cost),
    // “无分组”没有可下钻的 group_id
    disabled: !(g.group_id > 0)
  }))
)

const summaryText = computed(() => {
  const rows = groupRows.value
  if (!rows.length) return ''
  const total = props.metric === 'actual_cost'
    ? formatUSD(rows.reduce((sum, r) => sum + r.actual, 0))
    : formatCompact(rows.reduce((sum, r) => sum + r.tokens, 0))
  return `${t('admin.dashboard.distribution.summary', { count: rows.length })} · ${total}`
})

const toFiniteNumber = (value: unknown): number => {
  const numberValue = Number(value)
  return Number.isFinite(numberValue) ? numberValue : 0
}
</script>
