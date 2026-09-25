<template>
  <div
    v-if="points.length > 1"
    class="sparkline relative w-full"
    :style="{ height: `${height}px` }"
    role="img"
    :aria-label="ariaLabel"
  >
    <svg
      class="absolute inset-0 h-full w-full overflow-visible"
      :viewBox="`0 0 100 ${VIEW_H}`"
      preserveAspectRatio="none"
      aria-hidden="true"
    >
      <defs>
        <linearGradient :id="gradientId" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" :stop-color="washColor" stop-opacity="0.18" />
          <stop offset="100%" :stop-color="washColor" stop-opacity="0" />
        </linearGradient>
      </defs>
      <path :d="areaPath" :fill="`url(#${gradientId})`" />
      <path
        :d="linePath"
        fill="none"
        :stroke="lineColor"
        stroke-width="2"
        stroke-linecap="round"
        stroke-linejoin="round"
        vector-effect="non-scaling-stroke"
      />
    </svg>
    <!-- 末端点用 HTML 定位，避免 preserveAspectRatio="none" 把圆拉成椭圆 -->
    <span
      class="sparkline-dot"
      :style="{
        left: `${lastPoint.x}%`,
        top: `${(lastPoint.y / VIEW_H) * 100}%`,
        backgroundColor: theme.accent,
        boxShadow: `0 0 0 2px ${theme.surface}`
      }"
      aria-hidden="true"
    />
  </div>
  <div v-else :style="{ height: `${height}px` }" aria-hidden="true" />
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useChartTheme } from './chartTheme'

const props = withDefaults(
  defineProps<{
    values: number[]
    height?: number
    /** neutral：弱化色折线 + 强调色末端点（默认）；accent：整条用强调色 */
    tone?: 'neutral' | 'accent'
    ariaLabel?: string
  }>(),
  {
    height: 36,
    tone: 'neutral',
    ariaLabel: undefined
  }
)

const VIEW_H = 40
// 上下留白，避免 2px 线条和末端点被裁切
const PAD_Y = 4

const theme = useChartTheme()
const gradientId = `spark-${Math.random().toString(36).slice(2, 10)}`

const lineColor = computed(() => (props.tone === 'accent' ? theme.value.accent : theme.value.muted))
const washColor = computed(() => (props.tone === 'accent' ? theme.value.accent : theme.value.muted))

const points = computed(() => {
  const values = props.values.map((v) => (Number.isFinite(Number(v)) ? Number(v) : 0))
  if (values.length < 2) return []
  const min = Math.min(...values)
  const max = Math.max(...values)
  const span = max - min
  const usable = VIEW_H - PAD_Y * 2
  return values.map((v, i) => ({
    x: (i / (values.length - 1)) * 100,
    y: span === 0 ? VIEW_H / 2 : PAD_Y + usable - ((v - min) / span) * usable
  }))
})

const lastPoint = computed(() => points.value[points.value.length - 1] ?? { x: 0, y: 0 })

const linePath = computed(() =>
  points.value.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(2)},${p.y.toFixed(2)}`).join(' ')
)

const areaPath = computed(() => {
  if (!points.value.length) return ''
  return `${linePath.value} L100,${VIEW_H} L0,${VIEW_H} Z`
})
</script>

<style scoped>
.sparkline-dot {
  position: absolute;
  width: 7px;
  height: 7px;
  border-radius: 9999px;
  transform: translate(-50%, -50%);
  pointer-events: none;
}
</style>
