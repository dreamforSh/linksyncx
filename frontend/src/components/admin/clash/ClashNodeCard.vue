<template>
  <article
    class="flex min-w-0 flex-col rounded-xl border bg-white shadow-sm transition-shadow hover:shadow-md dark:bg-dark-900"
    :class="selected
      ? 'border-primary-300 ring-2 ring-primary-400/40 dark:border-primary-500/60'
      : 'border-gray-200 dark:border-dark-700'"
    data-testid="clash-node-card"
    :data-node-id="node.id"
  >
    <header class="flex items-start gap-2.5 px-4 pt-4">
      <input
        type="checkbox"
        class="mt-0.5 h-4 w-4 flex-shrink-0 cursor-pointer rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-500 dark:bg-dark-800"
        :checked="selected"
        :aria-label="t('admin.clash.nodes.card.select', { name: node.name })"
        data-testid="clash-node-select"
        @change="emit('toggle-select')"
      />
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-1.5">
          <h3 class="truncate text-sm font-semibold text-gray-900 dark:text-white" :title="node.name">{{ node.name }}</h3>
          <span :class="CLASH_TYPE_BADGE_CLASS">{{ node.type }}</span>
          <span
            v-if="node.hidden"
            :class="CLASH_HIDDEN_BADGE_CLASS"
            role="img"
            :aria-label="t('admin.clash.nodes.hiddenTag')"
            :title="t('admin.clash.nodes.hiddenHint')"
            data-testid="clash-node-hidden-badge"
          >
            <Icon name="eyeOff" size="xs" />
          </span>
        </div>
        <p class="mt-0.5 truncate text-xs text-gray-500 dark:text-dark-400" :title="`${node.profile_name} · ${node.server}:${node.server_port}`">
          {{ node.profile_name }}<span v-if="node.listen_port"> · {{ t('admin.clash.nodes.listenPort', { port: node.listen_port }) }}</span>
        </p>
      </div>
      <span :class="statusPillClass(node.status)" data-testid="clash-node-status">
        <span class="h-1.5 w-1.5 rounded-full" :class="statusDotClass(node.status)" aria-hidden="true"></span>
        {{ statusLabel(node.status) }}
      </span>
    </header>

    <p
      v-if="node.status !== 'active' && node.status_reason"
      class="mx-4 mt-2 line-clamp-2 break-words text-xs"
      :class="node.status === 'invalid' ? 'text-red-600 dark:text-red-400' : 'text-gray-500 dark:text-dark-400'"
      :title="node.status_reason"
    >{{ localizeClashUnavailableReason(node.status_reason, t) }}</p>
    <p
      v-else-if="checkError(node)"
      class="mx-4 mt-2 line-clamp-2 break-words text-xs"
      :class="node.health_status === 'unhealthy' ? 'text-red-600 dark:text-red-400' : 'text-amber-700 dark:text-amber-300'"
      :title="checkError(node)"
      data-testid="clash-node-check-error"
    >{{ checkError(node) }}</p>

    <div class="mx-4 mt-3 grid grid-cols-3 divide-x divide-gray-200 rounded-lg bg-gray-50 py-2 text-center dark:divide-dark-700 dark:bg-dark-800/70">
      <div class="min-w-0 px-1.5" :title="healthTitle(node)" data-testid="clash-node-health">
        <p class="text-[11px] text-gray-500 dark:text-dark-400">{{ t('admin.clash.nodes.card.latency') }}</p>
        <p class="mt-0.5 flex items-center justify-center gap-1 truncate text-sm">
          <span class="h-2 w-2 flex-shrink-0 rounded-full" :class="healthDotClass(node)" aria-hidden="true"></span>
          <span :class="healthTextClass(node)">{{ healthLabel(node) }}</span>
        </p>
      </div>
      <div class="min-w-0 px-1.5" :title="todayTitle" data-testid="clash-node-traffic-today">
        <p class="text-[11px] text-gray-500 dark:text-dark-400">{{ t('admin.clash.nodes.traffic.today') }}</p>
        <p class="mt-0.5 truncate text-sm font-medium tabular-nums text-gray-900 dark:text-gray-100">{{ formatTrafficBytes(today) }}</p>
      </div>
      <div class="min-w-0 px-1.5" :title="liveTitle" data-testid="clash-node-traffic-live">
        <p class="text-[11px] text-gray-500 dark:text-dark-400">{{ t('admin.clash.nodes.traffic.live') }}</p>
        <p
          class="mt-0.5 truncate text-sm tabular-nums"
          :class="connections > 0 ? 'font-medium text-primary-700 dark:text-primary-300' : 'text-gray-400 dark:text-dark-500'"
        >
          <template v-if="connections > 0">{{ formatTrafficRate(downloadRate) }}</template>
          <template v-else>-</template>
        </p>
      </div>
    </div>

    <div class="mx-4 mt-3">
      <div class="flex items-center justify-between text-[11px] text-gray-500 dark:text-dark-400">
        <span>{{ t('admin.clash.nodes.traffic.trend') }}</span>
        <span class="tabular-nums" :title="totalTitle" data-testid="clash-node-traffic-total">
          {{ t('admin.clash.nodes.traffic.totalValue', { value: formatTrafficBytes(total) }) }}
        </span>
      </div>
      <Sparkline
        v-if="hasTrend"
        class="mt-1"
        :values="trend"
        :height="28"
        tone="accent"
        :aria-label="t('admin.clash.nodes.traffic.trendAria')"
      />
      <p v-else class="mt-1 flex h-7 items-center text-xs text-gray-400 dark:text-dark-500">{{ t('admin.clash.nodes.traffic.noTraffic') }}</p>
    </div>

    <div class="mx-4 mt-3 min-h-[2.5rem] text-xs" data-testid="clash-node-exit">
      <div v-if="node.exit_ip" class="flex items-center gap-1.5">
        <CountryFlag :code="node.exit_country_code" :label="node.exit_country" />
        <span class="font-mono text-gray-900 dark:text-gray-100">{{ node.exit_ip }}</span>
        <span v-if="exitLocation(node)" class="truncate text-gray-500 dark:text-dark-400" :title="exitLocation(node)">{{ exitLocation(node) }}</span>
      </div>
      <div
        v-else-if="exitFailed(node)"
        class="text-amber-600 dark:text-amber-400"
        :title="t('admin.clash.nodes.checkedAt', { time: formatDateTime(node.exit_checked_at) })"
        data-testid="clash-node-exit-failed"
      >{{ t('admin.clash.nodes.exitFailed') }}</div>
      <div v-else class="italic text-gray-400 dark:text-dark-500">{{ t('admin.clash.nodes.exitUnprobed') }}</div>
      <div
        v-if="node.exit_status === 'changed'"
        class="mt-1 flex flex-wrap items-center gap-x-1.5 gap-y-1 rounded-md bg-amber-50 px-1.5 py-1 text-amber-800 dark:bg-amber-500/10 dark:text-amber-300"
        data-testid="clash-node-exit-changed"
      >
        <Icon name="exclamationTriangle" size="xs" class="flex-shrink-0" />
        <span>{{ t('admin.clash.nodes.pendingExit', { ip: node.exit_pending_ip || '-' }) }}</span>
        <button
          type="button"
          class="font-semibold underline decoration-dotted underline-offset-2 hover:text-amber-900 disabled:opacity-50 dark:hover:text-amber-200"
          :disabled="readonly"
          data-testid="clash-node-accept-exit"
          @click="emit('accept-exit')"
        >
          {{ t('admin.clash.actions.acceptExit') }}
        </button>
      </div>
      <div v-else-if="node.exit_status === 'stale'" class="mt-0.5 text-amber-600 dark:text-amber-400">
        {{ t('admin.clash.nodes.exitStale') }}
      </div>
      <div class="mt-1.5 flex flex-wrap gap-1" data-testid="clash-node-platforms">
        <span
          v-for="platform in CLASH_CHECK_PLATFORMS"
          :key="platform"
          :class="platformBadgeClass(node.platform_checks.results[platform])"
          :title="platformTitle(node, platform)"
        >{{ CLASH_PLATFORM_LABELS[platform] }}</span>
      </div>
    </div>

    <div class="mt-auto flex items-center gap-2 border-t border-gray-100 px-4 py-2.5 dark:border-dark-700/70">
      <button
        type="button"
        class="flex min-w-0 flex-1 items-center gap-1 rounded-md py-0.5 text-left text-xs transition-colors hover:bg-gray-50 dark:hover:bg-dark-800"
        :title="bindingsTitle"
        data-testid="clash-node-accounts"
        @click="emit('bindings')"
      >
        <Icon name="link" size="xs" class="flex-shrink-0 text-gray-400 dark:text-dark-500" />
        <template v-if="node.accounts.length > 0">
          <span class="truncate rounded bg-gray-100 px-1.5 py-0.5 text-gray-700 dark:bg-dark-700 dark:text-gray-300">
            {{ node.accounts[0].name }}<template v-if="node.accounts[0].is_shadow"> · {{ t('admin.clash.nodes.shadowTag') }}</template>
          </span>
          <span v-if="node.accounts.length > 1" class="flex-shrink-0 text-gray-500 dark:text-dark-400">+{{ node.accounts.length - 1 }}</span>
        </template>
        <span v-else class="text-emerald-600 dark:text-emerald-400">{{ t('admin.clash.nodes.card.idle') }}</span>
        <span
          v-if="maxPerExit"
          class="ml-auto flex-shrink-0 tabular-nums text-gray-400 dark:text-dark-500"
          data-testid="clash-node-occupancy"
        >{{ occupants }}/{{ maxPerExit }}</span>
      </button>
      <div class="flex flex-shrink-0 items-center gap-0.5">
        <button
          type="button"
          class="row-action px-1.5 disabled:cursor-not-allowed disabled:opacity-50"
          :disabled="readonly || busy || node.status === 'invalid'"
          :title="t('admin.clash.actions.testLatency')"
          :aria-label="t('admin.clash.actions.testLatency')"
          data-testid="clash-node-latency"
          @click="emit('test-latency')"
        >
          <Icon name="bolt" size="sm" :class="busy ? 'animate-pulse' : ''" />
        </button>
        <button
          type="button"
          class="row-action px-1.5 disabled:cursor-not-allowed disabled:opacity-50"
          :disabled="readonly || busy || node.status !== 'active'"
          :title="t('admin.clash.actions.probeExit')"
          :aria-label="t('admin.clash.actions.probeExit')"
          data-testid="clash-node-probe"
          @click="emit('probe-exit')"
        >
          <Icon name="globe" size="sm" />
        </button>
        <button
          v-if="node.hidden"
          type="button"
          class="row-action px-1.5 hover:!bg-emerald-50 hover:!text-emerald-700 disabled:cursor-not-allowed disabled:opacity-50 dark:hover:!bg-emerald-500/10 dark:hover:!text-emerald-300"
          :disabled="busy"
          :title="t('admin.clash.actions.unhide')"
          :aria-label="t('admin.clash.actions.unhide')"
          data-testid="clash-node-unhide"
          @click="emit('unhide')"
        >
          <Icon name="eye" size="sm" />
        </button>
        <template v-else>
          <button
            v-if="node.status === 'disabled'"
            type="button"
            class="row-action px-1.5 hover:!bg-emerald-50 hover:!text-emerald-700 disabled:cursor-not-allowed disabled:opacity-50 dark:hover:!bg-emerald-500/10 dark:hover:!text-emerald-300"
            :disabled="busy"
            :title="t('admin.clash.actions.enable')"
            :aria-label="t('admin.clash.actions.enable')"
            data-testid="clash-node-enable"
            @click="emit('enable')"
          >
            <Icon name="checkCircle" size="sm" />
          </button>
          <button
            v-else-if="node.status === 'active'"
            type="button"
            class="row-action px-1.5 hover:!bg-amber-50 hover:!text-amber-700 disabled:cursor-not-allowed disabled:opacity-50 dark:hover:!bg-amber-500/10 dark:hover:!text-amber-300"
            :disabled="busy"
            :title="t('admin.clash.actions.disable')"
            :aria-label="t('admin.clash.actions.disable')"
            data-testid="clash-node-disable"
            @click="emit('disable')"
          >
            <Icon name="ban" size="sm" />
          </button>
          <button
            type="button"
            class="row-action px-1.5 disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="busy"
            :title="t('admin.clash.actions.hide')"
            :aria-label="t('admin.clash.actions.hide')"
            data-testid="clash-node-hide"
            @click="emit('hide')"
          >
            <Icon name="eyeOff" size="sm" />
          </button>
        </template>
      </div>
    </div>
  </article>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import CountryFlag from '@/components/common/CountryFlag.vue'
import Sparkline from '@/components/charts/Sparkline.vue'
import Icon from '@/components/icons/Icon.vue'
import type { ClashNode } from '@/types'
import {
  clashTrafficToday,
  clashTrafficTotal,
  clashTrafficTrend,
  formatTrafficBytes,
  formatTrafficRate,
  localizeClashUnavailableReason
} from '@/utils/clash'
import { formatDateTime } from '@/utils/format'
import {
  CLASH_CHECK_PLATFORMS,
  CLASH_PLATFORM_LABELS,
  CLASH_HIDDEN_BADGE_CLASS,
  CLASH_TYPE_BADGE_CLASS,
  useClashNodeDisplay
} from './useClashNodeDisplay'

const props = withDefaults(
  defineProps<{
    node: ClashNode
    selected?: boolean
    busy?: boolean
    readonly?: boolean
    /** Per-exit account limit; hides the occupancy figure when unknown. */
    maxPerExit?: number | null
  }>(),
  { selected: false, busy: false, readonly: false, maxPerExit: null }
)

const emit = defineEmits<{
  (e: 'toggle-select'): void
  (e: 'test-latency'): void
  (e: 'probe-exit'): void
  (e: 'enable'): void
  (e: 'disable'): void
  (e: 'hide'): void
  (e: 'unhide'): void
  (e: 'accept-exit'): void
  (e: 'bindings'): void
}>()

const { t } = useI18n()
const {
  statusLabel,
  statusPillClass,
  statusDotClass,
  healthLabel,
  healthDotClass,
  healthTextClass,
  healthTitle,
  checkError,
  exitFailed,
  exitLocation,
  platformBadgeClass,
  platformTitle,
  accountNames
} = useClashNodeDisplay()

const traffic = computed(() => props.node.traffic)
const today = computed(() => clashTrafficToday(traffic.value))
const total = computed(() => clashTrafficTotal(traffic.value))
const trend = computed(() => clashTrafficTrend(traffic.value))
const hasTrend = computed(() => trend.value.length > 1 && trend.value.some((value) => value > 0))
const connections = computed(() => traffic.value?.connections ?? 0)
const downloadRate = computed(() => traffic.value?.download_rate ?? 0)
// Shadows ride on their parent's binding and never count against the limit.
const occupants = computed(() => props.node.accounts.filter((account) => !account.is_shadow).length)

const todayTitle = computed(() =>
  t('admin.clash.nodes.traffic.split', {
    up: formatTrafficBytes(traffic.value?.today_upload_bytes),
    down: formatTrafficBytes(traffic.value?.today_download_bytes)
  })
)

const totalTitle = computed(() => {
  const lines = [
    t('admin.clash.nodes.traffic.split', {
      up: formatTrafficBytes(traffic.value?.upload_bytes),
      down: formatTrafficBytes(traffic.value?.download_bytes)
    })
  ]
  if (traffic.value?.updated_at) {
    lines.push(t('admin.clash.nodes.traffic.updatedAt', { time: formatDateTime(traffic.value.updated_at) }))
  }
  lines.push(t('admin.clash.nodes.traffic.approxHint'))
  return lines.join('\n')
})

const liveTitle = computed(() => {
  if (connections.value === 0) return t('admin.clash.nodes.traffic.idle')
  return [
    t('admin.clash.nodes.traffic.rateSplit', {
      up: formatTrafficRate(traffic.value?.upload_rate),
      down: formatTrafficRate(traffic.value?.download_rate)
    }),
    t('admin.clash.nodes.traffic.connections', { count: connections.value })
  ].join('\n')
})

const bindingsTitle = computed(() =>
  props.node.accounts.length > 0
    ? `${accountNames(props.node)}\n${t('admin.clash.nodes.card.manageBindings')}`
    : t('admin.clash.nodes.card.manageBindings')
)
</script>
