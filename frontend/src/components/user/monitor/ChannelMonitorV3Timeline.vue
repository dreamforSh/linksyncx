<template>
  <div class="channel-signal-timeline mt-4 border-t border-white/70 pt-3 dark:border-dark-700/60">
    <div class="channel-signal-timeline__legend mb-2 flex justify-between text-[10px] font-semibold uppercase tracking-widest text-gray-400">
      <span>{{ t('monitorCommon.history60pts', { n: columns.length }) }}</span>
    </div>

    <!-- Bars = cards × buckets (thousands on a full board): each bar binds only a class string,
         a shared per-column aria-label and its index. Hover/focus details are built on demand by
         the delegated handlers below and shown in the page's single floating tooltip.
         Keep bars free of `position`, z-index and will-change (each would add a paint layer per
         bar), and do not add v-memo here (eslint vue/valid-v-memo rejects it inside v-for). -->
    <div
      :key="axisStep"
      class="v3-timeline-bars"
      @mouseover="onBarOver"
      @focusin="onBarOver"
      @mouseleave="hideTooltip"
      @focusout="hideTooltip"
    >
      <span
        v-for="(column, index) in columns"
        :key="column.slot.start"
        class="v3-bar"
        tabindex="0"
        role="img"
        :aria-label="column.bucket ? column.slot.time : column.slot.emptyLabel"
        :aria-describedby="tooltipId"
        :data-index="index"
      ><span :class="column.fillClass" /></span>
    </div>

    <div class="channel-signal-timeline__axis mt-1 flex justify-between text-[9px] uppercase tracking-widest text-gray-400">
      <span>{{ t('monitorCommon.past') }}</span>
      <span>{{ t('monitorCommon.now') }}</span>
    </div>

    <ChannelMonitorV3Tooltip v-if="ownTooltip" :id="tooltipId" :state="ownTooltip" />
  </div>
</template>

<script setup lang="ts">
import { computed, inject, reactive, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { MonitorMatrixBucket } from '@/api/channelMonitorV2'
import {
  availabilityBarClass,
  formatMonitorCacheRate,
  formatMonitorMs,
  formatMonitorPercent,
  isKnownHealthState,
  monitorHasTraffic,
} from '@/features/channel-monitor-v2/monitorFormat'
import { labelChannelStatusAxis, type ChannelStatusAxisSlot } from '@/features/channel-monitor-v2/channelStatusLayout'
import ChannelMonitorV3Tooltip from './ChannelMonitorV3Tooltip.vue'
import { createV3TooltipState, hideV3Tooltip, showV3Tooltip, V3_TOOLTIP_HOST, type V3TooltipState } from './v3TimelineTooltip'

const props = withDefaults(defineProps<{
  buckets?: MonitorMatrixBucket[]
  /** Shared time axis from matrix coverage; each bucket is placed by its bucket_start. */
  axis?: ChannelStatusAxisSlot[]
}>(), {
  buckets: () => [],
  axis: () => [],
})

const { t, locale } = useI18n()

// The page provides one tooltip for every timeline; a standalone timeline renders its own.
const host = inject(V3_TOOLTIP_HOST, null)
const localTooltipId = `v3-timeline-tooltip-${useId()}`
const tooltipState: V3TooltipState = host?.state ?? reactive(createV3TooltipState())
const ownTooltip = host ? null : tooltipState
const tooltipId = host?.id ?? localTooltipId
const owner = Symbol('v3-timeline')
let activeIndex = -1

interface TimelineColumn {
  slot: ChannelStatusAxisSlot
  bucket?: MonitorMatrixBucket
  fillClass: string
}

/** Heights come from the state class (see styles), colours from the availability band. */
const UNKNOWN_FILL = 'v3-bar-fill is-unknown bg-gray-300 dark:bg-dark-600'
const EMPTY_FILL = 'v3-bar-fill is-empty bg-gray-200 dark:bg-dark-700'

function fillClass(bucket: MonitorMatrixBucket): string {
  const state = bucket.health.overall
  if (!isKnownHealthState(state) || !monitorHasTraffic(bucket.metrics, bucket.health)) return UNKNOWN_FILL
  return `v3-bar-fill is-${state} ${availabilityBarClass((1 - bucket.metrics.error_rate) * 100)}`
}

/** Older payloads without a usable coverage: plot the buckets themselves, oldest first. */
function fallbackAxis(starts: Iterable<number>): ChannelStatusAxisSlot[] {
  return labelChannelStatusAxis(
    [...starts].sort((a, b) => a - b),
    locale.value || undefined,
    time => t('channelMonitorV2.matrix.noTrafficAt', { time }),
  )
}

const columns = computed<TimelineColumn[]>(() => {
  const byStart = new Map<number, MonitorMatrixBucket>()
  for (const bucket of props.buckets) {
    const start = Date.parse(bucket.bucket_start)
    if (Number.isFinite(start)) byStart.set(start, bucket)
  }
  // Empty slots stay where they are on the axis (no traffic in that interval) instead of
  // packing the sparse bucket list against one end.
  const axis = props.axis.length > 0 ? props.axis : fallbackAxis(byStart.keys())
  return axis.map((slot) => {
    const bucket = byStart.get(slot.start)
    return bucket ? { slot, bucket, fillClass: fillClass(bucket) } : { slot, fillClass: EMPTY_FILL }
  })
})

/**
 * Bucket size of the axis. Keying the bars container on it replays the entrance animation
 * when the range changes, but not when a refresh merely shifts the window by one bucket.
 */
const axisStep = computed(() => {
  const list = columns.value
  return list.length > 1 ? list[1].slot.start - list[0].slot.start : 0
})

function tooltipText(column: TimelineColumn): string {
  const bucket = column.bucket
  if (!bucket) return column.slot.emptyLabel
  const metrics = bucket.metrics
  return t('channelMonitorV3.timelineTooltip', {
    time: column.slot.time,
    availability: monitorHasTraffic(metrics, bucket.health)
      ? formatMonitorPercent(1 - metrics.error_rate, locale.value || 'zh-CN')
      : '-',
    cache: formatMonitorCacheRate(metrics),
    ttft: formatMonitorMs(metrics.ttft.p50_ms),
  })
}

/** Delegated mouseover/focusin: details are formatted only for the bar under the pointer/focus. */
function onBarOver(event: Event) {
  const bar = (event.target as Element | null)?.closest<HTMLElement>('.v3-bar')
  // The gaps between bars keep the current tooltip so it does not flicker across them.
  if (!bar) return
  const index = Number(bar.dataset.index)
  const column = columns.value[index]
  if (!column) return
  if (index === activeIndex && tooltipState.owner === owner && tooltipState.visible) return
  activeIndex = index
  showV3Tooltip(tooltipState, owner, bar.getBoundingClientRect(), tooltipText(column))
}

function hideTooltip() {
  activeIndex = -1
  hideV3Tooltip(tooltipState, owner)
}

// A refresh while hovering keeps the open tooltip in sync with the new data.
watch(columns, (next) => {
  if (activeIndex < 0 || tooltipState.owner !== owner) return
  const column = next[activeIndex]
  if (column) tooltipState.text = tooltipText(column)
  else hideTooltip()
})
</script>

<style scoped>
.v3-timeline-bars {
  display: flex;
  height: 20px;
  width: 100%;
  gap: 4px;
  transform-origin: center bottom;
  /* One entrance animation per timeline, not one (with its own delay) per bar. */
  /* `backwards`, not `both`: a finished animation must not leave a transform (and a layer) behind. */
  animation: v3-soft-glass-rise 0.7s cubic-bezier(0.22, 1, 0.36, 1) backwards;
}

/* Full-height hit column: the visible fill can be as short as 15%. */
.v3-bar {
  display: flex;
  align-items: flex-end;
  flex: 1 1 0%;
  min-width: 0;
  height: 100%;
  border-radius: 4px;
  cursor: crosshair;
}

.v3-bar:focus-visible {
  outline: 2px solid rgb(59 130 246 / 0.6);
  outline-offset: 2px;
}

.v3-bar-fill {
  display: block;
  width: 100%;
  min-height: 3px;
  border-radius: 3px;
  transform-origin: center bottom;
  transition: transform 240ms cubic-bezier(0.22, 1, 0.36, 1), opacity 220ms ease;
}

.v3-bar-fill.is-healthy { height: 100%; }
.v3-bar-fill.is-warning { height: 65%; }
.v3-bar-fill.is-critical { height: 35%; }
.v3-bar-fill.is-unknown,
.v3-bar-fill.is-empty { height: 15%; }

/* Hover feedback is pure CSS: the hovered timeline's other bars ease back while the bar under
   the pointer lifts, so pointer moves never re-render Vue. Only the fill transforms; the hit
   column keeps its box, so the pointer cannot slip between neighbouring bars. */
.v3-timeline-bars:hover .v3-bar-fill {
  transform: scaleY(0.94);
  opacity: 0.8;
}

.v3-timeline-bars .v3-bar:hover .v3-bar-fill,
.v3-timeline-bars .v3-bar:focus-visible .v3-bar-fill {
  transform: translateY(-1px) scaleY(1.1);
  opacity: 1;
  filter: saturate(1.12) brightness(1.05);
  box-shadow: 0 4px 10px rgb(15 118 110 / 0.24);
}

@keyframes v3-soft-glass-rise {
  from {
    transform: scaleY(0.15);
    opacity: 0.3;
  }
  to {
    transform: scaleY(1);
    opacity: 1;
  }
}

@media (prefers-reduced-motion: reduce) {
  .v3-timeline-bars {
    animation: none;
  }

  .v3-bar-fill {
    transition: none;
  }
}
</style>
