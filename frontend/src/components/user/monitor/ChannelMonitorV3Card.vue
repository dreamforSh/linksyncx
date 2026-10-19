<template>
  <article class="channel-signal-card group relative z-0 glass-card flex min-h-[286px] flex-col overflow-visible rounded-[24px] p-5 text-left hover:z-20">
    <header class="channel-signal-card__header flex items-start gap-3">
      <span class="channel-signal-card__provider grid h-9 w-9 shrink-0 place-items-center rounded-xl ring-1 ring-black/5 dark:ring-white/10" :class="providerGradient(row.platform)">
        <ProviderIcon :provider="row.platform" :size="20" />
      </span>
      <div class="min-w-0 flex-1">
        <div class="channel-signal-card__name truncate text-base font-semibold text-gray-900 dark:text-gray-100">{{ groupLabel }}</div>
        <div class="channel-signal-card__meta mt-1 flex min-w-0 flex-wrap items-center gap-1.5">
          <span v-if="showPlatformBadge" class="rounded-md px-1.5 py-0.5 text-[10px] font-medium" :class="providerBadgeClass(row.platform)">{{ providerLabel(row.platform) }}</span>
          <span class="rounded-md bg-primary-50 px-1.5 py-0.5 font-mono text-[10px] font-medium text-primary-700 dark:bg-dark-700 dark:text-gray-300">{{ t('channelMonitorV3.userRate') }} {{ formattedUserRate }}</span>
        </div>
      </div>
      <span
            v-if="showInsufficientSample"
            data-testid="channel-status-sample-badge"
            class="channel-signal-card__sample shrink-0 rounded-full bg-gray-100 px-2.5 py-1 text-xs font-semibold text-gray-600 dark:bg-dark-700 dark:text-gray-300"
          >{{ t('channelMonitorV3.unknown') }}</span>
    </header>

    <div class="channel-signal-card__metrics mt-5 grid grid-cols-3 gap-2">
      <div class="channel-signal-card__metric rounded-2xl border border-slate-200/80 bg-slate-50/85 p-3 dark:border-dark-700/50 dark:bg-dark-900/40">
        <div class="text-[10px] font-semibold uppercase tracking-wider text-gray-400">{{ t('channelMonitorV3.cacheRate') }}</div>
        <div class="mt-1.5 font-mono text-lg font-bold tabular-nums text-gray-900 dark:text-gray-100">{{ cacheRate }}</div>
      </div>
      <div class="channel-signal-card__metric rounded-2xl border border-slate-200/80 bg-slate-50/85 p-3 dark:border-dark-700/50 dark:bg-dark-900/40">
        <div class="text-[10px] font-semibold uppercase tracking-wider text-gray-400">{{ t('channelMonitorV3.successRate') }}</div>
        <div class="mt-1.5 font-mono text-lg font-bold tabular-nums" :class="availabilityClass" data-testid="channel-status-success-rate">{{ successRate }}</div>
      </div>
      <div class="channel-signal-card__metric rounded-2xl border border-slate-200/80 bg-slate-50/85 p-3 dark:border-dark-700/50 dark:bg-dark-900/40">
        <div class="text-[10px] font-semibold uppercase tracking-wider text-gray-400">{{ t('channelMonitorV3.ttft') }}</div>
        <div class="mt-1.5 font-mono text-lg font-bold tabular-nums text-gray-900 dark:text-gray-100">{{ ttft }}</div>
      </div>
    </div>

    <ChannelMonitorV3Timeline
      class="channel-signal-card__timeline mt-4"
      :buckets="row.buckets"
      :axis="axis"
    />
  </article>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { MonitorMatrixRow } from '@/api/channelMonitorV2'
import {
  availabilityTextClass,
  formatMonitorCacheRate,
  formatMonitorMs,
  formatMonitorPercent,
  isKnownHealthState,
  monitorHasTraffic,
} from '@/features/channel-monitor-v2/monitorFormat'
import type { ChannelStatusAxisSlot } from '@/features/channel-monitor-v2/channelStatusLayout'
import { providerGradient, useChannelMonitorFormat } from '@/composables/useChannelMonitorFormat'
import ProviderIcon from './ProviderIcon.vue'
import ChannelMonitorV3Timeline from './ChannelMonitorV3Timeline.vue'

const props = withDefaults(defineProps<{
  row: MonitorMatrixRow
  /** Shared, page-level time axis; deliberately no per-second countdown prop (it would re-render every card). */
  axis: ChannelStatusAxisSlot[]
  userRateMultiplier?: number | null
  showPlatformBadge?: boolean
}>(), {
  showPlatformBadge: true,
})
const { t } = useI18n()
const { providerLabel, providerBadgeClass } = useChannelMonitorFormat()

const groupLabel = computed(() => props.row.group_name || t('channelMonitorV3.unknownGroup'))
const formattedUserRate = computed(() => {
  const value = props.userRateMultiplier
  return typeof value === 'number' && Number.isFinite(value) ? `${value.toFixed(2)}x` : '-'
})
// Metrics follow the currently selected range (e.g. 90m → the 90m aggregate),
// matching the range tabs and the summary line above the board.
const cacheRate = computed(() => formatMonitorCacheRate(props.row.metrics))
// error_rate is 0 for a window without traffic, so 1 - error_rate alone would read as 100%.
const availabilityPercent = computed(() => (
  monitorHasTraffic(props.row.metrics, props.row.health) ? (1 - props.row.metrics.error_rate) * 100 : null
))
const successRate = computed(() => (
  availabilityPercent.value == null ? '-' : formatMonitorPercent(availabilityPercent.value / 100)
))
const availabilityClass = computed(() => {
  if (availabilityPercent.value == null) return 'text-gray-400 dark:text-gray-500'
  // Too few samples for an error-rate band: show the measured rate without a good/bad colour.
  if (!isKnownHealthState(props.row.health.error_rate)) return 'text-gray-900 dark:text-gray-100'
  return availabilityTextClass(availabilityPercent.value)
})
const ttft = computed(() => formatMonitorMs(props.row.metrics.ttft.p50_ms))
const showInsufficientSample = computed(() => !isKnownHealthState(props.row.health.overall))
</script>
