<template>
  <div class="relative" ref="containerRef" data-testid="proxy-selector">
    <button
      type="button"
      @click="toggle"
      :disabled="disabled"
      :class="[
        'select-trigger',
        isOpen && 'select-trigger-open',
        disabled && 'select-trigger-disabled'
      ]"
    >
      <span class="select-value">
        <ClashTag v-if="selectedClashExit" class="mr-1.5" />{{ selectedLabel }}
      </span>
      <span class="select-icon">
        <Icon
          name="chevronDown"
          size="md"
          :class="['transition-transform duration-200', isOpen && 'rotate-180']"
        />
      </span>
    </button>

    <Transition name="select-dropdown">
      <div v-if="isOpen" class="select-dropdown">
        <!-- Search and Batch Test Header -->
        <div class="select-header">
          <div class="select-search">
            <Icon name="search" size="sm" class="text-gray-400" />
            <input
              ref="searchInputRef"
              v-model="searchQuery"
              type="text"
              :placeholder="hasClashExits ? t('admin.clash.selector.searchPlaceholder') : t('admin.proxies.searchProxies')"
              class="select-search-input"
              @click.stop
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

        <!-- Options list -->
        <div :class="['select-options', hasClashExits && 'select-options-tall']">
          <!-- No Proxy option -->
          <div
            @click="selectOption(null)"
            :class="['select-option', modelValue === null && 'select-option-selected']"
          >
            <span class="select-option-label">{{ t('admin.accounts.noProxy') }}</span>
            <Icon v-if="modelValue === null" name="check" size="sm" class="text-primary-500" />
          </div>

          <!-- Proxy options -->
          <div
            v-for="proxy in filteredProxies"
            :key="proxy.id"
            @click="selectOption(proxy.id)"
            :class="['select-option', modelValue === proxy.id && 'select-option-selected']"
          >
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2">
                <span class="truncate font-medium">{{ proxy.name }}</span>
                <!-- Account count badge -->
                <span
                  v-if="proxy.account_count !== undefined"
                  class="inline-flex flex-shrink-0 items-center rounded bg-gray-100 px-1.5 py-0.5 text-xs text-gray-600 dark:bg-dark-600 dark:text-gray-400"
                >
                  {{ proxy.account_count }}
                </span>
                <!-- Test result badges -->
                <template v-if="testResults[proxy.id]">
                  <span
                    v-if="testResults[proxy.id].success"
                    class="inline-flex flex-shrink-0 items-center gap-1 rounded bg-emerald-100 px-1.5 py-0.5 text-xs text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400"
                  >
                    <span v-if="testResults[proxy.id].country">{{
                      testResults[proxy.id].country
                    }}</span>
                    <span v-if="testResults[proxy.id].latency_ms"
                      >{{ testResults[proxy.id].latency_ms }}ms</span
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
                {{ proxy.protocol }}://{{ proxy.host }}:{{ proxy.port }}
              </div>
            </div>

            <!-- Individual test button -->
            <button
              type="button"
              @click.stop="handleTestProxy(proxy)"
              :disabled="testingProxyIds.has(proxy.id)"
              class="test-btn"
              :title="t('admin.proxies.testConnection')"
            >
              <svg
                v-if="testingProxyIds.has(proxy.id)"
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
              v-if="modelValue === proxy.id"
              name="check"
              size="sm"
              class="flex-shrink-0 text-primary-500"
            />
          </div>

          <!-- Clash exits: one group per subscription, after the manual proxies -->
          <template v-for="group in filteredClashGroups" :key="`clash-group-${group.profileId}`">
            <div class="select-group-label" data-testid="clash-exit-group">
              <ClashTag />
              <span class="truncate">{{ group.profileName }}</span>
              <span class="ml-auto flex-shrink-0 font-normal tabular-nums">{{ group.options.length }}</span>
            </div>
            <div
              v-for="option in group.options"
              :key="`clash-exit-${option.exit.proxy_id}`"
              :data-testid="`clash-exit-${option.exit.proxy_id}`"
              :aria-disabled="option.disabled ? 'true' : undefined"
              :title="option.disabled ? option.blockReason : undefined"
              :class="[
                'select-option',
                modelValue === option.exit.proxy_id && 'select-option-selected',
                option.disabled && 'select-option-disabled'
              ]"
              @click="selectClashExit(option)"
            >
              <div class="min-w-0 flex-1">
                <div class="flex min-w-0 items-center gap-2">
                  <span class="min-w-0 truncate font-medium">{{ option.exit.node_name }}</span>
                  <span :class="['clash-chip flex-shrink-0', healthChipClass(option.exit.health_status)]">
                    <span class="h-1.5 w-1.5 flex-shrink-0 rounded-full" :class="healthDotClass(option.exit.health_status)"></span>
                    {{ healthLabel(option.exit) }}
                  </span>
                  <span
                    :class="['clash-chip min-w-0', occupancyChipClass(option)]"
                    :title="option.others.map((account) => account.name).join(', ') || undefined"
                    data-testid="clash-exit-occupancy"
                  >
                    <span class="truncate">{{ option.occupancyLabel }}</span>
                  </span>
                </div>
                <div class="mt-0.5 flex min-w-0 items-center gap-1.5 text-xs text-gray-500 dark:text-gray-400">
                  <CountryFlag v-if="option.exit.exit_ip" :code="option.exit.exit_country_code" :label="option.exit.exit_country" />
                  <span v-if="option.exit.exit_ip" class="font-mono">{{ option.exit.exit_ip }}</span>
                  <span v-else class="italic">{{ t('admin.clash.selector.exitUnprobed') }}</span>
                  <span v-if="option.location" class="truncate">· {{ option.location }}</span>
                </div>
                <div
                  v-if="option.unavailableText"
                  class="mt-0.5 truncate text-xs text-red-600 dark:text-red-400"
                  data-testid="clash-exit-block-reason"
                >
                  {{ option.unavailableText }}
                </div>
                <div
                  v-if="option.platformWarning"
                  class="mt-0.5 flex items-center gap-1 text-xs text-amber-600 dark:text-amber-400"
                  data-testid="clash-exit-platform-warning"
                >
                  <Icon name="exclamationTriangle" size="xs" class="flex-shrink-0" />
                  <span class="truncate">{{ t('admin.clash.selector.platformWarning') }}</span>
                </div>
              </div>
              <Icon
                v-if="modelValue === option.exit.proxy_id"
                name="check"
                size="sm"
                class="flex-shrink-0 text-primary-500"
              />
            </div>
          </template>

          <!-- Empty state -->
          <div
            v-if="filteredProxies.length === 0 && filteredClashCount === 0 && searchQuery"
            class="select-empty"
          >
            {{ t('common.noOptionsFound') }}
          </div>
        </div>
      </div>
    </Transition>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import Icon from '@/components/icons/Icon.vue'
import CountryFlag from '@/components/common/CountryFlag.vue'
import ClashTag from '@/components/common/ClashTag.vue'
import type { ClashBoundAccount, ClashExitList, ClashExitOption, ClashHealthStatus, Proxy } from '@/types'
import { mapWithConcurrency } from '@/utils/concurrency'
import { exitPlatformCheck, isBlockingPlatformCheck, localizeClashUnavailableReason } from '@/utils/clash'

const { t } = useI18n()

/** Upper bound of concurrent connectivity tests started by the batch button. */
const BATCH_TEST_CONCURRENCY = 4

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

interface ClashExitView {
  exit: ClashExitOption
  /** Accounts other than the edited one sharing this exit IP. */
  others: ClashBoundAccount[]
  full: boolean
  /** Unavailable / unprobed explanation (shown under the exit). */
  unavailableText: string
  /** Why the exit cannot be picked (unavailable, unprobed or full). */
  blockReason: string
  disabled: boolean
  occupancyLabel: string
  platformWarning: boolean
  location: string
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

const isOpen = ref(false)
const searchQuery = ref('')
const containerRef = ref<HTMLElement | null>(null)
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

const filteredProxies = computed(() => {
  if (!searchQuery.value) {
    return props.proxies
  }
  const query = searchQuery.value.toLowerCase()
  return props.proxies.filter((proxy) => {
    const name = proxy.name.toLowerCase()
    const host = proxy.host.toLowerCase()
    return name.includes(query) || host.includes(query)
  })
})

const clashExitViews = computed<ClashExitView[]>(() => {
  const list = props.clashExits
  if (!list) return []
  const maxPerExit = Math.max(1, list.max_accounts_per_exit || 1)
  return list.exits.map((exit) => {
    const others = (exit.occupants ?? []).filter(
      (account) => !account.is_shadow && account.id !== props.accountId
    )
    const full = others.length >= maxPerExit
    let unavailableText = ''
    if (!exit.available) {
      unavailableText = localizeClashUnavailableReason(exit.unavailable_reason, t)
    } else if (!exit.exit_ip && !list.allow_unprobed_exit_binding) {
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
    return {
      exit,
      others,
      full,
      unavailableText,
      blockReason,
      // The current binding stays selectable so saving other fields never forces a rebind.
      disabled: blockReason !== '' && props.modelValue !== exit.proxy_id,
      occupancyLabel,
      platformWarning: isBlockingPlatformCheck(exitPlatformCheck(exit, props.platform)),
      location: [exit.exit_country, exit.exit_city].filter(Boolean).join(' · ')
    }
  })
})

const filteredClashGroups = computed(() => {
  const query = searchQuery.value.trim().toLowerCase()
  const groups: Array<{ profileId: number; profileName: string; options: ClashExitView[] }> = []
  const byProfile = new Map<number, (typeof groups)[number]>()
  for (const option of clashExitViews.value) {
    const { exit } = option
    if (query) {
      const haystack = [
        exit.node_name,
        exit.profile_name,
        exit.exit_ip,
        exit.exit_country,
        exit.exit_country_code,
        exit.exit_city
      ]
      if (!haystack.some((value) => (value || '').toLowerCase().includes(query))) continue
    }
    let group = byProfile.get(exit.profile_id)
    if (!group) {
      group = { profileId: exit.profile_id, profileName: exit.profile_name || `#${exit.profile_id}`, options: [] }
      byProfile.set(exit.profile_id, group)
      groups.push(group)
    }
    group.options.push(option)
  }
  return groups
})

const filteredClashCount = computed(() =>
  filteredClashGroups.value.reduce((count, group) => count + group.options.length, 0)
)

const healthDotClass = (status: ClashHealthStatus) =>
  status === 'healthy' ? 'bg-emerald-500' : status === 'unhealthy' ? 'bg-red-500' : 'bg-gray-400'

const healthChipClass = (status: ClashHealthStatus) =>
  status === 'healthy'
    ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300'
    : status === 'unhealthy'
      ? 'bg-red-50 text-red-700 dark:bg-red-500/10 dark:text-red-300'
      : 'bg-gray-100 text-gray-600 dark:bg-dark-600 dark:text-gray-300'

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

const toggle = () => {
  if (props.disabled) return
  isOpen.value = !isOpen.value
  if (isOpen.value) {
    nextTick(() => {
      searchInputRef.value?.focus()
      // With Clash exits the list gets long: reveal the current binding.
      if (hasClashExits.value) {
        const selected = containerRef.value?.querySelector<HTMLElement>('.select-options .select-option-selected')
        selected?.scrollIntoView?.({ block: 'nearest' })
      }
    })
  }
}

const selectOption = (value: number | null) => {
  emit('update:modelValue', value)
  isOpen.value = false
  searchQuery.value = ''
}

const selectClashExit = (option: ClashExitView) => {
  if (option.disabled) return
  selectOption(option.exit.proxy_id)
}

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

const handleClickOutside = (event: MouseEvent) => {
  if (containerRef.value && !containerRef.value.contains(event.target as Node)) {
    isOpen.value = false
    searchQuery.value = ''
  }
}

const handleEscape = (event: KeyboardEvent) => {
  if (event.key === 'Escape' && isOpen.value) {
    isOpen.value = false
    searchQuery.value = ''
  }
}

onMounted(() => {
  document.addEventListener('click', handleClickOutside)
  document.addEventListener('keydown', handleEscape)
})

onUnmounted(() => {
  document.removeEventListener('click', handleClickOutside)
  document.removeEventListener('keydown', handleEscape)
})
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
  @apply flex-1 truncate text-left;
}

.select-icon {
  @apply flex-shrink-0 text-gray-400 dark:text-dark-400;
}

.select-dropdown {
  @apply absolute z-[100] mt-2 w-full;
  @apply bg-white dark:bg-dark-800;
  @apply rounded-xl;
  @apply border border-gray-200 dark:border-dark-700;
  @apply shadow-lg shadow-black/10 dark:shadow-black/30;
  @apply overflow-hidden;
}

.select-header {
  @apply flex items-center gap-2 px-3 py-2;
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

.select-options {
  @apply max-h-60 overflow-y-auto py-1;
}

.select-options-tall {
  @apply max-h-80;
}

.select-group-label {
  @apply mt-1 flex items-center gap-2 border-t border-gray-100 px-4 pb-1 pt-2;
  @apply text-xs font-semibold text-gray-500 dark:border-dark-700 dark:text-dark-400;
}

.select-option.select-option-disabled {
  @apply cursor-not-allowed opacity-60 hover:bg-transparent dark:hover:bg-transparent;
}

.clash-chip {
  @apply inline-flex max-w-[14rem] items-center gap-1 overflow-hidden whitespace-nowrap rounded px-1.5 py-0.5 text-xs;
}

.select-option {
  @apply flex items-center justify-between gap-2;
  @apply px-4 py-2.5 text-sm;
  @apply text-gray-700 dark:text-gray-300;
  @apply cursor-pointer transition-colors duration-150;
  @apply hover:bg-gray-50 dark:hover:bg-dark-700;
}

.select-option-selected {
  @apply bg-primary-50 dark:bg-primary-900/20;
  @apply text-primary-700 dark:text-primary-300;
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

/* Dropdown animation */
.select-dropdown-enter-active,
.select-dropdown-leave-active {
  transition: all 0.2s ease;
}

.select-dropdown-enter-from,
.select-dropdown-leave-to {
  opacity: 0;
  transform: translateY(-8px);
}
</style>
