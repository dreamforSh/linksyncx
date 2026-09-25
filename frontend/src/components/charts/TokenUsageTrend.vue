<template>
  <section class="card flex flex-col">
    <header class="card-section-header">
      <div class="min-w-0">
        <h3 class="card-section-title">{{ title || t('admin.dashboard.trend.title') }}</h3>
        <p class="card-section-subtitle flex flex-wrap items-center gap-x-3 gap-y-1">
          <span>
            {{ t('admin.dashboard.trend.total') }}
            <span class="font-medium tabular-nums text-gray-700 dark:text-gray-200">{{ totalText }}</span>
          </span>
          <span v-if="metric === 'tokens' && hasData">
            {{ t('admin.dashboard.trend.cacheHitRate') }}
            <span class="font-medium tabular-nums text-gray-700 dark:text-gray-200">{{ cacheHitRateText }}</span>
          </span>
        </p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <SegmentedControl
          v-model="metric"
          size="sm"
          :options="metricOptions"
          :aria-label="t('admin.dashboard.trend.title')"
        />
        <SegmentedControl
          v-model="view"
          size="sm"
          :options="viewOptions"
          :aria-label="t('admin.dashboard.trend.viewTable')"
        />
      </div>
    </header>

    <!-- 图例：≥2 个序列时始终显示；点击可隐藏/显示该序列 -->
    <div v-if="series.length > 1 && hasData" class="flex flex-wrap items-center gap-x-4 gap-y-1.5 px-5 pb-2">
      <button
        v-for="s in series"
        :key="s.key"
        type="button"
        class="inline-flex items-center gap-1.5 rounded text-xs transition-opacity focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/50"
        :class="hidden.has(s.key) ? 'opacity-40' : ''"
        :aria-pressed="!hidden.has(s.key)"
        @click="toggleSeries(s.key)"
      >
        <span
          :class="s.kind === 'line' ? 'h-[3px] w-3 rounded-full' : 'h-2.5 w-2.5 rounded-sm'"
          :style="{ backgroundColor: s.color }"
          aria-hidden="true"
        />
        <span class="text-gray-600 dark:text-dark-300">{{ s.label }}</span>
        <span class="font-medium tabular-nums text-gray-900 dark:text-gray-100">{{ s.totalText }}</span>
      </button>
    </div>

    <div class="px-3 pb-4">
      <div v-if="loading && !hasData" class="px-2" aria-hidden="true">
        <div class="skeleton h-60 w-full rounded-lg" />
      </div>
      <div
        v-else-if="!hasData"
        class="flex h-60 items-center justify-center text-sm text-gray-500 dark:text-dark-400"
      >
        {{ t('admin.dashboard.noDataAvailable') }}
      </div>
      <template v-else>
        <div
          v-show="view === 'chart'"
          class="relative h-60 transition-opacity"
          :class="loading ? 'opacity-50' : ''"
        >
          <Bar v-if="metric === 'tokens'" :data="barData" :options="barOptions" />
          <Line v-else :data="lineData" :options="lineOptions" />
        </div>
        <!-- 数据表视图：图表的无障碍等价形式 -->
        <div v-if="view === 'table'" class="max-h-60 overflow-auto px-2" :class="loading ? 'opacity-50' : ''">
          <table class="w-full text-xs">
            <thead class="sticky top-0 bg-white dark:bg-dark-900">
              <tr class="text-gray-500 dark:text-dark-400">
                <th scope="col" class="py-2 pr-3 text-left font-medium">{{ t('admin.dashboard.trend.time') }}</th>
                <th v-for="s in series" :key="s.key" scope="col" class="px-2 py-2 text-right font-medium">{{ s.label }}</th>
                <th v-if="metric === 'tokens'" scope="col" class="py-2 pl-2 text-right font-medium">{{ t('admin.dashboard.trend.total') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="(point, index) in trendData"
                :key="point.date"
                class="border-t border-gray-100 dark:border-dark-800"
              >
                <td class="whitespace-nowrap py-1.5 pr-3 tabular-nums text-gray-600 dark:text-dark-300">{{ point.date }}</td>
                <td
                  v-for="s in series"
                  :key="s.key"
                  class="whitespace-nowrap px-2 py-1.5 text-right tabular-nums text-gray-900 dark:text-gray-100"
                >
                  {{ formatValue(s.values[index]) }}
                </td>
                <td v-if="metric === 'tokens'" class="whitespace-nowrap py-1.5 pl-2 text-right font-medium tabular-nums text-gray-900 dark:text-gray-100">
                  {{ formatCompact(point.total_tokens || tokenSum(point)) }}
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
import {
  Chart as ChartJS,
  BarElement,
  CategoryScale,
  Filler,
  LinearScale,
  LineElement,
  PointElement,
  Tooltip
} from 'chart.js'
import { Bar, Line } from 'vue-chartjs'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import type { TrendDataPoint } from '@/types'
import {
  axisOptions,
  formatCompact,
  formatPercent,
  formatUSD,
  formatUSDCompact,
  htmlTooltip,
  seriesColor,
  useChartTheme,
  withAlpha,
  type HtmlTooltipRow
} from './chartTheme'

ChartJS.register(CategoryScale, LinearScale, BarElement, PointElement, LineElement, Tooltip, Filler)

type Metric = 'tokens' | 'requests' | 'cost'

const props = withDefaults(
  defineProps<{
    trendData: TrendDataPoint[]
    loading?: boolean
    title?: string
    defaultMetric?: Metric
  }>(),
  {
    loading: false,
    title: '',
    defaultMetric: 'tokens'
  }
)

const { t } = useI18n()
const theme = useChartTheme()

const metric = ref<Metric>(props.defaultMetric)
const view = ref<'chart' | 'table'>('chart')
const hidden = ref<Set<string>>(new Set())

const metricOptions = computed(() => [
  { value: 'tokens' as Metric, label: t('admin.dashboard.trend.metricTokens') },
  { value: 'requests' as Metric, label: t('admin.dashboard.trend.metricRequests') },
  { value: 'cost' as Metric, label: t('admin.dashboard.trend.metricCost') }
])

const viewOptions = computed(() => [
  { value: 'chart' as const, icon: 'chart' as const, title: t('admin.dashboard.trend.viewChart') },
  { value: 'table' as const, icon: 'document' as const, title: t('admin.dashboard.trend.viewTable') }
])

const hasData = computed(() => (props.trendData?.length ?? 0) > 0)

const num = (v: unknown) => {
  const n = Number(v)
  return Number.isFinite(n) ? n : 0
}

const tokenSum = (p: TrendDataPoint) =>
  num(p.input_tokens) + num(p.output_tokens) + num(p.cache_creation_tokens) + num(p.cache_read_tokens)

interface SeriesDef {
  key: string
  label: string
  color: string
  kind: 'bar' | 'line'
  values: number[]
  totalText: string
}

// 序列定义：颜色按实体固定分配（输入永远是第 1 色），不随显隐或排序重新着色
const series = computed<SeriesDef[]>(() => {
  const data = props.trendData ?? []
  const th = theme.value
  const sum = (values: number[]) => values.reduce((a, b) => a + b, 0)
  if (metric.value === 'tokens') {
    // 堆叠顺序即分色顺序：通常占比最大的缓存读取压在最底层并使用第 1 色（品牌青），
    // 让大面积色块保持沉稳；较小的输入/输出/缓存写入依次叠在上方
    const defs: Array<[string, string, keyof TrendDataPoint]> = [
      ['cacheRead', t('admin.dashboard.trend.cacheRead'), 'cache_read_tokens'],
      ['input', t('admin.dashboard.trend.input'), 'input_tokens'],
      ['output', t('admin.dashboard.trend.output'), 'output_tokens'],
      ['cacheCreation', t('admin.dashboard.trend.cacheCreation'), 'cache_creation_tokens']
    ]
    return defs.map(([key, label, field], index) => {
      const values = data.map((p) => num(p[field]))
      return { key, label, color: seriesColor(th, index), kind: 'bar' as const, values, totalText: formatCompact(sum(values)) }
    })
  }
  if (metric.value === 'requests') {
    const values = data.map((p) => num(p.requests))
    return [{ key: 'requests', label: t('admin.dashboard.trend.requests'), color: th.accent, kind: 'line' as const, values, totalText: formatCompact(sum(values)) }]
  }
  // 费用：实际扣费为主，标准计费作为灰色上下文（强调式，而非两种分类色）
  const actual = data.map((p) => num(p.actual_cost))
  const standard = data.map((p) => num(p.cost))
  return [
    { key: 'actual', label: t('admin.dashboard.trend.actualCost'), color: th.accent, kind: 'line' as const, values: actual, totalText: formatUSD(sum(actual)) },
    { key: 'standard', label: t('admin.dashboard.trend.standardCost'), color: th.muted, kind: 'line' as const, values: standard, totalText: formatUSD(sum(standard)) }
  ]
})

const visibleSeries = computed(() => series.value.filter((s) => !hidden.value.has(s.key)))

function toggleSeries(key: string) {
  const next = new Set(hidden.value)
  if (next.has(key)) next.delete(key)
  else if (series.value.length - next.size > 1) next.add(key) // 至少保留一个序列
  hidden.value = next
}

const formatValue = (value: number) => (metric.value === 'cost' ? formatUSD(value) : formatCompact(value))

const totalText = computed(() => {
  const data = props.trendData ?? []
  if (metric.value === 'tokens') return formatCompact(data.reduce((a, p) => a + (num(p.total_tokens) || tokenSum(p)), 0))
  if (metric.value === 'requests') return formatCompact(data.reduce((a, p) => a + num(p.requests), 0))
  return formatUSD(data.reduce((a, p) => a + num(p.actual_cost), 0))
})

const cacheHitRate = (points: TrendDataPoint[]) => {
  let read = 0
  let prompt = 0
  for (const p of points) {
    read += num(p.cache_read_tokens)
    prompt += num(p.input_tokens) + num(p.cache_read_tokens) + num(p.cache_creation_tokens)
  }
  return prompt > 0 ? read / prompt : 0
}

const cacheHitRateText = computed(() => formatPercent(cacheHitRate(props.trendData ?? [])))

// ==================== X 轴刻度 ====================
const HOUR_RE = /^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2})/
const DAY_RE = /^(\d{4})-(\d{2})-(\d{2})$/

const spansMultipleDays = computed(() => {
  const days = new Set((props.trendData ?? []).map((p) => p.date.slice(0, 10)))
  return days.size > 1
})

function shortLabel(raw: string, index: number): string {
  const hour = HOUR_RE.exec(raw)
  if (hour) {
    const [, , mm, dd, hh, mi] = hour
    if (spansMultipleDays.value && (index === 0 || hh === '00')) return `${mm}-${dd} ${hh}:${mi}`
    return `${hh}:${mi}`
  }
  const day = DAY_RE.exec(raw)
  if (day) return `${day[2]}-${day[3]}`
  return raw
}

const labels = computed(() => (props.trendData ?? []).map((p) => p.date))

// ==================== 提示框 ====================
const tooltipExternal = htmlTooltip((tooltip) => {
  const index = tooltip.dataPoints?.[0]?.dataIndex
  if (index === undefined) return null
  const point = props.trendData[index]
  if (!point) return null
  const rows: HtmlTooltipRow[] = visibleSeries.value.map((s) => ({
    color: s.color,
    label: s.label,
    value: formatValue(s.values[index]),
    key: s.kind === 'bar' ? 'rect' : 'line'
  }))
  const footer: string[] = []
  if (metric.value === 'tokens') {
    footer.push(`${t('admin.dashboard.trend.total')} ${formatCompact(num(point.total_tokens) || tokenSum(point))}`)
    footer.push(`${t('admin.dashboard.trend.cacheHitRate')} ${formatPercent(cacheHitRate([point]))}`)
  } else if (metric.value === 'requests') {
    footer.push(`${t('admin.dashboard.trend.actualCost')} ${formatUSD(point.actual_cost)}`)
  }
  return { title: point.date, rows, footer }
})

const prefersReducedMotion =
  typeof window !== 'undefined' && typeof window.matchMedia === 'function'
    ? window.matchMedia('(prefers-reduced-motion: reduce)').matches
    : false

const baseOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  animation: prefersReducedMotion ? (false as const) : { duration: 250 },
  interaction: { mode: 'index' as const, intersect: false },
  plugins: {
    legend: { display: false },
    tooltip: { enabled: false, external: tooltipExternal }
  }
}))

const xAxis = computed(() => ({
  ...axisOptions(theme.value, { grid: false }),
  ticks: {
    ...axisOptions(theme.value).ticks,
    maxRotation: 0,
    autoSkip: true,
    maxTicksLimit: 8,
    callback: (_value: string | number, index: number) => shortLabel(labels.value[index] ?? '', index)
  }
}))

// ==================== Token：堆叠柱 ====================
const barData = computed(() => {
  const vis = visibleSeries.value
  return {
    labels: labels.value,
    datasets: vis.map((s, i) => ({
      label: s.label,
      data: s.values,
      backgroundColor: s.color,
      hoverBackgroundColor: s.color,
      // 2px 底色间隙分隔堆叠段；只有最顶层的段做 4px 圆角
      borderColor: theme.value.surface,
      borderWidth: { top: 2, right: 0, bottom: 0, left: 0 },
      borderRadius: i === vis.length - 1 ? { topLeft: 4, topRight: 4, bottomLeft: 0, bottomRight: 0 } : 0,
      borderSkipped: false as const,
      maxBarThickness: 24,
      categoryPercentage: 0.8,
      barPercentage: 0.9
    }))
  }
})

const barOptions = computed(() => ({
  ...baseOptions.value,
  scales: {
    x: { ...xAxis.value, stacked: true },
    y: { ...axisOptions(theme.value, { format: formatCompact }), stacked: true, beginAtZero: true }
  }
}))

// ==================== 请求 / 费用：折线 ====================
const lineData = computed(() => ({
  labels: labels.value,
  datasets: visibleSeries.value.map((s, i) => ({
    label: s.label,
    data: s.values,
    borderColor: s.color,
    backgroundColor: withAlpha(s.color, 0.1),
    // 只有主序列带 10% 面积底色，上下文序列保持线条
    fill: i === 0 ? 'origin' : false,
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
  ...baseOptions.value,
  scales: {
    x: xAxis.value,
    y: {
      ...axisOptions(theme.value, { format: metric.value === 'cost' ? formatUSDCompact : formatCompact }),
      beginAtZero: true
    }
  }
}))
</script>
