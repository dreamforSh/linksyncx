<template>
  <section class="card overflow-hidden" aria-labelledby="clash-profiles-title" data-testid="clash-profile-table">
    <div class="card-section-header">
      <div>
        <h2 id="clash-profiles-title" class="card-section-title">{{ t('admin.clash.profiles.title') }}</h2>
        <p class="card-section-subtitle">{{ t('admin.clash.profiles.subtitle', { count: profiles.length }) }}</p>
      </div>
      <slot name="actions" />
    </div>

    <DataTable
      :columns="columns"
      :data="profiles"
      :loading="loading"
      row-key="id"
      :expandable-actions="false"
    >
      <template #cell-name="{ row }">
        <div class="min-w-[12rem] max-w-[14rem]">
          <div class="flex items-center gap-2">
            <span class="truncate font-medium text-gray-900 dark:text-white" :title="row.name">{{ row.name }}</span>
            <span v-if="row.last_format" class="format-badge">{{ formatLabel(row.last_format) }}</span>
          </div>
          <div class="mt-0.5 truncate font-mono text-xs text-gray-500 dark:text-dark-400" :title="row.url_masked">
            {{ row.url_masked }}
          </div>
          <div v-if="row.notes" class="mt-0.5 truncate text-xs text-gray-400 dark:text-dark-500" :title="row.notes">
            {{ row.notes }}
          </div>
        </div>
      </template>

      <template #cell-nodes="{ row }">
        <div class="text-sm" data-testid="clash-profile-nodes">
          <span class="font-semibold tabular-nums text-gray-900 dark:text-white">{{ row.stats.active }}</span>
          <span class="tabular-nums text-gray-400 dark:text-dark-500">/{{ row.stats.total }}</span>
          <span class="ml-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.clash.profiles.available') }}</span>
          <div class="mt-1 flex max-w-[11rem] flex-wrap items-center gap-x-2 gap-y-0.5 whitespace-nowrap text-xs">
            <span class="inline-flex items-center gap-1 text-emerald-600 dark:text-emerald-400">
              <span class="h-1.5 w-1.5 rounded-full bg-emerald-500" aria-hidden="true"></span>
              {{ t('admin.clash.profiles.healthyCount', { count: row.stats.healthy }) }}
            </span>
            <span v-if="row.stats.unhealthy > 0" class="inline-flex items-center gap-1 text-red-600 dark:text-red-400">
              <span class="h-1.5 w-1.5 rounded-full bg-red-500" aria-hidden="true"></span>
              {{ t('admin.clash.profiles.unhealthyCount', { count: row.stats.unhealthy }) }}
            </span>
            <span v-if="row.stats.missing > 0" class="text-gray-400 dark:text-dark-500">
              {{ t('admin.clash.profiles.missingCount', { count: row.stats.missing }) }}
            </span>
          </div>
          <div
            class="mt-1 inline-flex items-center gap-1 text-xs"
            :class="boundAccounts(row) > 0 ? 'text-gray-600 dark:text-gray-300' : 'text-gray-400 dark:text-dark-500'"
            :title="t('admin.clash.profiles.boundHint')"
            data-testid="clash-profile-bound"
          >
            <Icon name="link" size="xs" />
            {{ t('admin.clash.profiles.boundCount', { count: boundAccounts(row) }) }}
          </div>
        </div>
      </template>

      <template #cell-traffic="{ row }">
        <div v-if="row.total_bytes > 0" class="w-40" data-testid="clash-profile-traffic">
          <div class="flex items-baseline justify-between gap-2 text-xs">
            <span class="whitespace-nowrap text-gray-700 dark:text-gray-300">
              {{ formatBytes(usedBytes(row), 1) }} / {{ formatBytes(row.total_bytes, 1) }}
            </span>
            <span class="tabular-nums text-gray-500 dark:text-dark-400">{{ trafficPercent(row) }}%</span>
          </div>
          <div class="mt-1 h-1.5 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700">
            <div class="h-full rounded-full" :class="trafficBarClass(row)" :style="{ width: `${trafficPercent(row)}%` }"></div>
          </div>
        </div>
        <span v-else class="text-sm text-gray-400 dark:text-dark-500">-</span>
      </template>

      <template #cell-expire_at="{ row }">
        <div v-if="row.expire_at" class="flex flex-col gap-1 text-xs">
          <span class="text-gray-700 dark:text-gray-300" :title="formatDateTime(row.expire_at)">{{ formatDateOnly(row.expire_at) }}</span>
          <span :class="expiryBadgeClass(row.expire_at)" data-testid="clash-profile-expiry">{{ expiryLabel(row.expire_at) }}</span>
        </div>
        <span v-else class="text-sm text-gray-400 dark:text-dark-500">-</span>
      </template>

      <template #cell-last_refresh="{ row }">
        <div class="flex flex-col items-start gap-1">
          <div class="flex items-center">
            <span :class="['status-pill', refreshPillClass(row.last_refresh_status)]" data-testid="clash-profile-refresh-status">
              <span class="h-1.5 w-1.5 rounded-full" :class="refreshDotClass(row.last_refresh_status)" aria-hidden="true"></span>
              {{ refreshStatusLabel(row.last_refresh_status) }}
            </span>
            <HelpTooltip v-if="row.last_refresh_error" width-class="w-80">
              <p class="break-all">{{ row.last_refresh_error }}</p>
              <p v-if="row.last_refresh_status === 'skipped'" class="mt-1.5 text-gray-300">
                {{ t('admin.clash.profiles.skippedHint') }}
              </p>
            </HelpTooltip>
          </div>
          <span class="text-xs text-gray-500 dark:text-dark-400" :title="formatDateTime(row.last_refresh_at) || undefined">
            {{ row.last_refresh_at ? formatRelativeTime(row.last_refresh_at) : t('admin.clash.profiles.neverRefreshed') }}
          </span>
          <span class="inline-flex items-center gap-1 text-xs text-gray-400 dark:text-dark-500" data-testid="clash-profile-interval">
            <Icon name="clock" size="xs" />
            {{ intervalLabel(row.refresh_interval_minutes) }}
          </span>
          <button
            v-if="row.last_refresh_status === 'skipped'"
            type="button"
            class="text-xs font-medium text-amber-700 underline decoration-dashed underline-offset-2 hover:text-amber-800 disabled:cursor-not-allowed disabled:opacity-50 dark:text-amber-300"
            :disabled="readonly || refreshingIds.has(row.id)"
            @click="$emit('force-refresh', row)"
          >
            {{ t('admin.clash.actions.forceRefresh') }}
          </button>
        </div>
      </template>

      <template #cell-enabled="{ row }">
        <Toggle
          :model-value="row.enabled"
          :disabled="readonly || togglingIds.has(row.id)"
          :aria-label="t('admin.clash.profiles.columns.enabled')"
          :class="(readonly || togglingIds.has(row.id)) && 'cursor-not-allowed opacity-50'"
          @update:model-value="$emit('toggle', row, $event)"
        />
      </template>

      <template #cell-actions="{ row }">
        <div class="flex items-center gap-0.5">
          <button
            type="button"
            class="row-action px-1.5 disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="readonly || refreshingIds.has(row.id)"
            :title="t('admin.clash.actions.refreshNow')"
            :aria-label="t('admin.clash.actions.refreshNow')"
            data-testid="clash-profile-refresh"
            @click="$emit('refresh', row)"
          >
            <Icon name="refresh" size="sm" :class="refreshingIds.has(row.id) ? 'animate-spin' : ''" />
          </button>
          <button
            type="button"
            class="row-action px-1.5"
            :title="t('admin.clash.actions.viewNodes')"
            :aria-label="t('admin.clash.actions.viewNodes')"
            data-testid="clash-profile-view-nodes"
            @click="$emit('view-nodes', row)"
          >
            <Icon name="server" size="sm" />
          </button>
          <button
            type="button"
            class="row-action px-1.5 disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="readonly"
            :title="t('common.edit')"
            :aria-label="t('common.edit')"
            data-testid="clash-profile-edit"
            @click="$emit('edit', row)"
          >
            <Icon name="edit" size="sm" />
          </button>
          <button
            type="button"
            class="row-action px-1.5 hover:!bg-red-50 hover:!text-red-700 dark:hover:!bg-red-500/10 dark:hover:!text-red-300"
            :title="t('common.delete')"
            :aria-label="t('common.delete')"
            data-testid="clash-profile-delete"
            @click="$emit('delete', row)"
          >
            <Icon name="trash" size="sm" />
          </button>
        </div>
      </template>

      <template #empty>
        <EmptyState
          :title="t('admin.clash.profiles.empty')"
          :description="t('admin.clash.profiles.emptyHint')"
          :action-text="readonly ? undefined : t('admin.clash.actions.addProfile')"
          @action="$emit('create')"
        />
      </template>
    </DataTable>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import DataTable from '@/components/common/DataTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Column } from '@/components/common/types'
import type { ClashProfile, ClashRefreshStatus, ClashSubscriptionFormat } from '@/types'
import { formatBytes, formatDateOnly, formatDateTime, formatRelativeTime } from '@/utils/format'
import { daysUntil } from '@/utils/proxyExpiry'

withDefaults(
  defineProps<{
    profiles: ClashProfile[]
    loading?: boolean
    readonly?: boolean
    refreshingIds?: Set<number>
    togglingIds?: Set<number>
  }>(),
  {
    loading: false,
    readonly: false,
    refreshingIds: () => new Set<number>(),
    togglingIds: () => new Set<number>()
  }
)

defineEmits<{
  (e: 'create'): void
  (e: 'edit', profile: ClashProfile): void
  (e: 'refresh', profile: ClashProfile): void
  (e: 'force-refresh', profile: ClashProfile): void
  (e: 'toggle', profile: ClashProfile, enabled: boolean): void
  (e: 'view-nodes', profile: ClashProfile): void
  (e: 'delete', profile: ClashProfile): void
}>()

const { t } = useI18n()

const columns = computed<Column[]>(() => [
  { key: 'name', label: t('admin.clash.profiles.columns.name') },
  { key: 'nodes', label: t('admin.clash.profiles.columns.nodes') },
  { key: 'traffic', label: t('admin.clash.profiles.columns.traffic') },
  { key: 'expire_at', label: t('admin.clash.profiles.columns.expire') },
  { key: 'last_refresh', label: t('admin.clash.profiles.columns.lastRefresh') },
  { key: 'enabled', label: t('admin.clash.profiles.columns.enabled') },
  { key: 'actions', label: t('admin.clash.profiles.columns.actions') }
])

const formatLabel = (format: ClashSubscriptionFormat) => {
  switch (format) {
    case 'clash_yaml':
      return 'YAML'
    case 'base64_yaml':
      return 'Base64'
    case 'uri_list':
      return 'URI'
    default:
      return format
  }
}

// Older backends only report bound nodes; with one account per exit they match.
const boundAccounts = (profile: ClashProfile) => profile.stats.bound_accounts ?? profile.stats.bound

const usedBytes = (profile: ClashProfile) => Math.max(0, profile.upload_bytes + profile.download_bytes)

const trafficPercent = (profile: ClashProfile) => {
  if (profile.total_bytes <= 0) return 0
  return Math.min(100, Math.round((usedBytes(profile) / profile.total_bytes) * 100))
}

const trafficBarClass = (profile: ClashProfile) => {
  const percent = trafficPercent(profile)
  if (percent >= 90) return 'bg-red-500'
  if (percent >= 75) return 'bg-amber-500'
  return 'bg-primary-500'
}

/** Subscriptions expire hard, so warn a week ahead and alarm in the last three days. */
const expiryBadgeClass = (expireAt: string) => {
  const days = daysUntil(expireAt)
  if (days <= 3) return 'badge badge-danger w-fit'
  if (days <= 7) return 'badge badge-warning w-fit'
  return 'text-gray-500 dark:text-dark-400'
}

const expiryLabel = (expireAt: string) => {
  if (new Date(expireAt).getTime() <= Date.now()) return t('admin.clash.profiles.expired')
  return t('admin.clash.profiles.expiresInDays', { days: Math.max(1, daysUntil(expireAt)) })
}

const refreshStatusLabel = (status: ClashRefreshStatus) => {
  switch (status) {
    case 'ok':
      return t('admin.clash.profiles.refreshStatus.ok')
    case 'error':
      return t('admin.clash.profiles.refreshStatus.error')
    case 'skipped':
      return t('admin.clash.profiles.refreshStatus.skipped')
    default:
      return t('admin.clash.profiles.refreshStatus.never')
  }
}

const refreshPillClass = (status: ClashRefreshStatus) => {
  switch (status) {
    case 'ok':
      return 'bg-emerald-50 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-500/10 dark:text-emerald-300 dark:ring-emerald-400/20'
    case 'error':
      return 'bg-red-50 text-red-700 ring-red-600/20 dark:bg-red-500/10 dark:text-red-300 dark:ring-red-400/20'
    case 'skipped':
      return 'bg-amber-50 text-amber-700 ring-amber-600/20 dark:bg-amber-500/10 dark:text-amber-300 dark:ring-amber-400/20'
    default:
      return 'bg-gray-100 text-gray-600 ring-gray-500/20 dark:bg-dark-700 dark:text-dark-300 dark:ring-dark-500/30'
  }
}

const refreshDotClass = (status: ClashRefreshStatus) => {
  switch (status) {
    case 'ok':
      return 'bg-emerald-500'
    case 'error':
      return 'bg-red-500'
    case 'skipped':
      return 'bg-amber-500'
    default:
      return 'bg-gray-400'
  }
}

const intervalLabel = (minutes: number) => {
  if (!minutes) return t('admin.clash.interval.manual')
  if (minutes % 60 === 0) return t('admin.clash.interval.everyHours', { hours: minutes / 60 })
  return t('admin.clash.interval.everyMinutes', { minutes })
}
</script>

<style scoped>
.status-pill {
  @apply inline-flex items-center gap-1.5 whitespace-nowrap rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset;
}

.format-badge {
  @apply inline-flex flex-shrink-0 items-center rounded px-1.5 py-px text-[10px] font-medium;
  @apply bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300;
}
</style>
