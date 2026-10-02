<template>
  <section class="card flex flex-col">
    <header class="card-section-header">
      <div class="min-w-0">
        <h3 class="card-section-title">{{ t('admin.dashboard.heatmap.title') }}</h3>
        <p class="card-section-subtitle flex flex-wrap items-center gap-x-3 gap-y-1">
          <span>{{ layout === 'weekday' ? t('admin.dashboard.heatmap.subtitleWeekday') : t('admin.dashboard.heatmap.subtitleDate') }}</span>
          <span v-if="peak">
            {{ t('admin.dashboard.heatmap.peak') }}
            <span class="font-medium tabular-nums text-gray-700 dark:text-gray-200">{{ peak }}</span>
          </span>
        </p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <SegmentedControl v-model="layout" size="sm" :options="layoutOptions" :aria-label="t('admin.dashboard.heatmap.layout')" />
        <SegmentedControl v-model="metric" size="sm" :options="metricOptions" :aria-label="t('admin.dashboard.heatmap.metric')" />
        <SegmentedControl v-model="view" size="sm" :options="viewOptions" :aria-label="t('admin.dashboard.trend.viewTable')" />
      </div>
    </header>

    <div class="px-5 pb-4">
      <div v-if="loading && !hasData" class="skeleton h-56 w-full rounded-lg" aria-hidden="true" />
      <div v-else-if="!hasData" class="flex h-56 items-center justify-center text-sm text-gray-500 dark:text-dark-400">
        {{ t('admin.dashboard.noDataAvailable') }}
      </div>
      <template v-else>
        <div
          v-show="view === 'chart'"
          ref="gridRef"
          class="relative transition-opacity"
          :class="loading ? 'opacity-50' : ''"
          aria-hidden="true"
          @pointerleave="hovered = null"
        >
          <div class="grid gap-[2px]" :style="gridStyle">
            <span />
            <span
              v-for="hour in 24"
              :key="`h${hour}`"
              class="pb-1 text-center text-[10px] leading-3 tabular-nums text-gray-400 dark:text-dark-500"
            >{{ (hour - 1) % 3 === 0 ? hour - 1 : '' }}</span>
            <template v-for="(row, rowIndex) in matrix.rows" :key="row.key">
              <span
                class="flex items-center justify-end truncate pr-2 text-[10px] tabular-nums text-gray-500 dark:text-dark-400"
                :style="{ height: `${cellHeight}px` }"
              >{{ row.label }}</span>
              <span
                v-for="(cell, hour) in row.cells"
                :key="hour"
                class="rounded-[3px] transition-[filter] duration-100"
                :class="[
                  isHovered(rowIndex, hour) ? 'brightness-110 ring-1 ring-gray-900/40 dark:ring-white/50' : '',
                  cell.future ? 'opacity-40' : ''
                ]"
                :style="{ height: `${cellHeight}px`, backgroundColor: cellColor(cell.value) }"
                @pointerenter="cell.future ? (hovered = null) : onEnter($event, rowIndex, hour)"
              />
            </template>
          </div>

          <!-- 提示框：数值在前、名称在后；标签来自接口数据，只用文本插值 -->
          <div
            v-if="hovered"
            class="chart-tooltip"
            style="opacity: 1"
            :style="{ left: `${hovered.left}px`, top: `${hovered.top}px` }"
          >
            <div class="chart-tooltip-title">{{ hovered.title }}</div>
            <div class="chart-tooltip-row">
              <span class="chart-tooltip-key-rect" :style="{ backgroundColor: cellColor(hovered.cell.value) }" />
              <span class="chart-tooltip-value">{{ formatMetric(hovered.cell.value) }}</span>
              <span class="chart-tooltip-label">{{ metricLabel }}</span>
            </div>
            <div class="chart-tooltip-footer">
              <div>{{ t('admin.dashboard.heatmap.requests') }} {{ formatCompact(hovered.cell.requests) }}</div>
              <div>{{ t('admin.dashboard.heatmap.tokens') }} {{ formatCompact(hovered.cell.tokens) }}</div>
              <div>{{ t('admin.dashboard.heatmap.cost') }} {{ formatUSD(hovered.cell.cost) }}</div>
            </div>
          </div>

          <!-- 顺序色阶图例 -->
          <div class="mt-3 flex flex-wrap items-center justify-between gap-2 text-[11px] text-gray-500 dark:text-dark-400">
            <span v-if="matrix.truncated">{{ t('admin.dashboard.heatmap.truncated', { days: MAX_DATE_ROWS }) }}</span>
            <span v-else />
            <span class="inline-flex items-center gap-1.5">
              <span>{{ t('admin.dashboard.heatmap.less') }}</span>
              <span class="h-2.5 w-2.5 rounded-[3px]" :style="{ backgroundColor: theme.heatEmpty }" />
              <span
                v-for="color in theme.heat"
                :key="color"
                class="h-2.5 w-2.5 rounded-[3px]"
                :style="{ backgroundColor: color }"
              />
              <span>{{ t('admin.dashboard.heatmap.more') }}</span>
              <span class="ml-1 tabular-nums text-gray-700 dark:text-gray-300">{{ t('admin.dashboard.heatmap.max', { value: formatMetric(matrix.max) }) }}</span>
            </span>
          </div>
        </div>

        <!-- 数据表视图：热力图的无障碍等价形式 -->
        <div v-if="view === 'table'" class="max-h-72 overflow-auto" :class="loading ? 'opacity-50' : ''">
          <table class="w-full text-[11px]">
            <caption class="sr-only">{{ t('admin.dashboard.heatmap.title') }} · {{ metricLabel }}</caption>
            <thead class="sticky top-0 bg-white dark:bg-dark-900">
              <tr class="text-gray-500 dark:text-dark-400">
                <th scope="col" class="py-1.5 pr-2 text-left font-medium" />
                <th v-for="hour in 24" :key="hour" scope="col" class="px-1 py-1.5 text-right font-medium tabular-nums">{{ hour - 1 }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in matrix.rows" :key="row.key" class="border-t border-gray-100 dark:border-dark-800">
                <th scope="row" class="whitespace-nowrap py-1 pr-2 text-left font-normal text-gray-600 dark:text-dark-300">{{ row.label }}</th>
                <td
                  v-for="(cell, hour) in row.cells"
                  :key="hour"
                  class="whitespace-nowrap px-1 py-1 text-right tabular-nums text-gray-900 dark:text-gray-100"
                >{{ cell.value > 0 ? formatMetricCompact(cell.value) : '' }}</td>
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
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import type { TrendDataPoint } from '@/types'
import { formatCompact, formatUSD, formatUSDCompact, useChartTheme } from '@/components/charts/chartTheme'

type Layout = 'date' | 'weekday'
type Metric = 'requests' | 'tokens' | 'cost'

interface Cell {
  value: number
  requests: number
  tokens: number
  cost: number
  /** 尚未到来的小时：日期视图淡化显示，星期视图不计入日均 */
  future?: boolean
}

interface Row {
  key: string
  label: string
  /** 星期视图里的完整名称（提示框标题用） */
  title: string
  cells: Cell[]
}

const props = withDefaults(
  defineProps<{
    /** 小时粒度的趋势点，date 形如 "2026-10-02 14:00" */
    points: TrendDataPoint[]
    startDate?: string
    endDate?: string
    loading?: boolean
  }>(),
  { startDate: '', endDate: '', loading: false }
)

/** 日期视图最多展示的天数，更长的区间只保留最近的这些天（星期视图仍用完整区间）。 */
const MAX_DATE_ROWS = 31
const LEVELS = 5

const { t, locale } = useI18n()
const theme = useChartTheme()

const layout = ref<Layout>('date')
const metric = ref<Metric>('requests')
const view = ref<'chart' | 'table'>('chart')
const gridRef = ref<HTMLElement | null>(null)
const hovered = ref<{ row: number; hour: number; title: string; cell: Cell; left: number; top: number } | null>(null)

const layoutOptions = computed(() => [
  { value: 'date' as Layout, label: t('admin.dashboard.heatmap.byDate') },
  { value: 'weekday' as Layout, label: t('admin.dashboard.heatmap.byWeekday') }
])
const metricOptions = computed(() => [
  { value: 'requests' as Metric, label: t('admin.dashboard.trend.metricRequests') },
  { value: 'tokens' as Metric, label: t('admin.dashboard.trend.metricTokens') },
  { value: 'cost' as Metric, label: t('admin.dashboard.trend.metricCost') }
])
const viewOptions = computed(() => [
  { value: 'chart' as const, icon: 'chart' as const, title: t('admin.dashboard.trend.viewChart') },
  { value: 'table' as const, icon: 'document' as const, title: t('admin.dashboard.trend.viewTable') }
])

const metricLabel = computed(() => {
  const base =
    metric.value === 'requests'
      ? t('admin.dashboard.heatmap.requests')
      : metric.value === 'tokens'
        ? t('admin.dashboard.heatmap.tokens')
        : t('admin.dashboard.heatmap.cost')
  return layout.value === 'weekday' ? t('admin.dashboard.heatmap.dailyAverage', { metric: base }) : base
})

const num = (v: unknown) => {
  const n = Number(v)
  return Number.isFinite(n) ? n : 0
}

const HOUR_RE = /^(\d{4})-(\d{2})-(\d{2})[ T](\d{2})/
const DAY_RE = /^(\d{4})-(\d{2})-(\d{2})$/

/** 日历日按 UTC 解析，只取年月日和星期，与浏览器时区无关（时间标签由服务端按其时区格式化）。 */
const parseDay = (value: string): Date | null => {
  const m = DAY_RE.exec(value.slice(0, 10))
  if (!m) return null
  const d = new Date(Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3])))
  return Number.isNaN(d.getTime()) ? null : d
}
const dayKey = (d: Date) => d.toISOString().slice(0, 10)
/** 周一为第一天 */
const weekdayIndex = (d: Date) => (d.getUTCDay() + 6) % 7

const weekdayFormatter = computed(() => new Intl.DateTimeFormat(locale.value, { weekday: 'short', timeZone: 'UTC' }))
const weekdayLongFormatter = computed(() => new Intl.DateTimeFormat(locale.value, { weekday: 'long', timeZone: 'UTC' }))

const hasData = computed(() => (props.points?.length ?? 0) > 0)

const pad2 = (n: number) => String(n).padStart(2, '0')
/** 当前小时的 "YYYY-MM-DD HH" 键；与时间筛选一样按浏览器本地日期计 */
const nowHourKey = () => {
  const d = new Date()
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ${pad2(d.getHours())}`
}

/** 按日期聚合的完整网格（含零值日期），供两种视图共用 */
const byDate = computed(() => {
  const buckets = new Map<string, Cell[]>()
  const emptyCells = (): Cell[] => Array.from({ length: 24 }, () => ({ value: 0, requests: 0, tokens: 0, cost: 0 }))
  for (const p of props.points ?? []) {
    const m = HOUR_RE.exec(p.date)
    if (!m) continue
    const key = `${m[1]}-${m[2]}-${m[3]}`
    const hour = Number(m[4])
    if (hour < 0 || hour > 23) continue
    if (!buckets.has(key)) buckets.set(key, emptyCells())
    const cell = buckets.get(key)![hour]
    cell.requests += num(p.requests)
    cell.tokens +=
      num(p.total_tokens) ||
      num(p.input_tokens) + num(p.output_tokens) + num(p.cache_creation_tokens) + num(p.cache_read_tokens)
    cell.cost += num(p.actual_cost)
  }

  // 用筛选区间补齐没有用量的日期，空白日也要占一行
  const keys = [...buckets.keys()].sort()
  const start = parseDay(props.startDate) ?? (keys.length ? parseDay(keys[0]) : null)
  const end = parseDay(props.endDate) ?? (keys.length ? parseDay(keys[keys.length - 1]) : null)
  const days: Date[] = []
  if (start && end && start <= end) {
    for (let d = new Date(start); d <= end && days.length < 400; d = new Date(d.getTime() + 86_400_000)) days.push(d)
  }
  for (const key of keys) {
    if (!days.some((d) => dayKey(d) === key)) {
      const d = parseDay(key)
      if (d) days.push(d)
    }
  }
  days.sort((a, b) => a.getTime() - b.getTime())
  const nowKey = nowHourKey()
  return days.map((d) => {
    const key = dayKey(d)
    const cells = buckets.get(key) ?? emptyCells()
    cells.forEach((cell, hour) => {
      cell.future = cell.requests === 0 && `${key} ${pad2(hour)}` > nowKey
    })
    return { day: d, cells }
  })
})

const valueOf = (cell: Cell) => (metric.value === 'requests' ? cell.requests : metric.value === 'tokens' ? cell.tokens : cell.cost)

const matrix = computed(() => {
  const all = byDate.value
  let rows: Row[]
  let truncated = false
  if (layout.value === 'weekday') {
    // 星期 × 小时：同一星期几的日均值（按区间内该星期几出现的天数平均，包含零值日）
    const sums = Array.from({ length: 7 }, () => Array.from({ length: 24 }, () => ({ value: 0, requests: 0, tokens: 0, cost: 0 })))
    // 分母按（星期几, 小时）各自计数：今天还没到的小时不算作 0，否则会压低当天对应星期的日均
    const counts = Array.from({ length: 7 }, () => Array<number>(24).fill(0))
    for (const { day, cells } of all) {
      const w = weekdayIndex(day)
      cells.forEach((cell, hour) => {
        if (cell.future) return
        counts[w][hour]++
        sums[w][hour].requests += cell.requests
        sums[w][hour].tokens += cell.tokens
        sums[w][hour].cost += cell.cost
      })
    }
    rows = sums.map((cells, w) => {
      // 2024-01-01 是周一，借它取本地化星期名
      const ref = new Date(Date.UTC(2024, 0, 1 + w))
      return {
        key: `w${w}`,
        label: weekdayFormatter.value.format(ref),
        title: weekdayLongFormatter.value.format(ref),
        cells: cells.map((c, hour) => {
          const n = counts[w][hour]
          const avg: Cell = n > 0 ? { value: 0, requests: c.requests / n, tokens: c.tokens / n, cost: c.cost / n } : { value: 0, requests: 0, tokens: 0, cost: 0, future: true }
          avg.value = valueOf(avg)
          return avg
        })
      }
    })
  } else {
    const visible = all.length > MAX_DATE_ROWS ? all.slice(-MAX_DATE_ROWS) : all
    truncated = visible.length < all.length
    rows = visible.map(({ day, cells }) => {
      const key = dayKey(day)
      return {
        key,
        label: `${key.slice(5)} ${weekdayFormatter.value.format(day)}`,
        title: `${key} ${weekdayLongFormatter.value.format(day)}`,
        cells: cells.map((c) => ({ ...c, value: valueOf(c) }))
      }
    })
  }
  let max = 0
  for (const row of rows) for (const cell of row.cells) max = Math.max(max, cell.value)
  return { rows, max, truncated }
})

/** 按最大值线性分 5 档：小时级汇总很少有极端离群值，线性分档最能看出昼夜起伏 */
const levelOf = (value: number) => {
  const max = matrix.value.max
  if (value <= 0 || max <= 0) return 0
  return Math.min(LEVELS, Math.max(1, Math.ceil((value / max) * LEVELS)))
}

/** 行少时把格子拉高，让网格大致占满卡片；行多时回落到 14px */
const cellHeight = computed(() => Math.round(Math.min(32, Math.max(14, 200 / Math.max(matrix.value.rows.length, 1)))))

const cellColor = (value: number) => {
  const level = levelOf(value)
  return level === 0 ? theme.value.heatEmpty : theme.value.heat[level - 1]
}

const formatMetric = (value: number) => (metric.value === 'cost' ? formatUSD(value) : formatCompact(value))
const formatMetricCompact = (value: number) => (metric.value === 'cost' ? formatUSDCompact(value) : formatCompact(value))

const gridStyle = computed(() => ({ gridTemplateColumns: `4.75rem repeat(24, minmax(0, 1fr))` }))

const hourRange = (hour: number) => `${String(hour).padStart(2, '0')}:00–${String((hour + 1) % 24).padStart(2, '0')}:00`

/** 只标出最高的一格（选择性直接标注，不在每格写数字） */
const peak = computed(() => {
  let best: { row: Row; hour: number; value: number } | null = null
  for (const row of matrix.value.rows) {
    row.cells.forEach((cell, hour) => {
      if (cell.value > 0 && (!best || cell.value > best.value)) best = { row, hour, value: cell.value }
    })
  }
  if (!best) return ''
  const { row, hour, value } = best as { row: Row; hour: number; value: number }
  return `${row.title} ${hourRange(hour)} · ${formatMetric(value)}`
})

const isHovered = (row: number, hour: number) => hovered.value?.row === row && hovered.value?.hour === hour

function onEnter(event: PointerEvent, row: number, hour: number) {
  const container = gridRef.value
  const target = event.currentTarget as HTMLElement | null
  if (!container || !target) return
  const box = container.getBoundingClientRect()
  const cellBox = target.getBoundingClientRect()
  const tooltipWidth = 190
  let left = cellBox.right - box.left + 8
  if (left + tooltipWidth > box.width) left = cellBox.left - box.left - tooltipWidth - 8
  const rowData = matrix.value.rows[row]
  hovered.value = {
    row,
    hour,
    title: `${rowData.title} ${hourRange(hour)}`,
    cell: rowData.cells[hour],
    left: Math.max(0, left),
    top: Math.max(0, cellBox.top - box.top - 24)
  }
}
</script>
