<template>
  <div class="relative" ref="containerRef" data-testid="proxy-selector">
    <button
      ref="triggerRef"
      type="button"
      @click="toggle"
      @keydown="handleTriggerKeydown"
      :disabled="disabled"
      aria-haspopup="listbox"
      :aria-expanded="isOpen"
      :aria-controls="isOpen ? listboxId : undefined"
      :class="[
        'select-trigger',
        isOpen && 'select-trigger-open',
        disabled && 'select-trigger-disabled'
      ]"
    >
      <span class="select-value">
        <template v-if="selectedClashExit">
          <ClashTag />
          <CountryFlag
            v-if="selectedClashExit.exit_ip"
            :code="selectedClashExit.exit_country_code"
            :label="selectedClashExit.exit_country"
          />
          <span class="min-w-0 truncate">{{ selectedLabel }}</span>
          <span
            :class="['clash-chip flex-shrink-0', healthChipClass(selectedClashExit)]"
            data-testid="proxy-selector-selected-health"
          >
            <span class="h-1.5 w-1.5 flex-shrink-0 rounded-full" :class="healthDotClass(selectedClashExit)"></span>
            {{ healthLabel(selectedClashExit) }}
          </span>
        </template>
        <span v-else class="truncate">{{ selectedLabel }}</span>
      </span>
      <span class="select-icon">
        <Icon
          name="chevronDown"
          size="md"
          :class="['transition-transform duration-200', isOpen && 'rotate-180']"
        />
      </span>
    </button>

    <!-- Teleported so the modal body cannot clip it; placed under (or above) the trigger -->
    <Teleport to="body">
      <Transition name="select-dropdown">
        <div
          v-if="isOpen"
          ref="panelRef"
          :class="['select-dropdown', layout?.placement === 'top' && 'select-dropdown-top']"
          :style="panelStyle"
          tabindex="-1"
          data-testid="proxy-selector-panel"
          @keydown="handlePanelKeydown"
        >
          <!-- Search and Batch Test Header -->
          <div class="select-header">
            <div class="select-search">
              <Icon name="search" size="sm" class="text-gray-400" />
              <input
                ref="searchInputRef"
                v-model="searchQuery"
                type="text"
                role="combobox"
                aria-autocomplete="list"
                aria-expanded="true"
                :aria-controls="listboxId"
                :aria-activedescendant="activeDescendant"
                :aria-label="searchPlaceholder"
                :placeholder="searchPlaceholder"
                class="select-search-input"
              />
            </div>
            <button
              v-if="proxies.length > 0"
              type="button"
              @click.stop="handleBatchTest"
              :disabled="batchTesting"
              class="batch-test-btn"
              :title="t('admin.proxies.batchTest')"
            >
              <svg v-if="batchTesting" class="h-4 w-4 animate-spin" fill="none" viewBox="0 0 24 24">
                <circle
                  class="opacity-25"
                  cx="12"
                  cy="12"
                  r="10"
                  stroke="currentColor"
                  stroke-width="4"
                ></circle>
                <path
                  class="opacity-75"
                  fill="currentColor"
                  d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
                ></path>
              </svg>
              <Icon v-else name="play" size="sm" />
            </button>
          </div>

          <!-- Clash exit filters and sort order (remembered across account editors) -->
          <div
            v-if="hasClashExits"
            class="select-filters"
            role="group"
            :aria-label="t('admin.clash.selector.filters.label')"
            data-testid="clash-exit-filters"
          >
            <button
              type="button"
              :class="['exit-filter-chip', filters.onlyAvailable && 'exit-filter-chip-active']"
              :aria-pressed="filters.onlyAvailable"
              data-testid="clash-exit-filter-available"
              @mousedown.prevent
              @click="filters.onlyAvailable = !filters.onlyAvailable"
            >
              <Icon v-if="filters.onlyAvailable" name="check" size="xs" :stroke-width="2.5" />
              {{ t('admin.clash.selector.filters.onlyAvailable') }}
            </button>
            <button
              type="button"
              :class="['exit-filter-chip', filters.onlyIdle && 'exit-filter-chip-active']"
              :aria-pressed="filters.onlyIdle"
              data-testid="clash-exit-filter-idle"
              @mousedown.prevent
              @click="filters.onlyIdle = !filters.onlyIdle"
            >
              <Icon v-if="filters.onlyIdle" name="check" size="xs" :stroke-width="2.5" />
              {{ t('admin.clash.selector.filters.onlyIdle') }}
            </button>
            <button
              v-if="checkPlatform"
              type="button"
              :class="['exit-filter-chip', filters.platformPass && 'exit-filter-chip-active']"
              :aria-pressed="filters.platformPass"
              data-testid="clash-exit-filter-platform"
              @mousedown.prevent
              @click="filters.platformPass = !filters.platformPass"
            >
              <Icon v-if="filters.platformPass" name="check" size="xs" :stroke-width="2.5" />
              {{ t('admin.clash.selector.filters.platformPass', { platform: CLASH_CHECK_PLATFORM_LABELS[checkPlatform] }) }}
            </button>
            <label v-if="countryOptions.length > 1" class="exit-filter-select">
              <CountryFlag v-if="activeCountry?.code" :code="activeCountry.code" :label="activeCountry.label" />
              <Icon v-else name="globe" size="xs" class="flex-shrink-0 text-gray-400" />
              <select
                v-model="countryFilter"
                class="exit-filter-select-input"
                :aria-label="t('admin.clash.selector.filters.country')"
                data-testid="clash-exit-filter-country"
              >
                <option value="">{{ t('admin.clash.selector.filters.allCountries') }}</option>
                <option v-for="country in countryOptions" :key="country.key" :value="country.key">
                  {{ country.label }} ({{ country.count }})
                </option>
              </select>
            </label>
            <label v-if="profileOptions.length > 1" class="exit-filter-select">
              <ClashTag />
              <select
                v-model="profileFilter"
                class="exit-filter-select-input"
                :aria-label="t('admin.clash.selector.filters.profile')"
                data-testid="clash-exit-filter-profile"
              >
                <option :value="null">{{ t('admin.clash.selector.filters.allProfiles') }}</option>
                <option v-for="profile in profileOptions" :key="profile.id" :value="profile.id">
                  {{ profile.name }} ({{ profile.count }})
                </option>
              </select>
            </label>
            <label class="exit-filter-select">
              <Icon name="arrowsUpDown" size="xs" class="flex-shrink-0 text-gray-400" />
              <select
                v-model="filters.sort"
                class="exit-filter-select-input"
                :aria-label="t('admin.clash.selector.sort.label')"
                data-testid="clash-exit-sort"
              >
                <option value="default">{{ t('admin.clash.selector.sort.default') }}</option>
                <option value="latency">{{ t('admin.clash.selector.sort.latency') }}</option>
                <option value="idle">{{ t('admin.clash.selector.sort.idle') }}</option>
              </select>
            </label>
            <template v-if="filtersActive">
              <span class="ml-auto text-xs tabular-nums text-gray-500 dark:text-dark-400" data-testid="clash-exit-filter-summary">
                {{ t('admin.clash.selector.filters.summary', { shown: clashRows.matched, total: clashExitList.length }) }}
              </span>
              <button
                type="button"
                class="exit-filter-clear"
                data-testid="clash-exit-filter-clear"
                @mousedown.prevent
                @click="clearFilters"
              >
                {{ t('admin.clash.selector.filters.clear') }}
              </button>
            </template>
          </div>

          <!-- Options list (virtualized: subscriptions can hold hundreds of exits) -->
          <div
            :id="listboxId"
            ref="listRef"
            role="listbox"
            :aria-label="t('admin.accounts.proxy')"
            :class="['select-options', hasClashExits && 'select-options-tall']"
          >
            <div v-if="virtualPadding.top > 0" aria-hidden="true" :style="{ height: `${virtualPadding.top}px` }"></div>
            <div
              v-for="{ index, row } in renderedRows"
              :key="row.key"
              :ref="measureRow"
              :data-index="index"
              @mousedown.prevent
            >
              <!-- No Proxy option -->
              <div
                v-if="row.kind === 'none'"
                :id="optionDomId(row.key)"
                role="option"
                :aria-selected="modelValue === null"
                :aria-setsize="optionPositions.size"
                :aria-posinset="optionPositions.get(row.key)"
                @click="selectOption(null)"
                @mousemove="handleRowMouseMove($event, row)"
                :class="[
                  'select-option',
                  modelValue === null && 'select-option-selected',
                  activeKey === row.key && 'select-option-active'
                ]"
              >
                <span class="select-option-label">{{ t('admin.accounts.noProxy') }}</span>
                <Icon v-if="modelValue === null" name="check" size="sm" class="text-primary-500" />
              </div>

              <!-- Proxy options -->
              <div
                v-else-if="row.kind === 'proxy'"
                :id="optionDomId(row.key)"
                role="option"
                :aria-selected="modelValue === row.proxy.id"
                :aria-setsize="optionPositions.size"
                :aria-posinset="optionPositions.get(row.key)"
                @click="selectOption(row.proxy.id)"
                @mousemove="handleRowMouseMove($event, row)"
                :class="[
                  'select-option',
                  modelValue === row.proxy.id && 'select-option-selected',
                  activeKey === row.key && 'select-option-active'
                ]"
              >
                <div class="min-w-0 flex-1">
                  <div class="flex items-center gap-2">
                    <span class="truncate font-medium">{{ row.proxy.name }}</span>
                    <!-- Account count badge -->
                    <span
                      v-if="row.proxy.account_count !== undefined"
                      class="inline-flex flex-shrink-0 items-center rounded bg-gray-100 px-1.5 py-0.5 text-xs text-gray-600 dark:bg-dark-600 dark:text-gray-400"
                    >
                      {{ row.proxy.account_count }}
                    </span>
                    <!-- Test result badges -->
                    <template v-if="testResults[row.proxy.id]">
                      <span
                        v-if="testResults[row.proxy.id].success"
                        class="inline-flex flex-shrink-0 items-center gap-1 rounded bg-emerald-100 px-1.5 py-0.5 text-xs text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400"
                      >
                        <span v-if="testResults[row.proxy.id].country">{{
                          testResults[row.proxy.id].country
                        }}</span>
                        <span v-if="testResults[row.proxy.id].latency_ms"
                          >{{ testResults[row.proxy.id].latency_ms }}ms</span
                        >
                      </span>
                      <span
                        v-else
                        class="inline-flex flex-shrink-0 items-center rounded bg-red-100 px-1.5 py-0.5 text-xs text-red-700 dark:bg-red-900/30 dark:text-red-400"
                      >
                        {{ t('admin.proxies.testFailed') }}
                      </span>
                    </template>
                  </div>
                  <div class="truncate text-xs text-gray-500 dark:text-gray-400">
                    {{ row.proxy.protocol }}://{{ row.proxy.host }}:{{ row.proxy.port }}
                  </div>
                </div>

                <!-- Individual test button -->
                <button
                  type="button"
                  @click.stop="handleTestProxy(row.proxy)"
                  :disabled="testingProxyIds.has(row.proxy.id)"
                  class="test-btn"
                  :title="t('admin.proxies.testConnection')"
                >
                  <svg
                    v-if="testingProxyIds.has(row.proxy.id)"
                    class="h-3.5 w-3.5 animate-spin"
                    fill="none"
                    viewBox="0 0 24 24"
                  >
                    <circle
                      class="opacity-25"
                      cx="12"
                      cy="12"
                      r="10"
                      stroke="currentColor"
                      stroke-width="4"
                    ></circle>
                    <path
                      class="opacity-75"
                      fill="currentColor"
                      d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
                    ></path>
                  </svg>
                  <Icon v-else name="play" size="xs" />
                </button>

                <Icon
                  v-if="modelValue === row.proxy.id"
                  name="check"
                  size="sm"
                  class="flex-shrink-0 text-primary-500"
                />
              </div>

              <!-- Clash subscription header: collapses its exits -->
              <button
                v-else-if="row.kind === 'group'"
                type="button"
                tabindex="-1"
                class="select-group-label"
                :aria-expanded="!row.collapsed"
                data-testid="clash-exit-group"
                @click="toggleGroup(row.group.profileId)"
              >
                <Icon
                  name="chevronRight"
                  size="xs"
                  :class="['flex-shrink-0 transition-transform duration-150', !row.collapsed && 'rotate-90']"
                />
                <ClashTag />
                <span class="truncate">{{ row.group.profileName }}</span>
                <span class="ml-auto flex-shrink-0 font-normal tabular-nums" data-testid="clash-exit-group-counts">
                  {{ t('admin.clash.selector.groupCounts', { usable: row.group.usable, idle: row.group.idle, total: row.group.total }) }}
                </span>
              </button>

              <!-- Exits that cannot be picked, folded at the bottom -->
              <button
                v-else-if="row.kind === 'unavailable'"
                type="button"
                tabindex="-1"
                class="select-group-label"
                :aria-expanded="row.expanded"
                :title="t('admin.clash.selector.unavailableHint')"
                data-testid="clash-exit-unavailable-toggle"
                @click="unavailableExpanded = !unavailableExpanded"
              >
                <Icon
                  name="chevronRight"
                  size="xs"
                  :class="['flex-shrink-0 transition-transform duration-150', row.expanded && 'rotate-90']"
                />
                <Icon name="ban" size="xs" class="flex-shrink-0" />
                <span class="truncate">{{ t('admin.clash.selector.unavailableSection', { count: row.count }) }}</span>
              </button>

              <!-- Clash exit -->
              <div
                v-else
                :id="optionDomId(row.key)"
                role="option"
                :aria-selected="modelValue === row.view.exit.proxy_id"
                :aria-disabled="row.view.disabled ? 'true' : undefined"
                :aria-setsize="optionPositions.size"
                :aria-posinset="optionPositions.get(row.key)"
                :data-testid="`clash-exit-${row.view.exit.proxy_id}`"
                :title="row.view.disabled ? row.view.blockReason : undefined"
                :class="[
                  'select-option',
                  modelValue === row.view.exit.proxy_id && 'select-option-selected',
                  activeKey === row.key && 'select-option-active',
                  row.view.disabled && 'select-option-disabled'
                ]"
                @click="selectClashExit(row.view)"
                @mousemove="handleRowMouseMove($event, row)"
              >
                <div class="min-w-0 flex-1">
                  <div class="flex min-w-0 items-center gap-2">
                    <span class="min-w-0 truncate font-medium">{{ row.view.exit.node_name }}</span>
                    <span
                      :class="['clash-chip flex-shrink-0', healthChipClass(row.view.exit)]"
                      data-testid="clash-exit-health"
                    >
                      <span class="h-1.5 w-1.5 flex-shrink-0 rounded-full" :class="healthDotClass(row.view.exit)"></span>
                      {{ healthLabel(row.view.exit) }}
                    </span>
                    <span
                      :class="['clash-chip min-w-0', occupancyChipClass(row.view)]"
                      :title="row.view.others.map((account) => account.name).join(', ') || undefined"
                      data-testid="clash-exit-occupancy"
                    >
                      <span class="truncate">{{ row.view.occupancyLabel }}</span>
                    </span>
                  </div>
                  <div
                    v-if="row.view.exit.exit_ip || !row.view.unprobedBlocked || row.unavailable"
                    class="mt-0.5 flex min-w-0 items-center text-xs text-gray-500 dark:text-gray-400"
                  >
                    <span v-if="row.view.exit.exit_ip" class="exit-detail inline-flex flex-shrink-0 items-center gap-1.5">
                      <CountryFlag :code="row.view.exit.exit_country_code" :label="row.view.exit.exit_country" />
                      <span class="font-mono">{{ row.view.exit.exit_ip }}</span>
                    </span>
                    <!-- Unprobed exits that are blocked for it say so once, in the reason line below -->
                    <span v-else-if="!row.view.unprobedBlocked" class="exit-detail italic">
                      {{ t('admin.clash.selector.exitUnprobed') }}
                    </span>
                    <span v-if="row.view.location" class="exit-detail truncate">{{ row.view.location }}</span>
                    <span v-if="row.unavailable" class="exit-detail truncate">{{ row.view.exit.profile_name }}</span>
                  </div>
                  <div
                    v-if="inlineReason(row.view)"
                    class="mt-0.5 truncate text-xs text-red-600 dark:text-red-400"
                    data-testid="clash-exit-block-reason"
                  >
                    {{ inlineReason(row.view) }}
                  </div>
                  <div
                    v-if="row.view.platformWarning"
                    class="mt-0.5 flex items-center gap-1 text-xs text-amber-600 dark:text-amber-400"
                    data-testid="clash-exit-platform-warning"
                  >
                    <Icon name="exclamationTriangle" size="xs" class="flex-shrink-0" />
                    <span class="truncate">{{ t('admin.clash.selector.platformWarning') }}</span>
                  </div>
                </div>
                <Icon
                  v-if="modelValue === row.view.exit.proxy_id"
                  name="check"
                  size="sm"
                  class="flex-shrink-0 text-primary-500"
                />
              </div>
            </div>
            <div v-if="virtualPadding.bottom > 0" aria-hidden="true" :style="{ height: `${virtualPadding.bottom}px` }"></div>
          </div>

          <!-- Empty state (the filter bar above offers to clear the filters) -->
          <div v-if="emptyText" class="select-empty">
            {{ emptyText }}
          </div>
        </div>
      </Transition>
    </Teleport>
  </div>
</template>

<script lang="ts">
let proxySelectorCounter = 0
</script>

<script setup lang="ts">
import { ref, reactive, computed, watch, onBeforeUnmount, nextTick, type ComponentPublicInstance } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  measureElement as measureElementSize,
  observeElementRect,
  useVirtualizer,
  type Rect,
  type Virtualizer
} from '@tanstack/vue-virtual'
import { adminAPI } from '@/api/admin'
import Icon from '@/components/icons/Icon.vue'
import CountryFlag from '@/components/common/CountryFlag.vue'
import ClashTag from '@/components/common/ClashTag.vue'
import type { ClashExitList, ClashExitOption, Proxy } from '@/types'
import { mapWithConcurrency } from '@/utils/concurrency'
import {
  clashCheckPlatformFor,
  exitPlatformCheck,
  isBlockingPlatformCheck,
  localizeClashUnavailableReason
} from '@/utils/clash'
import {
  CLASH_CHECK_PLATFORM_LABELS,
  buildClashExitRows,
  clashCountryOptions,
  clashProfileOptions,
  computeDropdownPlacement,
  defaultClashExitFilters,
  effectiveClashExitFilters,
  exitLatencyTone,
  hasActiveClashExitFilters,
  matchesSearch,
  proxySearchFields,
  readClashExitFilters,
  searchTokens,
  writeClashExitFilters,
  type ClashExitFilters,
  type ClashExitView,
  type ClashLatencyTone,
  type ProxySelectorRow
} from './proxySelectorModel'

const { t } = useI18n()

/** Upper bound of concurrent connectivity tests started by the batch button. */
const BATCH_TEST_CONCURRENCY = 4
/** Tallest the option list gets (see .select-options-tall); first-frame height of the virtualizer. */
const LIST_MAX_HEIGHT = 480
/** Space above the first and below the last option, kept by the virtualizer so scrolling lines up. */
const LIST_PADDING = 4

interface ProxyTestResult {
  success: boolean
  message: string
  latency_ms?: number
  ip_address?: string
  city?: string
  region?: string
  country?: string
}

interface Props {
  modelValue: number | null
  proxies: Proxy[]
  disabled?: boolean
  /** Clash exits offered after the manual proxies; omit to keep the manual-only selector. */
  clashExits?: ClashExitList | null
  /** Account being edited: its own binding does not count as an occupant. */
  accountId?: number | null
  /** Account platform, used to warn about exits failing that platform's reachability check. */
  platform?: string
}

const props = withDefaults(defineProps<Props>(), {
  disabled: false,
  clashExits: null,
  accountId: null,
  platform: ''
})

const emit = defineEmits<{
  'update:modelValue': [value: number | null]
}>()

const listboxId = `proxy-selector-${++proxySelectorCounter}-listbox`
const optionDomId = (key: string) => `${listboxId}-${key}`

const isOpen = ref(false)
const searchQuery = ref('')
const containerRef = ref<HTMLElement | null>(null)
const triggerRef = ref<HTMLButtonElement | null>(null)
const panelRef = ref<HTMLElement | null>(null)
const listRef = ref<HTMLElement | null>(null)
const searchInputRef = ref<HTMLInputElement | null>(null)

// Test state
const testResults = reactive<Record<number, ProxyTestResult>>({})
const testingProxyIds = reactive(new Set<number>())
const batchTesting = ref(false)

const selectedProxy = computed(() => {
  if (props.modelValue === null) return null
  return props.proxies.find((p) => p.id === props.modelValue) || null
})

const clashExitList = computed(() => props.clashExits?.exits ?? [])
const hasClashExits = computed(() => clashExitList.value.length > 0)

const searchPlaceholder = computed(() =>
  hasClashExits.value ? t('admin.clash.selector.searchPlaceholder') : t('admin.proxies.searchProxies')
)

const selectedClashExit = computed(() => {
  if (props.modelValue === null || selectedProxy.value) return null
  return clashExitList.value.find((exit) => exit.proxy_id === props.modelValue) || null
})

const selectedLabel = computed(() => {
  const proxy = selectedProxy.value
  if (proxy) {
    return `${proxy.name} (${proxy.protocol}://${proxy.host}:${proxy.port})`
  }
  const exit = selectedClashExit.value
  if (exit) {
    return exit.exit_ip
      ? t('admin.clash.selector.selectedLabel', { node: exit.node_name, ip: exit.exit_ip })
      : exit.node_name
  }
  // Keep showing a binding we cannot resolve (e.g. exits failed to load) instead of "no proxy".
  if (typeof props.modelValue === 'number' && props.modelValue > 0) {
    return t('admin.clash.selector.unlistedProxy', { id: props.modelValue })
  }
  return t('admin.accounts.noProxy')
})

// The binding the editor started with stays selectable after picking another option,
// so the admin can always switch back to it.
const initialValue = ref<number | null>(props.modelValue)
watch(
  () => props.accountId,
  () => {
    initialValue.value = props.modelValue
  }
)

const clashExitViews = computed<ClashExitView[]>(() => {
  const list = props.clashExits
  if (!list) return []
  const maxPerExit = Math.max(1, list.max_accounts_per_exit || 1)
  return list.exits.map((exit) => {
    const others = (exit.occupants ?? []).filter(
      (account) => !account.is_shadow && account.id !== props.accountId
    )
    const full = others.length >= maxPerExit
    const unprobedBlocked = exit.available && !exit.exit_ip && !list.allow_unprobed_exit_binding
    let unavailableText = ''
    if (!exit.available) {
      unavailableText = localizeClashUnavailableReason(exit.unavailable_reason, t)
    } else if (unprobedBlocked) {
      unavailableText = t('admin.clash.selector.exitUnprobedBlocked')
    }
    const blockReason = unavailableText || (full ? t('admin.clash.selector.full', { max: maxPerExit }) : '')
    let occupancyLabel = t('admin.clash.selector.idle')
    if (others.length > 0) {
      const names = others.slice(0, 2).map((account) => account.name).join(', ')
      occupancyLabel = others.length > 2
        ? t('admin.clash.selector.usedByMany', { names, count: others.length })
        : t('admin.clash.selector.usedBy', { names })
    }
    // The current and the original binding stay selectable so saving other fields never forces a rebind.
    const pinned = props.modelValue === exit.proxy_id || initialValue.value === exit.proxy_id
    return {
      exit,
      others,
      full,
      unavailableText,
      unprobedBlocked,
      blockReason,
      disabled: blockReason !== '' && !pinned,
      occupancyLabel,
      platformWarning: isBlockingPlatformCheck(exitPlatformCheck(exit, props.platform)),
      location: [exit.exit_country, exit.exit_city].filter(Boolean).join(' · ')
    }
  })
})

// ==================== Filters, sort and search ====================

const filters = reactive<ClashExitFilters>(readClashExitFilters())
watch(filters, () => writeClashExitFilters({ ...filters }), { deep: true })

const checkPlatform = computed(() => clashCheckPlatformFor(props.platform))
const countryOptions = computed(() => clashCountryOptions(clashExitList.value))
const profileOptions = computed(() => clashProfileOptions(clashExitList.value))
const activeFilters = computed(() =>
  effectiveClashExitFilters(filters, {
    countries: countryOptions.value,
    profiles: profileOptions.value,
    platform: props.platform
  })
)
const filtersActive = computed(() => hasClashExits.value && hasActiveClashExitFilters(activeFilters.value))
const activeCountry = computed(
  () => countryOptions.value.find((country) => country.key === activeFilters.value.country) ?? null
)

// The selects show the filter actually applied (a saved country may be missing from this list).
const countryFilter = computed({
  get: () => activeFilters.value.country,
  set: (value: string) => {
    filters.country = value
  }
})
const profileFilter = computed({
  get: () => activeFilters.value.profileId,
  set: (value: number | null) => {
    filters.profileId = value
  }
})

const clearFilters = () => {
  Object.assign(filters, { ...defaultClashExitFilters(), sort: filters.sort })
}

const tokens = computed(() => searchTokens(searchQuery.value))

const filteredProxies = computed(() =>
  props.proxies.filter((proxy) => matchesSearch(tokens.value, proxySearchFields(proxy)))
)

const collapsedProfiles = reactive(new Set<number>())
const unavailableExpanded = ref(false)

const toggleGroup = (profileId: number) => {
  if (collapsedProfiles.has(profileId)) collapsedProfiles.delete(profileId)
  else collapsedProfiles.add(profileId)
}

const clashRows = computed(() =>
  buildClashExitRows({
    views: clashExitViews.value,
    tokens: tokens.value,
    filters: activeFilters.value,
    platform: props.platform,
    collapsedProfiles,
    unavailableExpanded: unavailableExpanded.value
  })
)

const NONE_ROW: ProxySelectorRow = { kind: 'none', key: 'none' }

const rows = computed<ProxySelectorRow[]>(() => [
  NONE_ROW,
  ...filteredProxies.value.map((proxy): ProxySelectorRow => ({ kind: 'proxy', key: `proxy-${proxy.id}`, proxy })),
  ...clashRows.value.rows
])

/** 1-based position of every option, so screen readers count the rows the virtualizer skips. */
const optionPositions = computed(() => {
  const positions = new Map<string, number>()
  for (const row of rows.value) {
    if (row.kind === 'none' || row.kind === 'proxy' || row.kind === 'exit') positions.set(row.key, positions.size + 1)
  }
  return positions
})

const emptyText = computed(() => {
  if (filteredProxies.value.length > 0 || clashRows.value.matched > 0) return ''
  if (filtersActive.value) return t('admin.clash.selector.filters.noMatch')
  return tokens.value.length > 0 ? t('common.noOptionsFound') : ''
})

/** Block reason shown under an exit; the current binding still shows why it is down. */
const inlineReason = (view: ClashExitView) => (view.disabled ? view.blockReason : view.unavailableText)

// ==================== Virtual list ====================

const estimateRowHeight = (row: ProxySelectorRow | undefined) => {
  switch (row?.kind) {
    case 'proxy':
      return 56
    case 'group':
    case 'unavailable':
      return 31
    case 'exit':
      return 58 + (inlineReason(row.view) ? 18 : 0) + (row.view.platformWarning ? 18 : 0)
    default:
      return 40
  }
}

// Detached or hidden elements report 0: keep the initial height / the estimate instead of
// collapsing the list to nothing (same guard as DataTable).
const observeListRect = (instance: Virtualizer<HTMLElement, Element>, cb: (rect: Rect) => void) =>
  observeElementRect(instance, (rect) => {
    if (rect.height > 0) cb(rect)
  })

const measureRowHeight = (
  element: Element,
  entry: ResizeObserverEntry | undefined,
  instance: Virtualizer<HTMLElement, Element>
) => {
  const size = measureElementSize(element, entry, instance)
  return size > 0 ? size : instance.options.estimateSize(instance.indexFromElement(element))
}

const virtualizer = useVirtualizer<HTMLElement, Element>(
  computed(() => {
    const list = rows.value
    return {
      count: list.length,
      getScrollElement: () => listRef.value,
      // Keys (not indexes) own the measured heights, so filtering and sorting reuse them.
      getItemKey: (index: number) => list[index]?.key ?? index,
      estimateSize: (index: number) => estimateRowHeight(list[index]),
      overscan: 6,
      paddingStart: LIST_PADDING,
      paddingEnd: LIST_PADDING,
      initialRect: { width: 0, height: LIST_MAX_HEIGHT },
      observeElementRect: observeListRect,
      measureElement: measureRowHeight,
      useAnimationFrameWithResizeObserver: true
    }
  })
)

const renderedRows = computed(() => {
  const list = rows.value
  return virtualizer.value.getVirtualItems().flatMap((item) => {
    const row = list[item.index]
    return row ? [{ index: item.index, row }] : []
  })
})

const virtualPadding = computed(() => {
  const items = virtualizer.value.getVirtualItems()
  if (items.length === 0) return { top: 0, bottom: 0 }
  return {
    top: items[0].start,
    bottom: Math.max(0, virtualizer.value.getTotalSize() - items[items.length - 1].end)
  }
})

// Function refs run on every render: measure each row element once, its ResizeObserver
// (registered by measureElement) reports later size changes.
const measuredRows = new WeakSet<Element>()
const measureRow = (element: Element | ComponentPublicInstance | null) => {
  if (!(element instanceof Element) || measuredRows.has(element)) return
  measuredRows.add(element)
  virtualizer.value.measureElement(element)
}

// ==================== Keyboard highlight ====================

const activeKey = ref<string | null>(null)
const activeIndex = computed(() =>
  activeKey.value === null ? -1 : rows.value.findIndex((row) => row.key === activeKey.value)
)
const activeDescendant = computed(() => {
  const key = activeKey.value
  return key !== null && renderedRows.value.some(({ row }) => row.key === key) ? optionDomId(key) : undefined
})

const isSelectableRow = (row: ProxySelectorRow | undefined) =>
  !!row && (row.kind === 'none' || row.kind === 'proxy' || (row.kind === 'exit' && !row.view.disabled))

/** First option that can be picked; while searching, the first match rather than "no proxy". */
const firstSelectableKey = (list: ProxySelectorRow[], preferMatches: boolean) => {
  const match = preferMatches ? list.find((row) => row.kind !== 'none' && isSelectableRow(row)) : undefined
  return (match ?? list.find((row) => isSelectableRow(row)))?.key ?? null
}

const selectedRowKey = computed(() => {
  if (props.modelValue === null) return NONE_ROW.key
  if (selectedProxy.value) return `proxy-${props.modelValue}`
  return selectedClashExit.value ? `exit-${props.modelValue}` : null
})

const scrollActiveIntoView = () => {
  nextTick(() => {
    if (activeIndex.value >= 0) virtualizer.value.scrollToIndex(activeIndex.value)
  })
}

/**
 * Centres the highlighted option right after opening. Uses the list's real height: the
 * virtualizer only learns about the clamped panel from its next ResizeObserver callback.
 */
const revealActive = () => {
  const list = listRef.value
  const item = virtualizer.value.measurementsCache[activeIndex.value]
  if (!list || !item) return
  virtualizer.value.scrollToOffset(Math.max(0, item.start - (list.clientHeight - item.size) / 2))
}

// Hovering highlights too, but only when the pointer really moves: rows scrolled under a resting
// pointer by the arrow keys must not steal the highlight back.
let lastPointer = ''
const handleRowMouseMove = (event: MouseEvent, row: ProxySelectorRow) => {
  const pointer = `${event.clientX},${event.clientY}`
  if (pointer === lastPointer) return
  lastPointer = pointer
  if (isSelectableRow(row)) activeKey.value = row.key
}

const moveActive = (step: 1 | -1) => {
  const list = rows.value
  const count = list.length
  if (count === 0) return
  let index = activeIndex.value >= 0 ? activeIndex.value : step > 0 ? -1 : count
  // Skips headers and blocked exits; wraps around like Select.vue.
  for (let visited = 0; visited < count; visited++) {
    index = (index + step + count) % count
    if (isSelectableRow(list[index])) {
      activeKey.value = list[index].key
      scrollActiveIntoView()
      return
    }
  }
}

const selectActive = () => {
  const row = rows.value[activeIndex.value]
  if (!row || !isSelectableRow(row)) return
  if (row.kind === 'none') selectOption(null)
  else if (row.kind === 'proxy') selectOption(row.proxy.id)
  else if (row.kind === 'exit') selectClashExit(row.view)
}

// A new filter or sort order shows its results from the top.
watch(activeFilters, () => {
  if (isOpen.value) nextTick(() => virtualizer.value.scrollToOffset(0))
})

watch([rows, searchQuery], ([list, query], [, previousQuery]) => {
  if (!isOpen.value) return
  if (query !== previousQuery) {
    // Typing highlights the first match.
    activeKey.value = firstSelectableKey(list, searchTokens(query).length > 0)
    scrollActiveIntoView()
  } else if (!isSelectableRow(list[activeIndex.value])) {
    // Filtered or folded away: the next arrow key starts over, without moving the list now.
    activeKey.value = null
  }
})

// ==================== Placement ====================

const triggerBox = ref<{ top: number; bottom: number; left: number; width: number } | null>(null)
const viewport = ref({ width: 0, height: 0 })
/** Height the open panel needs; measured unclamped right after opening and on resize. */
const naturalHeight = ref(0)
const measuring = ref(false)

const layout = computed(() =>
  triggerBox.value
    ? computeDropdownPlacement({
        trigger: triggerBox.value,
        viewport: viewport.value,
        panelHeight: naturalHeight.value,
        minWidth: hasClashExits.value ? 360 : 200
      })
    : null
)

const panelStyle = computed(() => {
  const style: Record<string, string> = { position: 'fixed', zIndex: '100000020' }
  const value = layout.value
  if (!value) return style
  style.left = `${value.left}px`
  style.width = `${value.width}px`
  if (value.placement === 'top') style.bottom = `${value.bottom}px`
  else style.top = `${value.top}px`
  if (!measuring.value) style.maxHeight = `${value.maxHeight}px`
  return style
})

const measureTrigger = () => {
  const rect = triggerRef.value?.getBoundingClientRect()
  if (!rect) return
  triggerBox.value = { top: rect.top, bottom: rect.bottom, left: rect.left, width: rect.width }
  viewport.value = { width: window.innerWidth, height: window.innerHeight }
}

const measurePanel = () => {
  naturalHeight.value = panelRef.value?.offsetHeight ?? 0
  measuring.value = false
}

const handleWindowScroll = (event: Event) => {
  // Scrolling the option list does not move the trigger.
  if (event.target instanceof Node && panelRef.value?.contains(event.target)) return
  measureTrigger()
}

const handleWindowResize = () => {
  measureTrigger()
  measuring.value = true
  nextTick(measurePanel)
}

// ==================== Open / close ====================

const openDropdown = () => {
  if (props.disabled || isOpen.value) return
  measureTrigger()
  measuring.value = true
  isOpen.value = true
}

const closeDropdown = (restoreFocus = false) => {
  if (!isOpen.value) return
  isOpen.value = false
  // The focused search box leaves with the panel: hand focus back to the trigger.
  if (restoreFocus) triggerRef.value?.focus()
}

const toggle = () => {
  if (props.disabled) return
  if (isOpen.value) closeDropdown()
  else openDropdown()
}

const selectOption = (value: number | null) => {
  emit('update:modelValue', value)
  closeDropdown(true)
}

const selectClashExit = (view: ClashExitView) => {
  if (view.disabled) return
  selectOption(view.exit.proxy_id)
}

// Capture phase: BaseDialog stops click propagation inside the modal, and a click may re-render
// (and detach) its target before a bubbling listener could check it.
const handleDocumentClick = (event: MouseEvent) => {
  const target = event.target
  if (!(target instanceof Node)) return
  if (containerRef.value?.contains(target) || panelRef.value?.contains(target)) return
  closeDropdown()
}

// Escape closes only the dropdown: BaseDialog closes on Escape from a bubbling document listener,
// so the event is stopped while it is still being captured, wherever the focus is.
const handleDocumentKeydown = (event: KeyboardEvent) => {
  if (event.key !== 'Escape' || !isOpen.value) return
  event.stopPropagation()
  // Escape cancels an IME composition in the search box first.
  if (event.isComposing) return
  event.preventDefault()
  closeDropdown(true)
}

const handleListKeydown = (event: KeyboardEvent) => {
  if (event.isComposing) return
  switch (event.key) {
    case 'ArrowDown':
    case 'ArrowUp':
      event.preventDefault()
      moveActive(event.key === 'ArrowDown' ? 1 : -1)
      break
    case 'Enter':
      event.preventDefault()
      selectActive()
      break
    case 'Tab':
      // Focus the trigger and let the browser tab on from there, not from the end of <body>.
      closeDropdown(true)
      break
  }
}

const handleTriggerKeydown = (event: KeyboardEvent) => {
  if (isOpen.value) {
    handleListKeydown(event)
  } else if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault()
    openDropdown()
  }
}

const handlePanelKeydown = (event: KeyboardEvent) => {
  const target = event.target as HTMLElement | null
  // Filter selects and buttons keep their own keys; Tab still closes the dropdown.
  if (event.key !== 'Tab' && target && target !== searchInputRef.value && target.closest('select, button')) return
  handleListKeydown(event)
}

const addOpenListeners = () => {
  document.addEventListener('click', handleDocumentClick, true)
  document.addEventListener('keydown', handleDocumentKeydown, true)
  window.addEventListener('scroll', handleWindowScroll, { capture: true, passive: true })
  window.addEventListener('resize', handleWindowResize)
}

const removeOpenListeners = () => {
  document.removeEventListener('click', handleDocumentClick, true)
  document.removeEventListener('keydown', handleDocumentKeydown, true)
  window.removeEventListener('scroll', handleWindowScroll, { capture: true })
  window.removeEventListener('resize', handleWindowResize)
}

watch(isOpen, (open) => {
  if (!open) {
    searchQuery.value = ''
    activeKey.value = null
    removeOpenListeners()
    return
  }
  const selected = rows.value.find((row) => row.key === selectedRowKey.value)
  activeKey.value = isSelectableRow(selected) ? selectedRowKey.value : firstSelectableKey(rows.value, false)
  addOpenListeners()
  nextTick(() => {
    measurePanel()
    searchInputRef.value?.focus()
    // With Clash exits the list gets long: reveal the current binding once the panel is placed.
    nextTick(revealActive)
  })
})

watch(
  () => props.disabled,
  (disabled) => {
    if (disabled) closeDropdown()
  }
)

onBeforeUnmount(removeOpenListeners)

// ==================== Health chips ====================

const HEALTH_TONE_CLASSES: Record<ClashLatencyTone, { chip: string; dot: string }> = {
  good: { chip: 'bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300', dot: 'bg-emerald-500' },
  fair: { chip: 'bg-amber-50 text-amber-700 dark:bg-amber-500/10 dark:text-amber-300', dot: 'bg-amber-500' },
  poor: { chip: 'bg-red-50 text-red-700 dark:bg-red-500/10 dark:text-red-300', dot: 'bg-red-500' },
  down: { chip: 'bg-red-50 text-red-700 dark:bg-red-500/10 dark:text-red-300', dot: 'bg-red-500' },
  unknown: { chip: 'bg-gray-100 text-gray-600 dark:bg-dark-600 dark:text-gray-300', dot: 'bg-gray-400' }
}

const healthChipClass = (exit: ClashExitOption) => HEALTH_TONE_CLASSES[exitLatencyTone(exit)].chip

const healthDotClass = (exit: ClashExitOption) => HEALTH_TONE_CLASSES[exitLatencyTone(exit)].dot

const healthLabel = (exit: ClashExitOption) => {
  if (exit.health_status === 'healthy') {
    return typeof exit.latency_ms === 'number' ? `${exit.latency_ms}ms` : t('admin.clash.health.healthy')
  }
  return exit.health_status === 'unhealthy' ? t('admin.clash.health.unhealthy') : t('admin.clash.health.unknown')
}

const occupancyChipClass = (option: ClashExitView) => {
  if (option.others.length === 0) {
    return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300'
  }
  return option.full
    ? 'bg-red-50 text-red-700 dark:bg-red-500/10 dark:text-red-300'
    : 'bg-amber-50 text-amber-700 dark:bg-amber-500/10 dark:text-amber-300'
}

// ==================== Connectivity tests ====================

const handleTestProxy = async (proxy: Proxy) => {
  if (testingProxyIds.has(proxy.id)) return

  testingProxyIds.add(proxy.id)
  try {
    const result = await adminAPI.proxies.testProxy(proxy.id)
    testResults[proxy.id] = result
  } catch (error: any) {
    testResults[proxy.id] = {
      success: false,
      message: error.response?.data?.detail || 'Test failed'
    }
  } finally {
    testingProxyIds.delete(proxy.id)
  }
}

const handleBatchTest = async () => {
  if (batchTesting.value || props.proxies.length === 0) return

  batchTesting.value = true

  // Only manual proxies are tested here (Clash exits are probed from the Clash page);
  // cap the number of tests in flight so large proxy lists do not flood the server.
  try {
    await mapWithConcurrency(props.proxies, BATCH_TEST_CONCURRENCY, handleTestProxy)
  } finally {
    batchTesting.value = false
  }
}
</script>

<style scoped>
.select-trigger {
  @apply flex w-full items-center justify-between gap-2;
  @apply rounded-xl px-4 py-2.5 text-sm;
  @apply bg-white dark:bg-dark-800;
  @apply border border-gray-200 dark:border-dark-600;
  @apply text-gray-900 dark:text-gray-100;
  @apply transition-all duration-200;
  @apply focus:border-primary-500 focus:outline-none focus:ring-2 focus:ring-primary-500/30;
  @apply hover:border-gray-300 dark:hover:border-dark-500;
  @apply cursor-pointer;
}

.select-trigger-open {
  @apply border-primary-500 ring-2 ring-primary-500/30;
}

.select-trigger-disabled {
  @apply cursor-not-allowed bg-gray-100 opacity-60 dark:bg-dark-900;
}

.select-value {
  @apply flex min-w-0 flex-1 items-center gap-1.5 text-left;
}

.select-icon {
  @apply flex-shrink-0 text-gray-400 dark:text-dark-400;
}

.select-dropdown {
  @apply flex flex-col;
  @apply bg-white dark:bg-dark-800;
  @apply rounded-xl;
  @apply border border-gray-200 dark:border-dark-700;
  @apply shadow-lg shadow-black/10 dark:shadow-black/30;
  @apply overflow-hidden outline-none;
}

.select-header {
  @apply flex flex-shrink-0 items-center gap-2 px-3 py-2;
  @apply border-b border-gray-100 dark:border-dark-700;
}

.select-search {
  @apply flex flex-1 items-center gap-2;
}

.select-search-input {
  @apply flex-1 bg-transparent text-sm;
  @apply text-gray-900 dark:text-gray-100;
  @apply placeholder:text-gray-400 dark:placeholder:text-dark-400;
  @apply focus:outline-none;
}

.batch-test-btn {
  @apply flex-shrink-0 rounded-lg p-1.5;
  @apply text-gray-500 hover:text-emerald-600 dark:hover:text-emerald-400;
  @apply hover:bg-emerald-50 dark:hover:bg-emerald-900/20;
  @apply transition-colors disabled:cursor-not-allowed disabled:opacity-50;
}

.select-filters {
  @apply flex flex-shrink-0 flex-wrap items-center gap-1.5 px-3 py-2;
  @apply border-b border-gray-100 dark:border-dark-700;
}

.exit-filter-chip {
  @apply inline-flex items-center gap-1 rounded-full border px-2.5 py-1 text-xs font-medium transition-colors;
  @apply border-gray-200 text-gray-600 hover:border-gray-300 hover:bg-gray-50;
  @apply dark:border-dark-600 dark:text-gray-300 dark:hover:border-dark-500 dark:hover:bg-dark-700;
}

.exit-filter-chip-active {
  @apply border-primary-500 bg-primary-50 text-primary-700 hover:border-primary-500 hover:bg-primary-50;
  @apply dark:border-primary-500/60 dark:bg-primary-900/20 dark:text-primary-300 dark:hover:bg-primary-900/20;
}

.exit-filter-select {
  @apply inline-flex min-w-0 items-center gap-1.5 rounded-full border border-gray-200 pl-2.5 pr-1 dark:border-dark-600;
  @apply focus-within:border-primary-500 focus-within:ring-2 focus-within:ring-primary-500/20;
}

.exit-filter-select-input {
  @apply min-w-0 max-w-[11rem] cursor-pointer truncate bg-transparent py-1 text-xs text-gray-700 focus:outline-none dark:text-gray-200;
}

.dark .exit-filter-select-input {
  color-scheme: dark;
}

.exit-filter-clear {
  @apply rounded px-1.5 py-0.5 text-xs font-medium text-primary-600 hover:bg-primary-50 dark:text-primary-400 dark:hover:bg-primary-900/20;
}

.select-options {
  @apply max-h-60 min-h-0 flex-shrink overflow-y-auto overscroll-contain;
}

.select-options-tall {
  max-height: min(60vh, 480px);
}

.select-group-label {
  @apply flex w-full items-center gap-2 border-t border-gray-100 px-4 pb-1 pt-2 text-left;
  @apply text-xs font-semibold text-gray-500 dark:border-dark-700 dark:text-dark-400;
  @apply transition-colors hover:text-gray-700 dark:hover:text-gray-200;
}

.select-option.select-option-disabled {
  @apply cursor-not-allowed opacity-60 hover:bg-transparent dark:hover:bg-transparent;
}

.clash-chip {
  @apply inline-flex max-w-[14rem] items-center gap-1 overflow-hidden whitespace-nowrap rounded px-1.5 py-0.5 text-xs;
}

.exit-detail + .exit-detail::before {
  content: '·';
  @apply mx-1.5;
}

.select-option {
  @apply flex items-center justify-between gap-2;
  @apply px-4 py-2.5 text-sm;
  @apply text-gray-700 dark:text-gray-300;
  @apply cursor-pointer transition-colors duration-150;
  @apply hover:bg-gray-50 dark:hover:bg-dark-700;
}

.select-option-active {
  @apply bg-gray-100 dark:bg-dark-700;
}

.select-option-selected {
  @apply bg-primary-50 dark:bg-primary-900/20;
  @apply text-primary-700 dark:text-primary-300;
}

.select-option-selected.select-option-active {
  @apply bg-primary-100 dark:bg-primary-900/30;
}

.select-option-label {
  @apply truncate;
}

.select-empty {
  @apply px-4 py-8 text-center text-sm;
  @apply text-gray-500 dark:text-dark-400;
}

.test-btn {
  @apply flex-shrink-0 rounded p-1;
  @apply text-gray-400 hover:text-emerald-600 dark:hover:text-emerald-400;
  @apply hover:bg-emerald-50 dark:hover:bg-emerald-900/20;
  @apply transition-colors disabled:cursor-not-allowed disabled:opacity-50;
}

/* Dropdown animation (slides from the trigger side) */
.select-dropdown-enter-active,
.select-dropdown-leave-active {
  transition: opacity 0.2s ease, transform 0.2s ease;
}

.select-dropdown-enter-from,
.select-dropdown-leave-to {
  opacity: 0;
  transform: translateY(-8px);
}

.select-dropdown-top.select-dropdown-enter-from,
.select-dropdown-top.select-dropdown-leave-to {
  transform: translateY(8px);
}
</style>
