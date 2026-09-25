<template>
  <section class="card flex h-full flex-col">
    <header class="card-section-header">
      <div class="min-w-0">
        <h3 class="card-section-title">{{ title || t('usage.endpointDistribution') }}</h3>
        <p v-if="summaryText" class="card-section-subtitle">{{ summaryText }}</p>
      </div>
      <div class="flex flex-wrap items-center justify-end gap-2">
        <SegmentedControl
          v-if="showSourceToggle"
          :model-value="source"
          size="sm"
          :options="sourceOptions"
          @update:model-value="(value) => emit('update:source', value)"
        />
        <SegmentedControl
          v-if="showMetricToggle"
          :model-value="metric"
          size="sm"
          :options="metricOptions"
          @update:model-value="(value) => emit('update:metric', value)"
        />
      </div>
    </header>

    <div class="flex-1 pb-2">
      <DistributionTable
        :rows="endpointRows"
        :name-header="t('usage.endpoint')"
        :metric="metric"
        :columns="['requests', 'tokens', 'actual', 'standard']"
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
import DistributionTable, { type DistributionRow } from './DistributionTable.vue'
import type { EndpointStat, UserBreakdownItem } from '@/types'
import { getUserBreakdown } from '@/api/admin/dashboard'
import { formatCompact, formatUSD } from './chartTheme'

const { t } = useI18n()

type DistributionMetric = 'tokens' | 'actual_cost'
type EndpointSource = 'inbound' | 'upstream' | 'path'

const props = withDefaults(
  defineProps<{
    endpointStats: EndpointStat[]
    upstreamEndpointStats?: EndpointStat[]
    endpointPathStats?: EndpointStat[]
    loading?: boolean
    title?: string
    metric?: DistributionMetric
    source?: EndpointSource
    showMetricToggle?: boolean
    showSourceToggle?: boolean
    enableBreakdown?: boolean
    startDate?: string
    endDate?: string
    filters?: Record<string, any>
  }>(),
  {
    upstreamEndpointStats: () => [],
    endpointPathStats: () => [],
    loading: false,
    title: '',
    metric: 'tokens',
    source: 'inbound',
    showMetricToggle: false,
    showSourceToggle: false,
    enableBreakdown: true
  }
)

const emit = defineEmits<{
  'update:metric': [value: DistributionMetric]
  'update:source': [value: EndpointSource]
}>()

const sourceOptions = computed(() => [
  { value: 'inbound' as EndpointSource, label: t('usage.inbound') },
  { value: 'upstream' as EndpointSource, label: t('usage.upstream') },
  { value: 'path' as EndpointSource, label: t('usage.path') }
])

const metricOptions = computed(() => [
  { value: 'tokens' as DistributionMetric, label: t('admin.dashboard.metricTokens') },
  { value: 'actual_cost' as DistributionMetric, label: t('admin.dashboard.metricActualCost') }
])

const expandedKey = ref<string | null>(null)
const breakdownItems = ref<UserBreakdownItem[]>([])
const breakdownLoading = ref(false)

const toggleBreakdown = async (endpoint: string) => {
  if (expandedKey.value === endpoint) {
    expandedKey.value = null
    return
  }
  expandedKey.value = endpoint
  breakdownLoading.value = true
  breakdownItems.value = []
  try {
    const res = await getUserBreakdown({
      ...props.filters,
      start_date: props.startDate,
      end_date: props.endDate,
      endpoint,
      endpoint_type: props.source,
    })
    if (expandedKey.value === endpoint) breakdownItems.value = res.users || []
  } catch {
    if (expandedKey.value === endpoint) breakdownItems.value = []
  } finally {
    if (expandedKey.value === endpoint) breakdownLoading.value = false
  }
}

const displayEndpointStats = computed(() => {
  const sourceStats = props.source === 'upstream'
    ? props.upstreamEndpointStats
    : props.source === 'path'
      ? props.endpointPathStats
      : props.endpointStats
  if (!sourceStats?.length) return []

  const metricKey = props.metric === 'actual_cost' ? 'actual_cost' : 'total_tokens'
  return [...sourceStats].sort((a, b) => toFiniteNumber(b[metricKey]) - toFiniteNumber(a[metricKey]))
})

const endpointRows = computed<DistributionRow[]>(() =>
  displayEndpointStats.value.map((item) => ({
    key: item.endpoint,
    label: item.endpoint,
    requests: toFiniteNumber(item.requests),
    tokens: toFiniteNumber(item.total_tokens),
    actual: toFiniteNumber(item.actual_cost),
    standard: toFiniteNumber(item.cost)
  }))
)

const summaryText = computed(() => {
  const rows = endpointRows.value
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
