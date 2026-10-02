<template>
  <section class="card flex flex-col">
    <header class="card-section-header">
      <div class="min-w-0">
        <h3 class="card-section-title">{{ t('admin.dashboard.revenue.title') }}</h3>
        <p class="card-section-subtitle flex flex-wrap items-center gap-x-3 gap-y-1">
          <span>{{ t('admin.dashboard.revenue.subtitle') }}</span>
          <span v-if="hasData">
            {{ t('admin.dashboard.revenue.margin') }}
            <span class="font-medium tabular-nums text-gray-700 dark:text-gray-200">{{ marginText }}</span>
          </span>
        </p>
      </div>
      <SegmentedControl v-model="view" size="sm" :options="viewOptions" :aria-label="t('admin.dashboard.trend.viewTable')" />
    </header>

    <!-- 图例：3 个序列始终显示；点击可隐藏/显示 -->
    <div v-if="hasData" class="flex flex-wrap items-center gap-x-4 gap-y-1.5 px-5 pb-2">
      <button
        v-for="s in series"
        :key="s.key"
        type="button"
        class="inline-flex items-center gap-1.5 rounded text-xs transition-opacity focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/50"
        :class="hidden.has(s.key) ? 'opacity-40' : ''"
        :aria-pressed="!hidden.has(s.key)"
        @click="toggleSeries(s.key)"
      >
        <span class="h-[3px] w-3 rounded-full" :style="{ backgroundColor: s.color }" aria-hidden="true" />
        <span class="text-gray-600 dark:text-dark-300">{{ s.label }}</span>
        <span class="font-medium tabular-nums text-gray-900 dark:text-gray-100">{{ formatUSD(s.total) }}</span>
      </button>
    </div>

    <div class="px-3 pb-4">
      <div v-if="loading && !hasData" class="px-2" aria-hidden="true">
        <div class="skeleton h-56 w-full rounded-lg" />
      </div>
      <div v-else-if="!hasData" class="flex h-56 items-center justify-center text-sm text-gray-500 dark:text-dark-400">
        {{ t('admin.dashboard.noDataAvailable') }}
      </div>
      <template v-else>
        <div v-show="view === 'chart'" class="relative h-56 transition-opacity" :class="loading ? 'opacity-50' : ''">
          <Line :data="lineData" :options="lineOptions" />
        </div>
        <div v-if="view === 'table'" class="max-h-56 overflow-auto px-2" :class="loading ? 'opacity-50' : ''">
          <table class="w-full text-xs">
            <thead class="sticky top-0 bg-white dark:bg-dark-900">
              <tr class="text-gray-500 dark:text-dark-400">
                <th scope="col" class="py-2 pr-3 text-left font-medium">{{ t('admin.dashboard.trend.time') }}</th>
                <th v-for="s in series" :key="s.key" scope="col" class="px-2 py-2 text-right font-medium">{{ s.label }}</th>
                <th scope="col" class="py-2 pl-2 text-right font-medium">{{ t('admin.dashboard.revenue.margin') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(point, index) in points" :key="point.date" class="border-t border-gray-100 dark:border-dark-800">
                <td class="whitespace-nowrap py-1.5 pr-3 tabular-nums text-gray-600 dark:text-dark-300">{{ point.date }}</td>
                <td
                  v-for="s in series"
                  :key="s.key"
                  class="whitespace-nowrap px-2 py-1.5 text-right tabular-nums text-gray-900 dark:text-gray-100"
                >{{ formatUSD(s.values[index]) }}</td>
                <td class="whitespace-nowrap py-1.5 pl-2 text-right tabular-nums text-gray-900 dark:text-gray-100">
                  {{ formatRatio(marginRatio(point.actual_cost, point.account_cost)) }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Chart as ChartJS, CategoryScale, Filler, LinearScale, LineElement, PointElement, Tooltip } from 'chart.js'
import { Line } from 'vue-chartjs'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import type { CostTrendPoint } from '@/types'
import {
  axisOptions,
  formatPercent,
  formatUSD,
  formatUSDCompact,
  htmlTooltip,
  marginRatio,
  seriesColor,
  useChartTheme,
  withAlpha
} from '@/components/charts/chartTheme'

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Tooltip, Filler)

const props = withDefaults(defineProps<{ points: CostTrendPoint[]; loading?: boolean }>(), { loading: false })

const { t } = useI18n()
const theme = useChartTheme()
const view = ref<'chart' | 'table'>('chart')
const hidden = ref<Set<string>>(new Set())

const viewOptions = computed(() => [
  { value: 'chart' as const, icon: 'chart' as const, title: t('admin.dashboard.trend.viewChart') },
  { value: 'table' as const, icon: 'document' as const, title: t('admin.dashboard.trend.viewTable') }
])

const num = (v: unknown) => {
  const n = Number(v)
  return Number.isFinite(n) ? n : 0
}

const hasData = computed(() => (props.points?.length ?? 0) > 0)

// 颜色跟随实体、按固定顺序分配：营收第 1 色，账号成本第 2 色，毛利第 3 色
const series = computed(() => {
  const data = props.points ?? []
  const th = theme.value
  const revenue = data.map((p) => num(p.actual_cost))
  const cost = data.map((p) => num(p.account_cost))
  const profit = data.map((_, i) => revenue[i] - cost[i])
  const sum = (values: number[]) => values.reduce((a, b) => a + b, 0)
  return [
    { key: 'revenue', label: t('admin.dashboard.revenue.revenue'), color: seriesColor(th, 0), values: revenue, total: sum(revenue) },
    { key: 'cost', label: t('admin.dashboard.revenue.accountCost'), color: seriesColor(th, 1), values: cost, total: sum(cost) },
    { key: 'profit', label: t('admin.dashboard.revenue.profit'), color: seriesColor(th, 2), values: profit, total: sum(profit) }
  ]
})

const visibleSeries = computed(() => series.value.filter((s) => !hidden.value.has(s.key)))

function toggleSeries(key: string) {
  const next = new Set(hidden.value)
  if (next.has(key)) next.delete(key)
  else if (series.value.length - next.size > 1) next.add(key)
  hidden.value = next
}

const formatRatio = (ratio: number | null) => (ratio === null ? '—' : formatPercent(ratio))

const marginText = computed(() => formatRatio(marginRatio(series.value[0].total, series.value[1].total)))

const HOUR_RE = /^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2})/
const DAY_RE = /^(\d{4})-(\d{2})-(\d{2})$/
const labels = computed(() => (props.points ?? []).map((p) => p.date))
const spansMultipleDays = computed(() => new Set(labels.value.map((d) => d.slice(0, 10))).size > 1)

function shortLabel(raw: string, index: number): string {
  const hour = HOUR_RE.exec(raw)
  if (hour) {
    const [, , mm, dd, hh, mi] = hour
    if (spansMultipleDays.value && (index === 0 || hh === '00')) return `${mm}-${dd} ${hh}:${mi}`
    return `${hh}:${mi}`
  }
  const day = DAY_RE.exec(raw)
  return day ? `${day[2]}-${day[3]}` : raw
}

const tooltipExternal = htmlTooltip((tooltip) => {
  const index = tooltip.dataPoints?.[0]?.dataIndex
  if (index === undefined) return null
  const point = props.points[index]
  if (!point) return null
  return {
    title: point.date,
    rows: visibleSeries.value.map((s) => ({ color: s.color, label: s.label, value: formatUSD(s.values[index]), key: 'line' as const })),
    footer: [`${t('admin.dashboard.revenue.margin')} ${formatRatio(marginRatio(point.actual_cost, point.account_cost))}`]
  }
})

const prefersReducedMotion =
  typeof window !== 'undefined' && typeof window.matchMedia === 'function'
    ? window.matchMedia('(prefers-reduced-motion: reduce)').matches
    : false

const lineData = computed(() => ({
  labels: labels.value,
  datasets: visibleSeries.value.map((s) => ({
    label: s.label,
    data: s.values,
    borderColor: s.color,
    backgroundColor: withAlpha(s.color, 0.1),
    // 只有营收带 10% 面积底色，成本与毛利保持线条
    fill: s.key === 'revenue' ? 'origin' : false,
    borderWidth: 2,
    tension: 0.3,
    pointRadius: 0,
    pointHoverRadius: 4,
    pointHoverBorderWidth: 2,
    pointHoverBorderColor: theme.value.surface,
    pointHoverBackgroundColor: s.color,
    borderCapStyle: 'round' as const,
    borderJoinStyle: 'round' as const
  }))
}))

const lineOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  animation: prefersReducedMotion ? (false as const) : { duration: 250 },
  interaction: { mode: 'index' as const, intersect: false },
  plugins: { legend: { display: false }, tooltip: { enabled: false, external: tooltipExternal } },
  scales: {
    x: {
      ...axisOptions(theme.value, { grid: false }),
      ticks: {
        ...axisOptions(theme.value).ticks,
        maxRotation: 0,
        autoSkip: true,
        maxTicksLimit: 8,
        callback: (_value: string | number, index: number) => shortLabel(labels.value[index] ?? '', index)
      }
    },
    // 单一金额轴；毛利可能为负，零线随数据自然出现
    y: { ...axisOptions(theme.value, { format: formatUSDCompact }), beginAtZero: true }
  }
}))
</script>
