import { computed } from 'vue'
import type { Chart, TooltipModel } from 'chart.js'
import { useDarkMode } from '@/composables/useDarkMode'

/**
 * 图表配色与图表外框（网格、坐标轴、文字）的唯一来源。
 *
 * 分类色按固定顺序分配、不循环：第 1 位是品牌青色，其余取自经过校验的参考色板。
 * 这组顺序已用 dataviz validate_palette.js 在实际卡片底色上校验通过：
 *   浅色（#ffffff）：相邻色盲 ΔE ≥ 10.7，常规视觉 ΔE ≥ 19.6；黄、品红对比度 < 3:1，
 *                    因此所有多序列图都必须带图例 + 数据表视图作为补偿通道。
 *   深色（#0f172a）：全部检查 PASS。
 * 换色前请重新跑校验脚本，不要凭肉眼调整。
 */
export const CATEGORICAL_LIGHT = [
  '#0d9488', // 青（品牌 primary-600）
  '#eb6834', // 橙
  '#2a78d6', // 蓝
  '#eda100', // 黄
  '#e87ba4', // 品红
  '#008300', // 绿
  '#4a3aa7', // 紫
  '#e34948' // 红
] as const

export const CATEGORICAL_DARK = [
  '#0d9488',
  '#d95926',
  '#3987e5',
  '#c98500',
  '#d55181',
  '#008300',
  '#9085e9',
  '#e66767'
] as const

/**
 * 热力图顺序色阶：单一青色，按数量由浅到深；深色模式翻转锚点（近零贴近深色底）。
 * 已用 validate_palette.js --ordinal 校验：亮度单调、相邻 ΔL ≥ 0.06、色相单一。
 * 最浅一档允许贴近底色（顺序色阶里近零值本应退后），所以不套用浅端 2:1 对比度；
 * 无数据的格子用独立的灰色，数值始终可在提示框与数据表视图读到。
 */
export const HEAT_LIGHT = ['#99f6e4', '#2dd4bf', '#0d9488', '#0f766e', '#134e4a'] as const
export const HEAT_DARK = ['#134e4a', '#0f766e', '#14b8a6', '#5eead4', '#ccfbf1'] as const

/** 状态色：只表达好/坏，永不当作序列色；使用时必须配图标 + 文字。 */
export const STATUS_COLORS = {
  good: '#0ca30c',
  warning: '#fab219',
  serious: '#ec835a',
  critical: '#d03b3b'
} as const

export interface ChartTheme {
  isDark: boolean
  series: readonly string[]
  /** 弱化色：“其他”、上下文序列、迷你趋势线 */
  muted: string
  accent: string
  surface: string
  grid: string
  axis: string
  tick: string
  textPrimary: string
  textSecondary: string
  /** 热力图顺序色阶（低 → 高）与无数据格子 */
  heat: readonly string[]
  heatEmpty: string
}

const LIGHT: ChartTheme = {
  isDark: false,
  series: CATEGORICAL_LIGHT,
  muted: '#94a3b8',
  accent: '#0d9488',
  surface: '#ffffff',
  grid: '#f0f2f5',
  axis: '#e5e7eb',
  tick: '#6b7280',
  textPrimary: '#111827',
  textSecondary: '#4b5563',
  heat: HEAT_LIGHT,
  heatEmpty: '#eef2f6'
}

const DARK: ChartTheme = {
  isDark: true,
  series: CATEGORICAL_DARK,
  muted: '#64748b',
  accent: '#14b8a6',
  surface: '#0f172a',
  grid: '#1e293b',
  axis: '#334155',
  tick: '#94a3b8',
  textPrimary: '#f8fafc',
  textSecondary: '#cbd5e1',
  heat: HEAT_DARK,
  heatEmpty: '#1e293b'
}

export function useChartTheme() {
  const isDark = useDarkMode()
  return computed<ChartTheme>(() => (isDark.value ? DARK : LIGHT))
}

/** 分类色按实体下标取色；超过 8 个的尾部应折叠为“其他”，这里兜底返回弱化色而不是循环。 */
export function seriesColor(theme: ChartTheme, index: number): string {
  return theme.series[index] ?? theme.muted
}

/** 半透明填充（面积图底色等），hex 需为 #rrggbb */
export function withAlpha(hex: string, alpha: number): string {
  const a = Math.round(Math.min(Math.max(alpha, 0), 1) * 255)
    .toString(16)
    .padStart(2, '0')
  return `${hex}${a}`
}

// ==================== 数字格式 ====================

const toFinite = (value: unknown): number => {
  const n = Number(value)
  return Number.isFinite(n) ? n : 0
}

/** 自动缩写：1,284 / 12.9K / 129K / 1.29M / 4.2B（约 3 位有效数字） */
export function formatCompact(value: unknown): string {
  const v = toFinite(value)
  const abs = Math.abs(v)
  if (abs < 10_000) return Math.round(v).toLocaleString()
  const units: Array<[number, string]> = [
    [1e12, 'T'],
    [1e9, 'B'],
    [1e6, 'M'],
    [1e3, 'K']
  ]
  for (const [base, suffix] of units) {
    if (abs >= base) {
      const scaled = v / base
      const digits = Math.abs(scaled) >= 100 ? 0 : Math.abs(scaled) >= 10 ? 1 : 2
      return `${Number(scaled.toFixed(digits))}${suffix}`
    }
  }
  return v.toLocaleString()
}

/** 金额：$1,284.20 / $0.123 / $0.0012 */
export function formatUSD(value: unknown): string {
  const v = toFinite(value)
  const abs = Math.abs(v)
  const sign = v < 0 ? '-' : ''
  if (abs >= 1) {
    return `${sign}$${abs.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`
  }
  if (abs >= 0.01) return `${sign}$${abs.toFixed(3)}`
  if (abs === 0) return '$0.00'
  return `${sign}$${abs.toFixed(4)}`
}

/** 金额缩写，用于坐标轴刻度：$1.2K / $980 */
export function formatUSDCompact(value: unknown): string {
  const v = toFinite(value)
  if (Math.abs(v) < 1000) return `$${Number(v.toFixed(Math.abs(v) < 10 ? 2 : 0))}`
  return `$${formatCompact(v)}`
}

export function formatDurationMs(ms: unknown): string {
  const v = toFinite(ms)
  return v >= 1000 ? `${(v / 1000).toFixed(2)}s` : `${Math.round(v)}ms`
}

export function formatPercent(ratio: unknown, digits = 1): string {
  return `${(toFinite(ratio) * 100).toFixed(digits)}%`
}

/**
 * 缓存命中率：缓存读 ÷ (输入 + 缓存写 + 缓存读)。各平台写入用量日志时
 * input_tokens 已扣除缓存读写，所以三者相加就是完整的提示词 token。
 * 没有提示词 token 时返回 null（显示为“—”，不能当成 0%）。
 */
export function cacheHitRatio(input: unknown, cacheCreation: unknown, cacheRead: unknown): number | null {
  const read = toFinite(cacheRead)
  const prompt = toFinite(input) + toFinite(cacheCreation) + read
  return prompt > 0 ? read / prompt : null
}

/** 毛利率：(营收 − 成本) ÷ 营收；没有营收时返回 null */
export function marginRatio(revenue: unknown, cost: unknown): number | null {
  const r = toFinite(revenue)
  return r > 0 ? (r - toFinite(cost)) / r : null
}

// ==================== Chart.js 公共配置 ====================

/** 坐标轴：发丝级实线网格、弱化刻度，不画外框 */
export function axisOptions(theme: ChartTheme, opts: { grid?: boolean; format?: (v: number) => string } = {}) {
  return {
    border: { display: false },
    grid: {
      display: opts.grid ?? true,
      color: theme.grid,
      drawTicks: false
    },
    ticks: {
      color: theme.tick,
      padding: 8,
      font: { size: 11 },
      ...(opts.format ? { callback: (value: string | number) => opts.format!(Number(value)) } : {})
    }
  }
}

export interface HtmlTooltipRow {
  color: string
  label: string
  value: string
  /** 'line' 用短线作图例键（折线），'rect' 用方块（柱/面积） */
  key?: 'line' | 'rect'
}

export interface HtmlTooltipContent {
  title: string
  rows: HtmlTooltipRow[]
  footer?: string[]
}

/**
 * Chart.js 外部 HTML 提示框：数值在前、名称在后，用短线/色块作序列键。
 * 所有文字都通过 textContent 写入（标签来自接口数据，不能走 innerHTML）。
 */
export function htmlTooltip(
  build: (tooltip: TooltipModel<any>) => HtmlTooltipContent | null
) {
  return (context: { chart: Chart; tooltip: TooltipModel<any> }) => {
    const { chart, tooltip } = context
    const parent = chart.canvas.parentNode as HTMLElement | null
    if (!parent) return

    let el = parent.querySelector<HTMLDivElement>(':scope > .chart-tooltip')
    if (!el) {
      el = document.createElement('div')
      el.className = 'chart-tooltip'
      el.setAttribute('role', 'tooltip')
      parent.appendChild(el)
    }

    if (tooltip.opacity === 0) {
      el.style.opacity = '0'
      return
    }

    const content = build(tooltip)
    if (!content) {
      el.style.opacity = '0'
      return
    }

    el.replaceChildren()
    const title = document.createElement('div')
    title.className = 'chart-tooltip-title'
    title.textContent = content.title
    el.appendChild(title)

    for (const row of content.rows) {
      const line = document.createElement('div')
      line.className = 'chart-tooltip-row'
      const key = document.createElement('span')
      key.className = row.key === 'rect' ? 'chart-tooltip-key-rect' : 'chart-tooltip-key-line'
      key.style.backgroundColor = row.color
      const value = document.createElement('span')
      value.className = 'chart-tooltip-value'
      value.textContent = row.value
      const label = document.createElement('span')
      label.className = 'chart-tooltip-label'
      label.textContent = row.label
      line.append(key, value, label)
      el.appendChild(line)
    }

    if (content.footer?.length) {
      const footer = document.createElement('div')
      footer.className = 'chart-tooltip-footer'
      for (const text of content.footer) {
        const item = document.createElement('div')
        item.textContent = text
        footer.appendChild(item)
      }
      el.appendChild(footer)
    }

    // 定位：默认在指针右侧，放不下时翻到左侧
    const canvasLeft = chart.canvas.offsetLeft
    const canvasTop = chart.canvas.offsetTop
    const width = el.offsetWidth
    const height = el.offsetHeight
    const gap = 12
    let left = canvasLeft + tooltip.caretX + gap
    if (left + width > parent.clientWidth) {
      left = canvasLeft + tooltip.caretX - width - gap
    }
    let top = canvasTop + tooltip.caretY - height / 2
    top = Math.max(0, Math.min(top, parent.clientHeight - height))
    el.style.opacity = '1'
    el.style.left = `${Math.max(0, left)}px`
    el.style.top = `${top}px`
  }
}
