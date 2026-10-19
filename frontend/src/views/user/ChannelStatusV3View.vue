<template>
  <AppLayout>
    <div class="signal-channel-page signal-channel-v3 space-y-5 pb-12">
      <section class="signal-channel-command glass-card overflow-hidden p-0">
        <header class="flex flex-wrap items-start justify-between gap-4 border-b border-gray-100 px-5 py-4 dark:border-dark-700 sm:px-6">
          <div class="min-w-0">
            <h1 class="page-title flex items-center gap-2 text-xl font-black text-gray-900 dark:text-white">
              <span class="grid h-8 w-8 place-items-center rounded-xl bg-primary-50 text-primary-500 dark:bg-primary-900/30 dark:text-primary-300"><Icon name="chart" size="sm" /></span>
              {{ t('channelMonitorV3.title') }}
            </h1>
            <div class="mt-1.5 flex flex-wrap items-center gap-2 text-xs text-gray-500 dark:text-gray-400">
              <span class="h-2 w-2 rounded-full" :class="refreshing ? 'bg-gray-400' : 'bg-emerald-500'" />
              <span>{{ snapshot ? t('channelMonitorV3.updatedTo', { time: formatTime(snapshot.coverage.data_through) }) : t('common.loading') }}</span>
              <!-- The only per-second update on the page lives in this component; cards never get the countdown. -->
              <ChannelMonitorV3Countdown :deadline="nextRefreshAt" class="text-gray-400 dark:text-gray-500" />
              <span v-if="snapshot && !snapshot.coverage.coverage_complete" class="badge badge-warning">{{ t('channelMonitorV3.partialCoverage') }}</span>
            </div>
          </div>
          <button class="btn btn-secondary btn-icon h-8 w-8 rounded-lg" type="button" :disabled="loading || refreshing" :title="t('common.refresh')" @click="reload(false)"><Icon name="refresh" size="sm" :class="refreshing ? 'animate-spin' : ''" /></button>
        </header>
        <div class="signal-channel-range flex flex-wrap items-center gap-2 px-4 py-3 sm:px-5">
          <button v-for="option in ranges" :key="option.value" type="button" class="tab !px-2.5 !py-1 text-xs" :class="filter.range === option.value ? 'tab-active' : ''" @click="setRange(option.value)">{{ option.label }}</button>
          <span class="mx-1 hidden h-5 w-px bg-gray-200 dark:bg-dark-700 sm:block" />
          <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('channelMonitorV3.description') }}</span>
          <span v-if="snapshot" class="ml-auto text-xs font-medium tabular-nums text-gray-500 dark:text-gray-400">{{ t('channelMonitorV3.summary', { success: summarySuccessRate, cache: formatMonitorCacheRate(snapshot.metrics) }) }}</span>
        </div>
        <div
          class="signal-channel-platforms flex flex-wrap items-center gap-1.5 border-t border-gray-100 px-4 py-2.5 dark:border-dark-700 sm:px-5"
          data-testid="channel-status-platform-filters"
        >
          <button
            type="button"
            class="tab !px-2.5 !py-1 text-xs"
            :class="filter.platforms.length === 0 ? 'tab-active' : ''"
            @click="setPlatform(null)"
          >
            {{ t('channelMonitorV3.allPlatforms') }}
          </button>
          <button
            v-for="item in platformFilters"
            :key="item.value"
            type="button"
            class="tab !px-2.5 !py-1 text-xs"
            :class="filter.platforms.length === 1 && filter.platforms[0] === item.value ? 'tab-active' : ''"
            @click="setPlatform(item.value)"
          >
            {{ item.label }}
          </button>
        </div>
      </section>

      <div v-if="loading && rows.length === 0" class="signal-channel-loading-grid grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
        <div v-for="i in 8" :key="i" class="h-72 animate-pulse bg-white/60 dark:bg-dark-800" />
      </div>
      <EmptyState v-else-if="rows.length === 0" :title="t('channelMonitorV3.emptyTitle')" :description="t('channelMonitorV3.emptyDescription')" />
      <div v-else class="channel-status-board space-y-8" data-testid="channel-status-board">
        <section
          v-for="(block, index) in layoutBlocks"
          :key="channelStatusLayoutBlockKey(block, index)"
          :data-layout="block.kind"
          :data-testid="block.kind === 'cluster' ? `channel-status-platform-${block.platform}` : 'channel-status-compact-platforms'"
        >
          <template v-if="block.kind === 'cluster'">
            <h2 class="signal-platform-heading mb-3 flex items-center gap-2 px-1 text-sm font-semibold text-gray-700 dark:text-gray-200">
              {{ providerLabel(block.platform) }}
              <span class="rounded-full bg-gray-100 px-1.5 py-px font-mono text-[10px] font-medium text-gray-500 dark:bg-dark-700 dark:text-gray-400">{{ block.rows.length }}</span>
            </h2>
            <div class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
              <ChannelMonitorV3Card
                v-for="row in block.rows"
                :key="row.group_id ?? `${row.platform}:${row.group_name ?? ''}`"
                :row="row"
                :user-rate-multiplier="getUserRateMultiplier(row.group_id)"
                :axis="axis"
              />
            </div>
          </template>
          <div
            v-else
            class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4"
          >
            <div
              v-for="item in block.items"
              :key="item.row.group_id ?? `${item.platform}:${item.row.group_name ?? ''}`"
              :data-testid="`channel-status-platform-${item.platform}`"
            >
              <h2 class="signal-platform-heading mb-3 px-1 text-sm font-semibold text-gray-700 dark:text-gray-200">{{ providerLabel(item.platform) }}</h2>
              <ChannelMonitorV3Card
                :row="item.row"
                :user-rate-multiplier="getUserRateMultiplier(item.row.group_id)"
                :axis="axis"
              />
            </div>
          </div>
        </section>
      </div>
      <ChannelMonitorV3Tooltip :id="timelineTooltipId" :state="timelineTooltip" />
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, provide, reactive, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import * as api from '@/api/channelMonitorV2'
import userGroupsAPI from '@/api/groups'
import type { MonitorDimensions, MonitorFilter, MonitorMatrixResponse, MonitorRange, MonitorSnapshot } from '@/api/channelMonitorV2'
import type { Group } from '@/types'
import ChannelMonitorV3Card from '@/components/user/monitor/ChannelMonitorV3Card.vue'
import ChannelMonitorV3Countdown from '@/components/user/monitor/ChannelMonitorV3Countdown.vue'
import ChannelMonitorV3Tooltip from '@/components/user/monitor/ChannelMonitorV3Tooltip.vue'
import { createV3TooltipState, V3_TOOLTIP_HOST } from '@/components/user/monitor/v3TimelineTooltip'
import { useChannelMonitorFormat } from '@/composables/useChannelMonitorFormat'
import {
  cachedDateTimeFormat,
  formatMonitorCacheRate,
  formatMonitorPercent,
  monitorHasTraffic,
} from '@/features/channel-monitor-v2/monitorFormat'
import {
  buildChannelStatusAxis,
  buildChannelStatusLayout,
  channelStatusLayoutBlockKey,
  labelChannelStatusAxis,
} from '@/features/channel-monitor-v2/channelStatusLayout'

const DEFAULT_REFRESH_SECONDS = 60
const MIN_REFRESH_SECONDS = 10
const BOOTSTRAP_REFRESH_SECONDS = 10
const FAILURE_RETRY_SECONDS = 30

const { t, locale } = useI18n()
const appStore = useAppStore()
const { providerLabel } = useChannelMonitorFormat()
const ranges = computed(() => [
  { value: '90m' as MonitorRange, label: t('channelMonitorV3.ranges.90m') },
  { value: '24h' as MonitorRange, label: t('channelMonitorV3.ranges.24h') },
  { value: '7d' as MonitorRange, label: t('channelMonitorV3.ranges.7d') },
  { value: '30d' as MonitorRange, label: t('channelMonitorV3.ranges.30d') },
])
const filter = ref<MonitorFilter>({ range: '90m', platforms: [], groupIds: [], models: [] })
const snapshot = ref<MonitorSnapshot | null>(null)
const matrix = ref<MonitorMatrixResponse | null>(null)
const dimensions = ref<MonitorDimensions | null>(null)
const loading = ref(true)
const refreshing = ref(false)
const userGroupRates = ref<Record<number, number>>({})
/** Epoch ms of the next auto refresh: arms the refresh timer and drives the countdown. */
const nextRefreshAt = ref<number | null>(null)
let controller: AbortController | null = null
let refreshTimer: number | null = null
let refreshIntervalSeconds = DEFAULT_REFRESH_SECONDS
let disposed = false

// One tooltip for every timeline on the board (instead of a Teleport per bar or per card).
const timelineTooltip = reactive(createV3TooltipState())
const timelineTooltipId = `channel-status-tooltip-${useId()}`
provide(V3_TOOLTIP_HOST, { id: timelineTooltipId, state: timelineTooltip })

// platform_group is intentionally used here: the backend scopes it to the
// monitor group_ids selected by the operator, so unrelated groups never appear.
const rows = computed(() => [...(matrix.value?.items ?? [])]
  .filter(row => row.group_id != null && row.group_id > 0)
  .sort((a, b) => (a.group_id ?? 0) - (b.group_id ?? 0)))
const platformSections = computed(() => {
  const grouped = new Map<string, typeof rows.value>()
  for (const row of rows.value) {
    const platform = row.platform || 'unknown'
    const current = grouped.get(platform) ?? []
    current.push(row)
    grouped.set(platform, current)
  }
  return [...grouped.entries()].map(([platform, sectionRows]) => ({
    platform,
    rows: sectionRows.sort((a, b) => (a.group_id ?? 0) - (b.group_id ?? 0)),
  }))
})
const layoutBlocks = computed(() => buildChannelStatusLayout(platformSections.value))
// Single-select platform chips: "All" plus every platform present in the
// selected dimension scope. The chip list stays stable while the matrix is
// filtered by the selected platform.
const platformFilters = computed(() => (dimensions.value?.platforms ?? []).map(item => ({
  value: item.value,
  label: providerLabel(item.value),
})))
// Time axis of the selected range, built once and shared by every card. The backend only
// returns buckets that had traffic; cards place them on this axis by bucket_start.
const axis = computed(() => labelChannelStatusAxis(
  buildChannelStatusAxis(matrix.value?.coverage),
  locale.value || undefined,
  time => t('channelMonitorV2.matrix.noTrafficAt', { time }),
))
const summarySuccessRate = computed(() => {
  const current = snapshot.value
  if (!current || !monitorHasTraffic(current.metrics, current.health)) return '-'
  return formatMonitorPercent(1 - current.metrics.error_rate, locale.value || 'zh-CN')
})

function formatTime(value?: string) {
  if (!value) return '-'
  return cachedDateTimeFormat(locale.value || undefined, 'short-date-time', { dateStyle: 'short', timeStyle: 'short' }).format(new Date(value))
}
function setRange(value: MonitorRange) { filter.value = { ...filter.value, range: value } }
function setPlatform(value: string | null) {
  const next = value ? [value] : []
  if (filter.value.platforms.length === next.length && filter.value.platforms[0] === next[0]) return
  filter.value = { ...filter.value, platforms: next }
}

function getUserRateMultiplier(groupId?: number) {
  if (!groupId) return null
  const value = userGroupRates.value[groupId]
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

async function loadUserGroupRates() {
  try {
    const [groups, customRates] = await Promise.all([
      userGroupsAPI.getAvailable(),
      userGroupsAPI.getUserGroupRates(),
    ])
    userGroupRates.value = Object.fromEntries(
      (groups as Group[]).map(group => [group.id, customRates[group.id] ?? group.rate_multiplier]),
    )
  } catch (error) {
    // Monitoring remains usable when the optional pricing endpoint is unavailable.
    console.warn('Failed to load channel monitor group rates', error)
  }
}

async function reload(silent = true) {
  controller?.abort()
  const request = new AbortController()
  controller = request
  // Every reload ends by arming exactly one next refresh (success or failure).
  clearRefreshTimer()
  nextRefreshAt.value = null
  refreshing.value = true
  if (!silent) loading.value = true
  try {
    const [nextSnapshot, nextMatrix, nextDimensions] = await Promise.all([
      api.getSnapshot(filter.value, false, request.signal),
      api.getMatrix(filter.value, 'platform_group', false, request.signal),
      // Platform chips are an enhancement: a dimensions failure must not hide
      // the channel board itself.
      api.getDimensions({ ...filter.value, platforms: [] }, false, request.signal).catch(() => null),
    ])
    if (request.signal.aborted || controller !== request) return
    snapshot.value = nextSnapshot
    matrix.value = nextMatrix
    if (nextDimensions) dimensions.value = nextDimensions
    refreshIntervalSeconds = nextSnapshot.config.refresh_interval_seconds || refreshIntervalSeconds
    scheduleRefresh(nextSnapshot.coverage.bootstrap?.active ? BOOTSTRAP_REFRESH_SECONDS : refreshIntervalSeconds)
  } catch (error) {
    // Superseded by a newer reload (which owns the schedule) or torn down on unmount.
    if (request.signal.aborted || controller !== request) return
    const e = error as { name?: string; code?: string }
    if (e.name !== 'AbortError' && e.code !== 'ERR_CANCELED') appStore.showError(extractApiErrorMessage(error, t('channelMonitorV3.loadFailed')))
    // Retry failures too, including a failed first load, never later than the normal cadence.
    scheduleRefresh(Math.min(refreshIntervalSeconds, FAILURE_RETRY_SECONDS))
  } finally {
    if (controller === request) { loading.value = false; refreshing.value = false }
  }
}

function clearRefreshTimer() {
  if (refreshTimer != null) window.clearTimeout(refreshTimer)
  refreshTimer = null
}

// A single deadline arms the timer and feeds the countdown, so they cannot drift apart.
function scheduleRefresh(seconds: number) {
  clearRefreshTimer()
  if (disposed) return
  const delay = Math.max(MIN_REFRESH_SECONDS, seconds) * 1000
  nextRefreshAt.value = Date.now() + delay
  refreshTimer = window.setTimeout(onRefreshDue, delay)
}

function onRefreshDue() {
  refreshTimer = null
  // A hidden tab does not poll; the due refresh runs as soon as the tab is visible again.
  if (document.hidden) return
  void reload(true)
}

function onVisibilityChange() {
  if (document.hidden || refreshTimer != null || refreshing.value) return
  if (nextRefreshAt.value != null && Date.now() >= nextRefreshAt.value) void reload(true)
}

watch(() => filter.value.range, () => void reload(false))
watch(() => filter.value.platforms, () => void reload(false))
onMounted(() => {
  document.addEventListener('visibilitychange', onVisibilityChange)
  void loadUserGroupRates()
  void reload(false)
})
onBeforeUnmount(() => {
  disposed = true
  document.removeEventListener('visibilitychange', onVisibilityChange)
  controller?.abort()
  clearRefreshTimer()
})
</script>
