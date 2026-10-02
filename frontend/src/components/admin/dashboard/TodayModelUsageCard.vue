<template>
  <section class="card flex flex-col">
    <header class="card-section-header">
      <div class="min-w-0">
        <h3 class="card-section-title">{{ t('admin.dashboard.todayModels.title') }}</h3>
        <p class="card-section-subtitle">
          {{ t('admin.dashboard.todayModels.subtitle', { count: models.length }) }}
        </p>
      </div>
      <SegmentedControl v-model="sortBy" size="sm" :options="sortOptions" :aria-label="t('admin.dashboard.todayModels.sortBy')" />
    </header>

    <div class="px-2 pb-3">
      <div v-if="loading && rows.length === 0" class="space-y-2 px-3 py-2" aria-hidden="true">
        <div v-for="i in 5" :key="i" class="skeleton h-5 w-full rounded" />
      </div>
      <div v-else-if="rows.length === 0" class="flex min-h-[10rem] items-center justify-center text-sm text-gray-500 dark:text-dark-400">
        {{ t('admin.dashboard.todayModels.empty') }}
      </div>
      <div v-else class="overflow-x-auto transition-opacity" :class="loading ? 'opacity-50' : ''">
        <table class="w-full min-w-[46rem] text-xs">
          <caption class="sr-only">{{ t('admin.dashboard.todayModels.title') }}</caption>
          <thead>
            <tr class="text-gray-500 dark:text-dark-400">
              <th scope="col" class="px-3 py-2 text-left font-medium">{{ t('admin.dashboard.todayModels.model') }}</th>
              <th scope="col" class="w-[22%] px-3 py-2 text-left font-medium">{{ sortLabel }}</th>
              <th scope="col" class="px-3 py-2 text-right font-medium">{{ t('admin.dashboard.todayModels.requests') }}</th>
              <th scope="col" class="px-3 py-2 text-right font-medium">{{ t('admin.dashboard.todayModels.tokens') }}</th>
              <th scope="col" class="px-3 py-2 text-right font-medium">{{ t('admin.dashboard.todayModels.cacheHit') }}</th>
              <th scope="col" class="px-3 py-2 text-right font-medium">{{ t('admin.dashboard.todayModels.revenue') }}</th>
              <th scope="col" class="px-3 py-2 text-right font-medium">{{ t('admin.dashboard.todayModels.accountCost') }}</th>
              <th scope="col" class="px-3 py-2 text-right font-medium">{{ t('admin.dashboard.todayModels.margin') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="row in rows"
              :key="row.key"
              class="border-t border-gray-100 dark:border-dark-800"
              :class="row.other ? 'text-gray-500 dark:text-dark-400' : ''"
            >
              <th scope="row" class="max-w-[16rem] px-3 py-2 text-left font-normal">
                <span class="flex min-w-0 items-center gap-2">
                  <ModelIcon v-if="!row.other" :model="row.model" size="16px" />
                  <span class="truncate text-gray-900 dark:text-gray-100" :title="row.model">{{ row.label }}</span>
                </span>
              </th>
              <!-- 单一序列的占比条：一种颜色，长度即数值，不再用色深重复编码 -->
              <td class="px-3 py-2">
                <span class="flex items-center gap-2">
                  <span class="h-1.5 flex-1 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-800" aria-hidden="true">
                    <span
                      class="block h-full rounded-full"
                      :style="{ width: `${Math.max(row.share * 100, row.share > 0 ? 1.5 : 0)}%`, backgroundColor: row.other ? theme.muted : theme.accent }"
                    />
                  </span>
                  <span class="w-12 text-right tabular-nums text-gray-600 dark:text-dark-300">{{ formatPercent(row.share) }}</span>
                </span>
              </td>
              <td class="whitespace-nowrap px-3 py-2 text-right tabular-nums text-gray-900 dark:text-gray-100">{{ formatCompact(row.requests) }}</td>
              <td class="whitespace-nowrap px-3 py-2 text-right tabular-nums text-gray-900 dark:text-gray-100" :title="formatNumber(row.tokens)">{{ formatCompact(row.tokens) }}</td>
              <td class="whitespace-nowrap px-3 py-2 text-right tabular-nums text-gray-900 dark:text-gray-100">{{ formatRatio(row.cacheHit) }}</td>
              <td class="whitespace-nowrap px-3 py-2 text-right font-medium tabular-nums text-gray-900 dark:text-gray-100">{{ formatUSD(row.revenue) }}</td>
              <td class="whitespace-nowrap px-3 py-2 text-right tabular-nums text-gray-600 dark:text-dark-300">{{ formatUSD(row.accountCost) }}</td>
              <td class="whitespace-nowrap px-3 py-2 text-right tabular-nums text-gray-900 dark:text-gray-100">{{ formatRatio(row.margin) }}</td>
            </tr>
          </tbody>
          <tfoot v-if="rows.length > 1">
            <tr class="border-t border-gray-200 font-medium text-gray-900 dark:border-dark-700 dark:text-gray-100">
              <th scope="row" class="px-3 py-2 text-left">{{ t('admin.dashboard.todayModels.total') }}</th>
              <td />
              <td class="px-3 py-2 text-right tabular-nums">{{ formatCompact(totals.requests) }}</td>
              <td class="px-3 py-2 text-right tabular-nums">{{ formatCompact(totals.tokens) }}</td>
              <td class="px-3 py-2 text-right tabular-nums">{{ formatRatio(totals.cacheHit) }}</td>
              <td class="px-3 py-2 text-right tabular-nums">{{ formatUSD(totals.revenue) }}</td>
              <td class="px-3 py-2 text-right tabular-nums">{{ formatUSD(totals.accountCost) }}</td>
              <td class="px-3 py-2 text-right tabular-nums">{{ formatRatio(totals.margin) }}</td>
            </tr>
          </tfoot>
        </table>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import ModelIcon from '@/components/common/ModelIcon.vue'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import type { ModelStat } from '@/types'
import {
  cacheHitRatio,
  formatCompact,
  formatPercent,
  formatUSD,
  marginRatio,
  useChartTheme
} from '@/components/charts/chartTheme'

type SortKey = 'revenue' | 'tokens' | 'requests'

const props = withDefaults(defineProps<{ models: ModelStat[]; loading?: boolean; limit?: number }>(), {
  loading: false,
  limit: 10
})

const { t } = useI18n()
const theme = useChartTheme()
const sortBy = ref<SortKey>('revenue')

const sortOptions = computed(() => [
  { value: 'revenue' as SortKey, label: t('admin.dashboard.todayModels.byRevenue') },
  { value: 'tokens' as SortKey, label: t('admin.dashboard.todayModels.byTokens') },
  { value: 'requests' as SortKey, label: t('admin.dashboard.todayModels.byRequests') }
])
const sortLabel = computed(() => t('admin.dashboard.todayModels.share', { metric: sortOptions.value.find((o) => o.value === sortBy.value)?.label ?? '' }))

const num = (v: unknown) => {
  const n = Number(v)
  return Number.isFinite(n) ? n : 0
}
const formatNumber = (v: number) => num(v).toLocaleString()
const formatRatio = (ratio: number | null) => (ratio === null ? '—' : formatPercent(ratio))

interface Row {
  key: string
  model: string
  label: string
  other: boolean
  requests: number
  tokens: number
  input: number
  cacheCreation: number
  cacheRead: number
  revenue: number
  accountCost: number
  share: number
  cacheHit: number | null
  margin: number | null
}

const toRow = (m: ModelStat): Row => {
  const tokens =
    num(m.total_tokens) || num(m.input_tokens) + num(m.output_tokens) + num(m.cache_creation_tokens) + num(m.cache_read_tokens)
  return {
    key: m.model,
    model: m.model,
    label: m.model || '-',
    other: false,
    requests: num(m.requests),
    tokens,
    input: num(m.input_tokens),
    cacheCreation: num(m.cache_creation_tokens),
    cacheRead: num(m.cache_read_tokens),
    revenue: num(m.actual_cost),
    accountCost: num(m.account_cost),
    share: 0,
    cacheHit: null,
    margin: null
  }
}

const finish = (row: Row, total: number) => {
  row.share = total > 0 ? metricOf(row) / total : 0
  row.cacheHit = cacheHitRatio(row.input, row.cacheCreation, row.cacheRead)
  row.margin = marginRatio(row.revenue, row.accountCost)
  return row
}

const metricOf = (row: Row) => (sortBy.value === 'revenue' ? row.revenue : sortBy.value === 'tokens' ? row.tokens : row.requests)

const allRows = computed(() => (props.models ?? []).map(toRow))

const totals = computed(() => {
  const sum = allRows.value.reduce(
    (acc, r) => {
      acc.requests += r.requests
      acc.tokens += r.tokens
      acc.input += r.input
      acc.cacheCreation += r.cacheCreation
      acc.cacheRead += r.cacheRead
      acc.revenue += r.revenue
      acc.accountCost += r.accountCost
      return acc
    },
    { requests: 0, tokens: 0, input: 0, cacheCreation: 0, cacheRead: 0, revenue: 0, accountCost: 0 }
  )
  return {
    ...sum,
    cacheHit: cacheHitRatio(sum.input, sum.cacheCreation, sum.cacheRead),
    margin: marginRatio(sum.revenue, sum.accountCost)
  }
})

// 前 N 个模型单列，其余折叠为“其他”，不为长尾分配更多颜色或行
const rows = computed<Row[]>(() => {
  const sorted = [...allRows.value].sort((a, b) => metricOf(b) - metricOf(a))
  const total = sorted.reduce((acc, r) => acc + metricOf(r), 0)
  const head = sorted.slice(0, props.limit).map((r) => finish({ ...r }, total))
  const tail = sorted.slice(props.limit)
  if (tail.length > 0) {
    const other = tail.reduce<Row>(
      (acc, r) => {
        acc.requests += r.requests
        acc.tokens += r.tokens
        acc.input += r.input
        acc.cacheCreation += r.cacheCreation
        acc.cacheRead += r.cacheRead
        acc.revenue += r.revenue
        acc.accountCost += r.accountCost
        return acc
      },
      {
        key: '__other__',
        model: '',
        label: t('admin.dashboard.todayModels.other', { count: tail.length }),
        other: true,
        requests: 0,
        tokens: 0,
        input: 0,
        cacheCreation: 0,
        cacheRead: 0,
        revenue: 0,
        accountCost: 0,
        share: 0,
        cacheHit: null,
        margin: null
      }
    )
    head.push(finish(other, total))
  }
  return head
})
</script>
