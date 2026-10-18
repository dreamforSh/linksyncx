<template>
  <AppLayout>
    <div class="w-full min-w-0 space-y-6 pb-8">
      <header
        class="page-header mb-0 rounded-3xl bg-white p-5 shadow-sm ring-1 ring-gray-900/5 dark:bg-dark-800 dark:ring-dark-700 sm:p-6"
      >
        <h1 class="page-title flex items-center gap-2 text-xl font-black text-gray-900 dark:text-white">
          <span class="inline-flex h-8 w-8 items-center justify-center rounded-xl bg-blue-50 text-blue-500 dark:bg-blue-900/30 dark:text-blue-400">
            <Icon name="chart" size="sm" />
          </span>
          {{ t('admin.channelMonitor.title') }}
        </h1>
        <p class="page-description mt-1.5 text-xs text-gray-500 dark:text-gray-400">
          {{
            isV1Mode
              ? t('channelMonitorV2.admin.descriptionV1')
              : t('channelMonitorV2.admin.descriptionV2')
          }}
        </p>
        <div class="mt-4 border-t border-gray-100 pt-4 dark:border-dark-700">
          <div
            class="tabs inline-flex w-full max-w-xl flex-wrap sm:w-auto"
            role="tablist"
            :aria-label="t('channelMonitorV2.admin.tabAria')"
          >
            <button
              type="button"
              role="tab"
              class="tab flex-1 sm:flex-none"
              :class="adminMonitorTab === 'v2' ? 'tab-active' : ''"
              :aria-selected="adminMonitorTab === 'v2'"
              @click="adminMonitorTab = 'v2'"
            >
              {{ t('channelMonitorV2.admin.tabV2') }}
            </button>
            <button
              type="button"
              role="tab"
              class="tab flex-1 sm:flex-none"
              :class="adminMonitorTab === 'legacy' ? 'tab-active' : ''"
              :aria-selected="adminMonitorTab === 'legacy'"
              @click="adminMonitorTab = 'legacy'"
            >
              {{ isV1Mode ? t('channelMonitorV2.admin.tabV1Active') : t('channelMonitorV2.admin.tabV1History') }}
            </button>
          </div>
        </div>
      </header>

      <MonitorSettingsPanel v-if="adminMonitorTab === 'v2'" />

      <TablePageLayout v-else>
      <template #filters>
        <MonitorFiltersBar
          v-model:search="searchQuery"
          v-model:provider="providerFilter"
          v-model:enabled="enabledFilter"
          :loading="loading"
          @reload="reload"
          @create="openCreateDialog"
          @manage-templates="showTemplateManager = true"
          @search-input="handleSearch"
        />
        <!-- 状态汇总：一眼看出有几个渠道异常；只统计当前页时明确标注 -->
        <div v-if="monitors.length" class="mt-3 flex flex-wrap items-center gap-2 text-xs" data-testid="monitor-status-summary">
          <span
            v-for="item in statusSummary"
            :key="item.key"
            class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 font-medium"
            :class="item.count > 0 ? item.activeClass : 'bg-gray-50 text-gray-400 dark:bg-dark-800 dark:text-dark-500'"
          >
            <span class="h-1.5 w-1.5 rounded-full" :class="item.count > 0 ? item.dotClass : 'bg-gray-300 dark:bg-dark-600'"></span>
            {{ item.label }} {{ item.count }}
          </span>
          <span v-if="pagination.total > monitors.length" class="text-gray-400">{{ t('admin.channelMonitor.list.summary.pageScope') }}</span>
        </div>
      </template>

      <template #table>
        <DataTable :columns="columns" :data="monitors" :loading="loading">
          <template #cell-name="{ row, value }">
            <div class="min-w-0" :class="row.enabled ? '' : 'opacity-60'">
              <div class="flex items-center gap-1.5">
                <span class="font-medium text-gray-900 dark:text-white">{{ value }}</span>
                <span v-if="!row.enabled" class="rounded bg-gray-100 px-1.5 py-0.5 text-[10px] font-medium text-gray-500 dark:bg-dark-700 dark:text-gray-400">
                  {{ t('admin.channelMonitor.list.disabled') }}
                </span>
                <HelpTooltip v-if="row.api_key_decrypt_failed" :content="t('admin.channelMonitor.apiKeyDecryptFailed')">
                  <template #trigger>
                    <Icon name="exclamationTriangle" size="sm" class="text-red-500" />
                  </template>
                </HelpTooltip>
              </div>
              <div class="mt-0.5 max-w-[170px] truncate text-[11px] text-gray-400 dark:text-dark-400" :title="row.endpoint">
                {{ [row.group_name, endpointHost(row.endpoint)].filter(Boolean).join(' · ') }}
              </div>
            </div>
          </template>

          <template #cell-provider="{ row }">
            <span class="inline-flex items-center rounded-md px-2 py-0.5 text-xs font-medium" :class="providerBadgeClass(row.provider)">
              {{ providerLabel(row.provider) }}
            </span>
            <!-- 三种检测模式并列展示，quota 系配额数据源与纯探活一眼可分 -->
            <span class="ml-1 inline-flex items-center rounded-md px-2 py-0.5 text-xs font-medium" :class="checkModeBadgeClass(row.check_mode)">
              {{ checkModeLabel(row.check_mode) }}
            </span>
          </template>

          <template #cell-primary_model="{ row }">
            <MonitorPrimaryModelCell :row="row" />
          </template>

          <template #cell-availability_7d="{ row }">
            <div v-if="row.primary_status" class="w-[72px]">
              <span class="text-sm font-medium tabular-nums" :class="availabilityTextClass(row.availability_7d)">{{ formatAvailability(row) }}</span>
              <div class="mt-1 h-1 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700">
                <div class="h-full rounded-full" :class="availabilityBarClass(row.availability_7d)" :style="{ width: `${Math.min(100, Math.max(0, row.availability_7d))}%` }"></div>
              </div>
            </div>
            <span v-else class="text-sm text-gray-400">-</span>
          </template>

          <template #cell-last_checked="{ row }">
            <div class="text-xs" :title="row.last_checked_at || ''">
              <span v-if="!row.last_checked_at" class="text-gray-400">{{ t('admin.channelMonitor.list.neverChecked') }}</span>
              <span v-else-if="isStale(row)" class="inline-flex items-center gap-1 font-medium text-amber-600 dark:text-amber-400" :title="t('admin.channelMonitor.list.staleHint')">
                <Icon name="exclamationTriangle" size="xs" />
                {{ formatRelativeTime(row.last_checked_at) }}
              </span>
              <span v-else class="text-gray-700 dark:text-gray-300">{{ formatRelativeTime(row.last_checked_at) }}</span>
              <div class="text-[11px] text-gray-400 dark:text-dark-400">
                {{ t('admin.channelMonitor.list.intervalEvery', { interval: formatInterval(row.interval_seconds) }) }}
              </div>
            </div>
          </template>

          <template #cell-latency="{ row }">
            <span class="text-sm text-gray-900 dark:text-gray-100">{{ formatLatency(row.primary_latency_ms) }}</span>
          </template>

          <template #cell-enabled="{ row }">
            <Toggle :modelValue="row.enabled" @update:modelValue="toggleEnabled(row)" />
          </template>

          <template #cell-actions="{ row }">
            <MonitorActionsCell
              :row="row"
              :running="runningId === row.id"
              :duplicating="duplicatingIds.has(row.id)"
              @run="handleRunNow"
              @duplicate="handleDuplicate"
              @edit="openEditDialog"
              @delete="handleDelete"
            />
          </template>

          <template #empty>
            <EmptyState
              :title="t('admin.channelMonitor.noMonitorsYet')"
              :description="t('admin.channelMonitor.createFirstMonitor')"
              :action-text="t('admin.channelMonitor.createButton')"
              @action="openCreateDialog"
            />
          </template>
        </DataTable>
      </template>

      <template #pagination>
        <Pagination
          v-if="pagination.total > 0"
          :page="pagination.page"
          :total="pagination.total"
          :page-size="pagination.page_size"
          @update:page="onPageChange"
          @update:pageSize="onPageSizeChange"
        />
      </template>
      </TablePageLayout>
    </div>

    <MonitorFormDialog
      :show="showDialog"
      :monitor="editing"
      @close="closeDialog"
      @saved="reload"
    />

    <MonitorTemplateManagerDialog
      :show="showTemplateManager"
      @close="showTemplateManager = false"
      @updated="reload"
    />

    <MonitorRunResultDialog
      :show="showRunResult"
      :results="runResults"
      @close="showRunResult = false"
    />

    <ConfirmDialog
      :show="showDeleteDialog"
      :title="t('common.delete')"
      :message="deleteConfirmMessage"
      :confirm-text="t('common.delete')"
      :cancel-text="t('common.cancel')"
      :danger="true"
      @confirm="confirmDelete"
      @cancel="showDeleteDialog = false"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { adminAPI } from '@/api/admin'
import type {
  ChannelMonitor,
  CheckResult,
  ListParams,
  Provider,
} from '@/api/admin/channelMonitor'
import type { Column } from '@/components/common/types'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Icon from '@/components/icons/Icon.vue'
import Toggle from '@/components/common/Toggle.vue'
import MonitorFiltersBar from '@/components/admin/monitor/MonitorFiltersBar.vue'
import MonitorFormDialog from '@/components/admin/monitor/MonitorFormDialog.vue'
import MonitorTemplateManagerDialog from '@/components/admin/monitor/MonitorTemplateManagerDialog.vue'
import MonitorRunResultDialog from '@/components/admin/monitor/MonitorRunResultDialog.vue'
import MonitorPrimaryModelCell from '@/components/admin/monitor/MonitorPrimaryModelCell.vue'
import MonitorActionsCell from '@/components/admin/monitor/MonitorActionsCell.vue'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { useChannelMonitorFormat } from '@/composables/useChannelMonitorFormat'
import MonitorSettingsPanel from '@/features/channel-monitor-v2/MonitorSettingsPanel.vue'
import { isChannelMonitorV1Mode } from '@/utils/featureFlags'

const { t } = useI18n()
const appStore = useAppStore()
const isV1Mode = computed(() => isChannelMonitorV1Mode())
const adminMonitorTab = ref<'v2' | 'legacy'>(isChannelMonitorV1Mode() ? 'legacy' : 'v2')
const {
  providerLabel,
  providerBadgeClass,
  checkModeLabel,
  checkModeBadgeClass,
  formatLatency,
  formatAvailability,
  formatRelativeTime,
} = useChannelMonitorFormat()

/** 本页各状态计数（停用单独计，不再计入其最后状态）。 */
const statusSummary = computed(() => {
  const counts = { operational: 0, degraded: 0, failed: 0, error: 0, disabled: 0 }
  for (const row of monitors.value) {
    if (!row.enabled) counts.disabled++
    else if (row.primary_status) counts[row.primary_status]++
  }
  const style = {
    operational: ['bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300', 'bg-emerald-500'],
    degraded: ['bg-amber-50 text-amber-700 dark:bg-amber-500/10 dark:text-amber-300', 'bg-amber-500'],
    failed: ['bg-red-50 text-red-700 dark:bg-red-500/10 dark:text-red-300', 'bg-red-500'],
    error: ['bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-gray-300', 'bg-gray-500'],
    disabled: ['bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-400', 'bg-gray-400'],
  } as const
  return (Object.keys(counts) as Array<keyof typeof counts>).map((key) => ({
    key,
    count: counts[key],
    label: t(`admin.channelMonitor.list.summary.${key}`),
    activeClass: style[key][0],
    dotClass: style[key][1],
  }))
})

function endpointHost(endpoint: string): string {
  try {
    return new URL(endpoint).host
  } catch {
    return ''
  }
}

function formatInterval(seconds: number): string {
  if (!seconds || seconds < 60) return `${seconds || 0}s`
  if (seconds % 3600 === 0) return `${seconds / 3600}h`
  if (seconds % 60 === 0) return `${seconds / 60}m`
  return `${seconds}s`
}

/** 超过 3 个检测周期（含抖动余量）没有新结果，说明调度停了或一直被跳过。 */
function isStale(row: ChannelMonitor): boolean {
  if (!row.enabled || !row.last_checked_at) return false
  const last = Date.parse(row.last_checked_at)
  if (Number.isNaN(last)) return false
  const budget = (Math.max(30, row.interval_seconds || 60) * 3 + (row.jitter_seconds || 0)) * 1000
  return Date.now() - last > budget
}

function availabilityTextClass(value: number): string {
  if (value >= 99) return 'text-emerald-600 dark:text-emerald-400'
  if (value >= 95) return 'text-amber-600 dark:text-amber-400'
  return 'text-red-600 dark:text-red-400'
}

function availabilityBarClass(value: number): string {
  if (value >= 99) return 'bg-emerald-500'
  if (value >= 95) return 'bg-amber-500'
  return 'bg-red-500'
}

const monitors = ref<ChannelMonitor[]>([])
const loading = ref(false)
const runningId = ref<number | null>(null)
const searchQuery = ref('')
const providerFilter = ref<Provider | ''>('')
const enabledFilter = ref<'' | 'true' | 'false'>('')
const pagination = reactive({ page: 1, page_size: getPersistedPageSize(), total: 0 })

const showDialog = ref(false)
const showTemplateManager = ref(false)
const editing = ref<ChannelMonitor | null>(null)
const showDeleteDialog = ref(false)
const deleting = ref<ChannelMonitor | null>(null)
const showRunResult = ref(false)
const runResults = ref<CheckResult[]>([])
const duplicatingIds = reactive(new Set<number>())

let abortController: AbortController | null = null
let searchTimeout: ReturnType<typeof setTimeout> | null = null

const columns = computed<Column[]>(() => [
  { key: 'name', label: t('admin.channelMonitor.columns.name'), sortable: false },
  { key: 'provider', label: t('admin.channelMonitor.columns.provider'), sortable: false },
  { key: 'primary_model', label: t('admin.channelMonitor.columns.primaryModel'), sortable: false },
  { key: 'availability_7d', label: t('admin.channelMonitor.columns.availability7d'), sortable: false },
  { key: 'latency', label: t('admin.channelMonitor.columns.latency'), sortable: false },
  { key: 'last_checked', label: t('admin.channelMonitor.columns.lastChecked'), sortable: false },
  { key: 'enabled', label: t('admin.channelMonitor.columns.enabled'), sortable: false },
  { key: 'actions', label: t('admin.channelMonitor.columns.actions'), sortable: false },
])

const deleteConfirmMessage = computed(() => {
  const name = deleting.value?.name || ''
  return t('admin.channelMonitor.deleteConfirm', { name })
})

async function reload(silent = false) {
  if (abortController) abortController.abort()
  const ctrl = new AbortController()
  abortController = ctrl
  // 静默刷新（自动轮询）不切换表格 loading，避免每分钟整表闪烁。
  if (silent !== true) loading.value = true
  try {
    const params: ListParams = {
      page: pagination.page,
      page_size: pagination.page_size,
    }
    if (providerFilter.value) params.provider = providerFilter.value
    if (enabledFilter.value === 'true') params.enabled = true
    if (enabledFilter.value === 'false') params.enabled = false
    if (searchQuery.value.trim()) params.search = searchQuery.value.trim()

    const res = await adminAPI.channelMonitor.list(params, { signal: ctrl.signal })
    if (ctrl.signal.aborted || abortController !== ctrl) return
    monitors.value = res.items || []
    pagination.total = res.total
  } catch (err: unknown) {
    const e = err as { name?: string; code?: string }
    if (e?.name === 'AbortError' || e?.code === 'ERR_CANCELED') return
    appStore.showError(extractApiErrorMessage(err, t('admin.channelMonitor.loadError')))
  } finally {
    if (abortController === ctrl) {
      loading.value = false
      abortController = null
    }
  }
}

function handleSearch() {
  if (searchTimeout) clearTimeout(searchTimeout)
  searchTimeout = setTimeout(() => {
    pagination.page = 1
    reload()
  }, 300)
}

function onPageChange(page: number) {
  pagination.page = page
  reload()
}

function onPageSizeChange(size: number) {
  pagination.page_size = size
  pagination.page = 1
  reload()
}

function openCreateDialog() {
  editing.value = null
  showDialog.value = true
}

function openEditDialog(row: ChannelMonitor) {
  editing.value = row
  showDialog.value = true
}

function closeDialog() {
  showDialog.value = false
  editing.value = null
}

async function toggleEnabled(row: ChannelMonitor) {
  const next = !row.enabled
  try {
    await adminAPI.channelMonitor.update(row.id, { enabled: next })
    row.enabled = next
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  }
}

async function handleRunNow(row: ChannelMonitor) {
  if (!isV1Mode.value) {
    appStore.showError(t('admin.channelMonitor.runFailed'))
    return
  }
  if (runningId.value != null) return
  runningId.value = row.id
  try {
    const res = await adminAPI.channelMonitor.runNow(row.id)
    runResults.value = res.results || []
    showRunResult.value = true
    appStore.showSuccess(t('admin.channelMonitor.runSuccess'))
    // Refresh row to get latest status from backend
    void reload()
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('admin.channelMonitor.runFailed')))
  } finally {
    runningId.value = null
  }
}

async function handleDuplicate(row: ChannelMonitor) {
  if (row.api_key_decrypt_failed) {
    appStore.showError(t('admin.channelMonitor.duplicateKeyUnavailable'))
    return
  }
  if (duplicatingIds.has(row.id)) return

  duplicatingIds.add(row.id)
  try {
    const duplicate = await adminAPI.channelMonitor.duplicate(row.id)
    appStore.showSuccess(t('admin.channelMonitor.duplicateSuccess', { name: duplicate.name }))
    await reload()
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('admin.channelMonitor.duplicateFailed')))
  } finally {
    duplicatingIds.delete(row.id)
  }
}

function handleDelete(row: ChannelMonitor) {
  deleting.value = row
  showDeleteDialog.value = true
}

async function confirmDelete() {
  if (!deleting.value) return
  try {
    await adminAPI.channelMonitor.del(deleting.value.id)
    appStore.showSuccess(t('admin.channelMonitor.deleteSuccess'))
    showDeleteDialog.value = false
    deleting.value = null
    reload()
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  }
}

watch(adminMonitorTab, (tab) => {
  if (tab === 'legacy' && monitors.value.length === 0) void reload()
})
// 列表每分钟静默刷新状态/可用率/最近检测；弹窗打开或页面在后台时跳过。
const AUTO_REFRESH_MS = 60_000
const autoRefreshTimer = window.setInterval(() => {
  if (adminMonitorTab.value !== 'legacy' || document.hidden || loading.value) return
  if (showDialog.value || showDeleteDialog.value || showTemplateManager.value || showRunResult.value) return
  void reload(true)
}, AUTO_REFRESH_MS)
onMounted(() => {
  if (adminMonitorTab.value === 'legacy') void reload()
})
onUnmounted(() => {
  window.clearInterval(autoRefreshTimer)
  if (searchTimeout) clearTimeout(searchTimeout)
  abortController?.abort()
})
</script>
