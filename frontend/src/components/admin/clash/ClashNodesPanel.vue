<template>
  <section ref="rootRef" class="card scroll-mt-20 overflow-hidden" aria-labelledby="clash-nodes-title" data-testid="clash-nodes-panel">
    <div class="card-section-header">
      <div>
        <h2 id="clash-nodes-title" class="card-section-title">{{ t('admin.clash.nodes.title') }}</h2>
        <p class="card-section-subtitle">{{ t('admin.clash.nodes.subtitle', { count: pagination.total }) }}</p>
      </div>
      <button
        type="button"
        class="btn btn-secondary btn-sm px-2.5"
        :disabled="loading"
        :title="t('common.refresh')"
        :aria-label="t('common.refresh')"
        @click="load"
      >
        <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
      </button>
    </div>

    <div class="toolbar-compact flex flex-wrap items-center gap-2 px-5 pb-3">
      <div class="relative w-full sm:w-64">
        <Icon
          name="search"
          size="sm"
          class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 dark:text-dark-400"
        />
        <input
          v-model="filters.search"
          type="text"
          class="input pl-9 pr-8"
          :placeholder="t('admin.clash.nodes.searchPlaceholder')"
          :aria-label="t('admin.clash.nodes.searchPlaceholder')"
          data-testid="clash-nodes-search"
          @input="onSearchInput"
        />
        <button
          v-if="filters.search"
          type="button"
          class="absolute right-2 top-1/2 flex h-5 w-5 -translate-y-1/2 items-center justify-center rounded text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-600 dark:hover:bg-dark-700 dark:hover:text-gray-200"
          :aria-label="t('common.clear')"
          @click="clearSearch"
        >
          <Icon name="x" size="xs" :stroke-width="2" />
        </button>
      </div>
      <div class="w-full sm:w-48">
        <Select
          v-model="filters.profileId"
          :options="profileOptions"
          :aria-label="t('admin.clash.nodes.filters.profile')"
          data-testid="clash-nodes-profile-filter"
          @change="applyFilters"
        />
      </div>
      <div class="w-full sm:w-32">
        <Select
          v-model="filters.status"
          :options="statusOptions"
          :aria-label="t('admin.clash.nodes.filters.status')"
          @change="applyFilters"
        />
      </div>
      <div class="w-full sm:w-32">
        <Select
          v-model="filters.health"
          :options="healthOptions"
          :aria-label="t('admin.clash.nodes.filters.health')"
          @change="applyFilters"
        />
      </div>
      <SegmentedControl
        v-model="filters.bound"
        :options="boundOptions"
        :aria-label="t('admin.clash.nodes.filters.bound')"
        test-id="clash-nodes-bound-filter"
        @change="applyFilters"
      />
    </div>

    <div
      v-if="loadError && nodes.length === 0"
      role="alert"
      class="mx-5 mb-3 rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-500/10 dark:text-red-300"
    >
      {{ loadError }}
    </div>

    <div
      v-if="selectedIds.length > 0"
      class="flex flex-wrap items-center gap-2 border-y border-primary-200/70 bg-primary-50/80 px-5 py-2 dark:border-primary-500/20 dark:bg-primary-500/10"
      role="region"
      :aria-label="t('admin.clash.nodes.selectedCount', { count: selectedIds.length })"
      data-testid="clash-nodes-bulk-bar"
    >
      <span class="mr-1 text-sm font-medium text-primary-900 dark:text-primary-100">
        {{ t('admin.clash.nodes.selectedCount', { count: selectedIds.length }) }}
      </span>
      <button
        type="button"
        class="btn btn-secondary btn-sm"
        :disabled="readonly || bulkTesting || bulkProbing"
        data-testid="clash-nodes-bulk-latency"
        @click="runLatencyTest(selectedIds)"
      >
        <Icon name="bolt" size="sm" :class="bulkTesting ? 'animate-pulse' : ''" />
        {{ bulkTesting ? t('admin.clash.nodes.testingLatency') : t('admin.clash.actions.testLatency') }}
      </button>
      <button
        type="button"
        class="btn btn-secondary btn-sm"
        :disabled="readonly || bulkTesting || bulkProbing"
        data-testid="clash-nodes-bulk-probe"
        @click="runExitProbe(selectedIds)"
      >
        <Icon name="globe" size="sm" :class="bulkProbing ? 'animate-pulse' : ''" />
        {{ bulkProbing ? t('admin.clash.nodes.probingExit') : t('admin.clash.actions.probeExit') }}
      </button>
      <button
        type="button"
        class="ml-auto rounded-md px-2 py-1 text-sm font-medium text-primary-800 transition-colors hover:bg-primary-100 dark:text-primary-200 dark:hover:bg-primary-500/20"
        @click="selectedIds = []"
      >
        {{ t('admin.clash.actions.clearSelection') }}
      </button>
    </div>

    <DataTable
      :columns="columns"
      :data="nodes"
      :loading="loading"
      row-key="id"
      selectable
      :selected-keys="selectedIds"
      :selection-label="(row: ClashNode) => row.name"
      :expandable-actions="false"
      @update:selected-keys="onSelectionChange"
    >
      <template #cell-name="{ row }">
        <div class="min-w-[12rem] max-w-[15rem]">
          <div class="flex items-center gap-1.5">
            <span class="truncate font-medium text-gray-900 dark:text-white" :title="row.name">{{ row.name }}</span>
            <span class="type-badge">{{ row.type }}</span>
          </div>
          <code
            class="mt-0.5 block truncate font-mono text-xs text-gray-600 dark:text-gray-400"
            :title="`${row.server}:${row.server_port}`"
            data-testid="clash-node-server"
          >{{ row.server }}:{{ row.server_port }}</code>
          <div class="mt-0.5 flex items-center gap-1.5 text-xs text-gray-400 dark:text-dark-500">
            <span class="truncate" :title="row.profile_name">{{ row.profile_name }}</span>
            <span
              v-if="row.listen_port"
              class="flex-shrink-0 font-mono"
              :title="t('admin.clash.nodes.columns.listenPort')"
              data-testid="clash-node-listen-port"
            >· {{ t('admin.clash.nodes.listenPort', { port: row.listen_port }) }}</span>
          </div>
        </div>
      </template>

      <template #cell-status="{ row }">
        <div class="flex max-w-[12rem] flex-col items-start gap-1 whitespace-normal">
          <span :class="['status-pill', statusPillClass(row.status)]" data-testid="clash-node-status">
            <span class="h-1.5 w-1.5 rounded-full" :class="statusDotClass(row.status)" aria-hidden="true"></span>
            {{ statusLabel(row.status) }}
          </span>
          <span class="flex flex-wrap items-center gap-x-1.5 text-xs" :title="healthTitle(row)" data-testid="clash-node-health">
            <span class="h-2 w-2 flex-shrink-0 rounded-full" :class="healthDotClass(row)" aria-hidden="true"></span>
            <span :class="healthTextClass(row)">{{ healthLabel(row) }}</span>
            <span v-if="row.health_status === 'unhealthy' && row.consecutive_failures > 0" class="text-red-600 dark:text-red-400">
              · {{ t('admin.clash.nodes.consecutiveFailures', { count: row.consecutive_failures }) }}
            </span>
            <span v-else-if="row.last_checked_at" class="text-gray-400 dark:text-dark-500">
              · {{ formatRelativeTime(row.last_checked_at) }}
            </span>
          </span>
          <span
            v-if="row.status === 'missing' && row.missing_since"
            class="text-xs text-gray-500 dark:text-dark-400"
            :title="formatDateTime(row.missing_since)"
          >{{ t('admin.clash.nodes.missingSince', { time: formatRelativeTime(row.missing_since) }) }}</span>
          <span
            v-if="row.status !== 'active' && row.status_reason"
            class="line-clamp-2 break-words text-xs"
            :class="row.status === 'invalid' ? 'text-red-600 dark:text-red-400' : 'text-gray-500 dark:text-dark-400'"
            :title="row.status_reason"
          >{{ localizeClashUnavailableReason(row.status_reason, t) }}</span>
        </div>
      </template>

      <template #cell-exit="{ row }">
        <div class="min-w-[9rem] max-w-[13rem] whitespace-normal" data-testid="clash-node-exit">
          <div v-if="row.exit_ip" class="flex items-center gap-1.5">
            <CountryFlag :code="row.exit_country_code" :label="row.exit_country" />
            <span class="font-mono text-xs text-gray-900 dark:text-gray-100">{{ row.exit_ip }}</span>
          </div>
          <div v-else class="text-xs italic text-gray-400 dark:text-dark-500">{{ t('admin.clash.nodes.exitUnprobed') }}</div>
          <div v-if="exitLocation(row)" class="mt-0.5 truncate text-xs text-gray-500 dark:text-dark-400">{{ exitLocation(row) }}</div>
          <div
            v-if="row.exit_status === 'changed'"
            class="mt-1 flex flex-wrap items-center gap-x-1.5 gap-y-1 rounded-md bg-amber-50 px-1.5 py-1 text-xs text-amber-800 dark:bg-amber-500/10 dark:text-amber-300"
            data-testid="clash-node-exit-changed"
          >
            <Icon name="exclamationTriangle" size="xs" class="flex-shrink-0" />
            <span>{{ t('admin.clash.nodes.pendingExit', { ip: row.exit_pending_ip || '-' }) }}</span>
            <button
              type="button"
              class="font-semibold underline decoration-dotted underline-offset-2 hover:text-amber-900 dark:hover:text-amber-200"
              data-testid="clash-node-accept-exit"
              @click="pendingAccept = row"
            >
              {{ t('admin.clash.actions.acceptExit') }}
            </button>
          </div>
          <div v-else-if="row.exit_status === 'stale'" class="mt-0.5 text-xs text-amber-600 dark:text-amber-400">
            {{ t('admin.clash.nodes.exitStale') }}
          </div>
        </div>
      </template>

      <template #cell-platforms="{ row }">
        <div class="grid w-max grid-cols-2 gap-1" data-testid="clash-node-platforms">
          <span
            v-for="platform in CHECK_PLATFORMS"
            :key="platform"
            :class="['platform-badge', platformBadgeClass(row.platform_checks.results[platform])]"
            :title="platformTitle(row, platform)"
          >{{ PLATFORM_LABELS[platform] }}</span>
        </div>
      </template>

      <template #cell-accounts="{ row }">
        <div v-if="row.accounts.length > 0" class="flex items-center gap-1" :title="accountNames(row)" data-testid="clash-node-accounts">
          <span
            class="inline-block max-w-[8rem] truncate rounded bg-gray-100 px-1.5 py-0.5 align-middle text-xs text-gray-700 dark:bg-dark-700 dark:text-gray-300"
          >{{ row.accounts[0].name }}<template v-if="row.accounts[0].is_shadow"> · {{ t('admin.clash.nodes.shadowTag') }}</template></span>
          <span v-if="row.accounts.length > 1" class="flex-shrink-0 text-xs text-gray-500 dark:text-dark-400">+{{ row.accounts.length - 1 }}</span>
        </div>
        <span v-else class="text-sm text-gray-400 dark:text-dark-500">-</span>
      </template>

      <template #cell-actions="{ row }">
        <div class="flex items-center gap-0.5">
          <button
            type="button"
            class="row-action px-1.5 disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="readonly || busyIds.has(row.id) || row.status === 'invalid'"
            :title="t('admin.clash.actions.testLatency')"
            :aria-label="t('admin.clash.actions.testLatency')"
            data-testid="clash-node-latency"
            @click="runLatencyTest([row.id])"
          >
            <Icon name="bolt" size="sm" :class="busyIds.has(row.id) ? 'animate-pulse' : ''" />
          </button>
          <button
            v-if="row.status === 'disabled'"
            type="button"
            class="row-action px-1.5 hover:!bg-emerald-50 hover:!text-emerald-700 disabled:cursor-not-allowed disabled:opacity-50 dark:hover:!bg-emerald-500/10 dark:hover:!text-emerald-300"
            :disabled="busyIds.has(row.id)"
            :title="t('admin.clash.actions.enable')"
            :aria-label="t('admin.clash.actions.enable')"
            data-testid="clash-node-enable"
            @click="setNodeEnabled(row, true)"
          >
            <Icon name="checkCircle" size="sm" />
          </button>
          <button
            v-else-if="row.status === 'active'"
            type="button"
            class="row-action px-1.5 hover:!bg-amber-50 hover:!text-amber-700 disabled:cursor-not-allowed disabled:opacity-50 dark:hover:!bg-amber-500/10 dark:hover:!text-amber-300"
            :disabled="busyIds.has(row.id)"
            :title="t('admin.clash.actions.disable')"
            :aria-label="t('admin.clash.actions.disable')"
            data-testid="clash-node-disable"
            @click="pendingDisable = row"
          >
            <Icon name="ban" size="sm" />
          </button>
        </div>
      </template>

      <template #empty>
        <EmptyState :title="t('admin.clash.nodes.empty')" :description="t('admin.clash.nodes.emptyHint')" />
      </template>
    </DataTable>

    <Pagination
      v-if="pagination.total > 0"
      :page="pagination.page"
      :total="pagination.total"
      :page-size="pagination.page_size"
      @update:page="handlePageChange"
      @update:pageSize="handlePageSizeChange"
    />

    <ConfirmDialog
      :show="pendingDisable !== null"
      :title="t('admin.clash.nodes.disableConfirm.title')"
      :message="t('admin.clash.nodes.disableConfirm.message', { name: pendingDisable?.name ?? '' })"
      :confirm-text="t('admin.clash.actions.disable')"
      danger
      @confirm="confirmDisable"
      @cancel="pendingDisable = null"
    >
      <div
        v-if="pendingDisable && pendingDisable.accounts.length > 0"
        class="rounded-lg border border-amber-200 bg-amber-50 p-3 text-sm text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200"
        data-testid="clash-node-disable-bound"
      >
        <p>{{ t('admin.clash.nodes.disableConfirm.bound', { count: pendingDisable.accounts.length }) }}</p>
        <div class="mt-2 flex flex-wrap gap-1">
          <span
            v-for="account in pendingDisable.accounts"
            :key="account.id"
            class="rounded bg-white/70 px-1.5 py-0.5 text-xs font-medium dark:bg-dark-800/60"
          >{{ account.name }}</span>
        </div>
      </div>
    </ConfirmDialog>

    <ConfirmDialog
      :show="pendingAccept !== null"
      :title="t('admin.clash.nodes.acceptConfirm.title')"
      :message="t('admin.clash.nodes.acceptConfirm.message', {
        name: pendingAccept?.name ?? '',
        from: pendingAccept?.exit_ip || '-',
        to: pendingAccept?.exit_pending_ip || '-'
      })"
      :confirm-text="t('admin.clash.actions.acceptExit')"
      @confirm="confirmAcceptExit"
      @cancel="pendingAccept = null"
    />
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import Select from '@/components/common/Select.vue'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import CountryFlag from '@/components/common/CountryFlag.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Column } from '@/components/common/types'
import type {
  ClashHealthStatus,
  ClashNode,
  ClashNodeListFilters,
  ClashNodeStatus,
  ClashPlatform,
  ClashPlatformCheckResult,
  ClashProfile
} from '@/types'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { extractApiErrorMessage } from '@/utils/apiError'
import { clashErrorMessage, localizeClashUnavailableReason } from '@/utils/clash'
import { formatDateTime, formatRelativeTime } from '@/utils/format'

type BoundFilter = 'all' | 'bound' | 'unbound'

const props = withDefaults(
  defineProps<{
    profiles: ClashProfile[]
    readonly?: boolean
  }>(),
  { readonly: false }
)

const emit = defineEmits<{ (e: 'changed'): void }>()

const { t } = useI18n()
const appStore = useAppStore()

const CHECK_PLATFORMS: ClashPlatform[] = ['openai', 'anthropic', 'gemini', 'grok']
const PLATFORM_LABELS: Record<ClashPlatform, string> = {
  openai: 'OpenAI',
  anthropic: 'Anthropic',
  gemini: 'Gemini',
  grok: 'Grok'
}

const rootRef = ref<HTMLElement | null>(null)
const nodes = ref<ClashNode[]>([])
const loading = ref(false)
const loadError = ref('')
const selectedIds = ref<number[]>([])
const bulkTesting = ref(false)
const bulkProbing = ref(false)
const busyIds = ref(new Set<number>())
const pendingDisable = ref<ClashNode | null>(null)
const pendingAccept = ref<ClashNode | null>(null)

const filters = reactive({
  search: '',
  profileId: null as number | null,
  status: '' as '' | ClashNodeStatus,
  health: '' as '' | ClashHealthStatus,
  bound: 'all' as BoundFilter
})

const pagination = reactive({
  page: 1,
  page_size: getPersistedPageSize(),
  total: 0,
  pages: 0
})

// Type, server:port and the listener port live in the node cell; health shares the status cell.
const columns = computed<Column[]>(() => [
  { key: 'name', label: t('admin.clash.nodes.columns.name') },
  { key: 'status', label: t('admin.clash.nodes.columns.status') },
  { key: 'exit', label: t('admin.clash.nodes.columns.exit') },
  { key: 'platforms', label: t('admin.clash.nodes.columns.platforms') },
  { key: 'accounts', label: t('admin.clash.nodes.columns.accounts') },
  { key: 'actions', label: t('admin.clash.nodes.columns.actions') }
])

const profileOptions = computed(() => [
  { value: null, label: t('admin.clash.nodes.filters.allProfiles') },
  ...props.profiles.map((profile) => ({ value: profile.id, label: profile.name }))
])

const statusOptions = computed(() => [
  { value: '', label: t('admin.clash.nodes.filters.allStatus') },
  { value: 'active', label: t('admin.clash.nodeStatus.active') },
  { value: 'missing', label: t('admin.clash.nodeStatus.missing') },
  { value: 'disabled', label: t('admin.clash.nodeStatus.disabled') },
  { value: 'invalid', label: t('admin.clash.nodeStatus.invalid') }
])

const healthOptions = computed(() => [
  { value: '', label: t('admin.clash.nodes.filters.allHealth') },
  { value: 'healthy', label: t('admin.clash.health.healthy') },
  { value: 'unhealthy', label: t('admin.clash.health.unhealthy') },
  { value: 'unknown', label: t('admin.clash.health.unknown') }
])

const boundOptions = computed(() => [
  { value: 'all' as BoundFilter, label: t('admin.clash.nodes.filters.boundAll') },
  { value: 'bound' as BoundFilter, label: t('admin.clash.nodes.filters.boundOnly') },
  { value: 'unbound' as BoundFilter, label: t('admin.clash.nodes.filters.unboundOnly') }
])

const describeError = (error: unknown, fallbackKey: string) =>
  clashErrorMessage(error, t) ?? extractApiErrorMessage(error, t(fallbackKey))

const buildFilters = (): ClashNodeListFilters => ({
  profile_id: filters.profileId ?? undefined,
  status: filters.status || undefined,
  health: filters.health || undefined,
  bound: filters.bound === 'all' ? undefined : filters.bound === 'bound',
  search: filters.search.trim() || undefined
})

let abortController: AbortController | null = null

const isAbortError = (error: unknown) => {
  if (!error || typeof error !== 'object') return false
  const maybe = error as { name?: string; code?: string }
  return maybe.name === 'AbortError' || maybe.code === 'ERR_CANCELED' || maybe.name === 'CanceledError'
}

async function load() {
  abortController?.abort()
  const controller = new AbortController()
  abortController = controller
  loading.value = true
  try {
    const response = await adminAPI.clash.listNodes(pagination.page, pagination.page_size, buildFilters(), {
      signal: controller.signal
    })
    if (controller.signal.aborted || abortController !== controller) return
    nodes.value = Array.isArray(response?.items) ? response.items : []
    pagination.total = response?.total ?? 0
    pagination.pages = response?.pages ?? 0
    loadError.value = ''
  } catch (error) {
    if (isAbortError(error)) return
    loadError.value = describeError(error, 'admin.clash.nodes.loadFailed')
  } finally {
    if (abortController === controller) {
      loading.value = false
      abortController = null
    }
  }
}

function applyFilters() {
  pagination.page = 1
  selectedIds.value = []
  void load()
}

let searchTimer: ReturnType<typeof setTimeout> | undefined
function onSearchInput() {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(applyFilters, 300)
}

function clearSearch() {
  filters.search = ''
  applyFilters()
}

function handlePageChange(page: number) {
  pagination.page = page
  void load()
}

function handlePageSizeChange(pageSize: number) {
  pagination.page_size = pageSize
  pagination.page = 1
  void load()
}

function onSelectionChange(keys: Array<string | number>) {
  selectedIds.value = keys.map((key) => Number(key)).filter((id) => Number.isFinite(id))
}

function markBusy(ids: number[], busy: boolean) {
  const next = new Set(busyIds.value)
  for (const id of ids) {
    if (busy) next.add(id)
    else next.delete(id)
  }
  busyIds.value = next
}

async function runLatencyTest(ids: number[]) {
  if (ids.length === 0 || props.readonly) return
  const bulk = ids.length > 1 || ids === selectedIds.value
  if (bulk) bulkTesting.value = true
  markBusy(ids, true)
  try {
    const results = await adminAPI.clash.testNodesLatency({ node_ids: [...ids] })
    const success = results.filter((result) => result.success).length
    const failed = results.length - success
    if (failed === 0) {
      appStore.showSuccess(t('admin.clash.nodes.latencyDone', { success, failed }))
    } else {
      appStore.showWarning(t('admin.clash.nodes.latencyDone', { success, failed }))
    }
    await load()
    emit('changed')
  } catch (error) {
    appStore.showError(describeError(error, 'admin.clash.nodes.latencyFailed'))
  } finally {
    markBusy(ids, false)
    if (bulk) bulkTesting.value = false
  }
}

async function runExitProbe(ids: number[]) {
  if (ids.length === 0 || props.readonly) return
  bulkProbing.value = true
  markBusy(ids, true)
  try {
    const results = await adminAPI.clash.probeNodesExit({ node_ids: [...ids] })
    const success = results.filter((result) => result.success).length
    const failed = results.length - success
    const changed = results.filter((result) => result.exit_status === 'changed').length
    if (changed > 0) {
      appStore.showWarning(t('admin.clash.nodes.probeChanged', { success, failed, changed }))
    } else if (failed > 0) {
      appStore.showWarning(t('admin.clash.nodes.probeDone', { success, failed }))
    } else {
      appStore.showSuccess(t('admin.clash.nodes.probeDone', { success, failed }))
    }
    await load()
    emit('changed')
  } catch (error) {
    appStore.showError(describeError(error, 'admin.clash.nodes.probeFailed'))
  } finally {
    markBusy(ids, false)
    bulkProbing.value = false
  }
}

async function setNodeEnabled(node: ClashNode, enabled: boolean) {
  markBusy([node.id], true)
  try {
    if (enabled) await adminAPI.clash.enableNode(node.id)
    else await adminAPI.clash.disableNode(node.id)
    appStore.showSuccess(enabled ? t('admin.clash.nodes.enabledToast') : t('admin.clash.nodes.disabledToast'))
    await load()
    emit('changed')
  } catch (error) {
    appStore.showError(describeError(error, 'admin.clash.nodes.toggleFailed'))
  } finally {
    markBusy([node.id], false)
  }
}

async function confirmDisable() {
  const node = pendingDisable.value
  pendingDisable.value = null
  if (node) await setNodeEnabled(node, false)
}

async function confirmAcceptExit() {
  const node = pendingAccept.value
  pendingAccept.value = null
  if (!node) return
  markBusy([node.id], true)
  try {
    await adminAPI.clash.acceptNodeExit(node.id)
    appStore.showSuccess(t('admin.clash.nodes.acceptSuccess'))
    await load()
    emit('changed')
  } catch (error) {
    appStore.showError(describeError(error, 'admin.clash.nodes.acceptFailed'))
  } finally {
    markBusy([node.id], false)
  }
}

/** Filters the table to one subscription and brings the panel into view. */
function focusProfile(profileId: number | null) {
  filters.profileId = profileId
  applyFilters()
  rootRef.value?.scrollIntoView?.({ behavior: 'smooth', block: 'start' })
}

const statusLabel = (status: ClashNodeStatus) => {
  switch (status) {
    case 'active':
      return t('admin.clash.nodeStatus.active')
    case 'missing':
      return t('admin.clash.nodeStatus.missing')
    case 'disabled':
      return t('admin.clash.nodeStatus.disabled')
    default:
      return t('admin.clash.nodeStatus.invalid')
  }
}

const statusPillClass = (status: ClashNodeStatus) => {
  switch (status) {
    case 'active':
      return 'bg-emerald-50 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-500/10 dark:text-emerald-300 dark:ring-emerald-400/20'
    case 'disabled':
      return 'bg-amber-50 text-amber-700 ring-amber-600/20 dark:bg-amber-500/10 dark:text-amber-300 dark:ring-amber-400/20'
    case 'invalid':
      return 'bg-red-50 text-red-700 ring-red-600/20 dark:bg-red-500/10 dark:text-red-300 dark:ring-red-400/20'
    default:
      return 'bg-gray-100 text-gray-600 ring-gray-500/20 dark:bg-dark-700 dark:text-dark-300 dark:ring-dark-500/30'
  }
}

const statusDotClass = (status: ClashNodeStatus) => {
  switch (status) {
    case 'active':
      return 'bg-emerald-500'
    case 'disabled':
      return 'bg-amber-500'
    case 'invalid':
      return 'bg-red-500'
    default:
      return 'bg-gray-400'
  }
}

const healthLabel = (node: ClashNode) => {
  if (node.health_status === 'healthy') {
    return typeof node.latency_ms === 'number' ? `${node.latency_ms} ms` : t('admin.clash.health.healthy')
  }
  return node.health_status === 'unhealthy' ? t('admin.clash.health.unhealthy') : t('admin.clash.health.unknown')
}

const healthDotClass = (node: ClashNode) => {
  if (node.health_status === 'unhealthy') return 'bg-red-500'
  if (node.health_status !== 'healthy') return 'bg-gray-400'
  const latency = node.latency_ms ?? 0
  if (latency >= 1000) return 'bg-red-500'
  if (latency >= 300) return 'bg-amber-500'
  return 'bg-emerald-500'
}

const healthTextClass = (node: ClashNode) => {
  if (node.health_status === 'unhealthy') return 'font-medium text-red-600 dark:text-red-400'
  if (node.health_status === 'healthy') return 'font-medium tabular-nums text-gray-800 dark:text-gray-100'
  return 'text-gray-500 dark:text-dark-400'
}

const healthTitle = (node: ClashNode) => {
  const parts: string[] = []
  if (node.last_checked_at) parts.push(t('admin.clash.nodes.checkedAt', { time: formatDateTime(node.last_checked_at) }))
  if (node.last_check_error) parts.push(node.last_check_error)
  return parts.join('\n') || undefined
}

const accountNames = (node: ClashNode) => node.accounts.map((account) => account.name).join(', ')

const exitLocation = (node: ClashNode) =>
  [node.exit_country, node.exit_region, node.exit_city].filter(Boolean).filter((value, index, all) => all.indexOf(value) === index).join(' · ')

const platformBadgeClass = (result: ClashPlatformCheckResult | undefined) => {
  switch (result) {
    case 'pass':
      return 'bg-emerald-50 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-500/10 dark:text-emerald-300 dark:ring-emerald-400/20'
    case 'warn':
      return 'bg-amber-50 text-amber-700 ring-amber-600/20 dark:bg-amber-500/10 dark:text-amber-300 dark:ring-amber-400/20'
    case 'fail':
    case 'challenge':
      return 'bg-red-50 text-red-700 ring-red-600/20 dark:bg-red-500/10 dark:text-red-300 dark:ring-red-400/20'
    default:
      return 'bg-gray-50 text-gray-400 ring-gray-400/20 dark:bg-dark-800 dark:text-dark-500 dark:ring-dark-600/40'
  }
}

const platformResultLabel = (result: ClashPlatformCheckResult | undefined) => {
  switch (result) {
    case 'pass':
      return t('admin.clash.platformResult.pass')
    case 'warn':
      return t('admin.clash.platformResult.warn')
    case 'fail':
      return t('admin.clash.platformResult.fail')
    case 'challenge':
      return t('admin.clash.platformResult.challenge')
    default:
      return t('admin.clash.platformResult.unchecked')
  }
}

const platformTitle = (node: ClashNode, platform: ClashPlatform) => {
  const label = `${PLATFORM_LABELS[platform]}: ${platformResultLabel(node.platform_checks.results[platform])}`
  const checkedAt = node.platform_checks.checked_at
  return checkedAt ? `${label}\n${t('admin.clash.nodes.checkedAt', { time: formatDateTime(checkedAt) })}` : label
}

onMounted(() => {
  void load()
})

onUnmounted(() => {
  clearTimeout(searchTimer)
  abortController?.abort()
})

defineExpose({ reload: load, focusProfile })
</script>

<style scoped>
.status-pill {
  @apply inline-flex items-center gap-1.5 whitespace-nowrap rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset;
}

.type-badge {
  @apply inline-flex flex-shrink-0 items-center rounded px-1.5 py-px font-mono text-[10px] font-medium uppercase;
  @apply bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300;
}

.platform-badge {
  @apply inline-flex items-center rounded px-1.5 py-px text-[11px] font-medium ring-1 ring-inset;
}
</style>
