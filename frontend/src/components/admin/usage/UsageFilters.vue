<template>
  <div :class="flat ? '' : 'card p-4 sm:p-5'">
    <!-- 常用筛选 + 操作 -->
    <div class="flex flex-wrap items-end gap-3">
      <!-- User Search -->
      <div ref="userSearchRef" class="usage-filter-dropdown relative w-full sm:w-auto sm:min-w-[220px]">
        <label class="usage-filter-label" for="usage-filter-user">{{ t('admin.usage.userFilter') }}</label>
        <div class="relative">
          <input
            id="usage-filter-user"
            v-model="userKeyword"
            type="text"
            class="input pr-8"
            :placeholder="t('admin.usage.searchUserPlaceholder')"
            autocomplete="off"
            @input="debounceUserSearch"
            @focus="showUserDropdown = true"
          />
          <button
            v-if="filters.user_id"
            type="button"
            @click="clearUser"
            class="usage-filter-clear"
            aria-label="Clear user filter"
          >
            <Icon name="x" size="xs" :stroke-width="2" />
          </button>
        </div>
        <div
          v-if="showUserDropdown && (userResults.length > 0 || userKeyword)"
          class="usage-filter-results"
        >
          <button
            v-for="u in userResults"
            :key="u.id"
            type="button"
            @click="selectUser(u)"
            class="usage-filter-result"
          >
            <span class="truncate">{{ u.email }}<span v-if="u.deleted" class="ml-1 text-xs text-gray-400">（{{ t('admin.usage.userDeletedBadge') }}）</span></span>
            <span class="ml-2 shrink-0 text-xs text-gray-400">#{{ u.id }}</span>
          </button>
        </div>
      </div>

      <!-- API Key Search -->
      <div ref="apiKeySearchRef" class="usage-filter-dropdown relative w-full sm:w-auto sm:min-w-[200px]">
        <label class="usage-filter-label" for="usage-filter-api-key">{{ t('usage.apiKeyFilter') }}</label>
        <div class="relative">
          <input
            id="usage-filter-api-key"
            v-model="apiKeyKeyword"
            type="text"
            class="input pr-8"
            :placeholder="t('admin.usage.searchApiKeyPlaceholder')"
            autocomplete="off"
            @input="debounceApiKeySearch"
            @focus="onApiKeyFocus"
          />
          <button
            v-if="filters.api_key_id"
            type="button"
            @click="onClearApiKey"
            class="usage-filter-clear"
            aria-label="Clear API key filter"
          >
            <Icon name="x" size="xs" :stroke-width="2" />
          </button>
        </div>
        <div
          v-if="showApiKeyDropdown && apiKeyResults.length > 0"
          class="usage-filter-results"
        >
          <button
            v-for="k in apiKeyResults"
            :key="k.id"
            type="button"
            @click="selectApiKey(k)"
            class="usage-filter-result"
          >
            <span class="truncate">{{ k.name || `#${k.id}` }}</span>
            <span class="ml-2 shrink-0 text-xs text-gray-400">#{{ k.id }}</span>
          </button>
        </div>
      </div>

      <!-- Model Filter -->
      <div class="w-full sm:w-auto sm:min-w-[200px]">
        <label class="usage-filter-label">{{ t('usage.model') }}</label>
        <Select :model-value="filters.model ?? null" :options="modelOptions" searchable :aria-label="t('usage.model')" @update:model-value="(value) => (filters.model = value)" @change="emitChange" />
      </div>

      <!-- Group Filter -->
      <div class="w-full sm:w-auto sm:min-w-[180px]">
        <label class="usage-filter-label">{{ t('admin.usage.group') }}</label>
        <Select :model-value="filters.group_id ?? null" :options="groupOptions" searchable :aria-label="t('admin.usage.group')" @update:model-value="(value) => (filters.group_id = value)" @change="emitChange" />
      </div>

      <!-- Error Phase Filter (errors only) -->
      <div v-if="mode === 'errors'" class="w-full sm:w-auto sm:min-w-[160px]">
        <label class="usage-filter-label">{{ t('admin.ops.errorLog.type') }}</label>
        <Select v-model="filters.error_phase" :options="errorPhaseOptions" :aria-label="t('admin.ops.errorLog.type')" @change="emitChange" />
      </div>

      <!-- Error Category Filter (errors only) -->
      <div v-if="mode === 'errors'" class="w-full sm:w-auto sm:min-w-[160px]">
        <label class="usage-filter-label">{{ t('usage.errors.category') }}</label>
        <Select v-model="filters.error_category" :options="errorCategoryOptions" :aria-label="t('usage.errors.category')" @change="emitChange" />
      </div>

      <!-- Status Code Filter (errors only) -->
      <div v-if="mode === 'errors'" class="w-full sm:w-auto sm:min-w-[140px]">
        <label class="usage-filter-label">{{ t('admin.ops.errorLog.status') }}</label>
        <Select v-model="filters.status_code" :options="statusCodeOptions" :aria-label="t('admin.ops.errorLog.status')" @change="emitChange" />
      </div>

      <!-- 更多筛选（可折叠时显示） -->
      <button
        v-if="collapsible"
        type="button"
        class="btn btn-secondary"
        :aria-expanded="advancedOpen"
        aria-controls="usage-advanced-filters"
        @click="advancedOpen = !advancedOpen"
      >
        <Icon name="adjustments" size="sm" />
        {{ t('admin.usage.moreFilters') }}
        <span
          v-if="advancedActiveCount > 0"
          class="rounded-full bg-primary-100 px-1.5 text-[11px] font-semibold leading-4 tabular-nums text-primary-800 dark:bg-primary-500/20 dark:text-primary-200"
        >{{ advancedActiveCount }}</span>
        <Icon name="chevronDown" size="xs" class="h-3.5 w-3.5 transition-transform" :class="advancedOpen ? 'rotate-180' : ''" :stroke-width="2" />
      </button>

      <!-- Right: actions -->
      <div v-if="showActions" class="flex w-full flex-wrap items-center justify-end gap-2 sm:ml-auto sm:w-auto">
        <button
          type="button"
          @click="$emit('refresh')"
          class="btn btn-secondary px-2.5"
          :title="t('common.refresh')"
          :aria-label="t('common.refresh')"
        >
          <Icon name="refresh" size="sm" />
        </button>
        <button type="button" @click="$emit('reset')" class="btn btn-ghost">
          {{ t('common.reset') }}
        </button>
        <slot name="after-reset" />
        <template v-if="mode === 'usage'">
          <button
            type="button"
            @click="$emit('cleanup')"
            class="btn btn-secondary text-red-600 hover:!border-red-200 hover:!bg-red-50 dark:text-red-400 dark:hover:!border-red-500/30 dark:hover:!bg-red-500/10"
          >
            <Icon name="trash" size="sm" />
            {{ t('admin.usage.cleanup.button') }}
          </button>
          <button type="button" @click="$emit('export')" :disabled="exporting" class="btn btn-primary">
            <Icon name="download" size="sm" />
            {{ t('usage.exportExcel') }}
          </button>
        </template>
      </div>
    </div>

    <!-- 低频筛选：可折叠 -->
    <div
      v-show="!collapsible || advancedOpen"
      id="usage-advanced-filters"
      class="flex flex-wrap items-end gap-3"
      :class="collapsible ? 'mt-3 border-t border-dashed border-gray-200 pt-3 dark:border-dark-700' : 'mt-3'"
    >
      <!-- Account Filter -->
      <div ref="accountSearchRef" class="usage-filter-dropdown relative w-full sm:w-auto sm:min-w-[200px]">
        <label class="usage-filter-label" for="usage-filter-account">{{ t('admin.usage.account') }}</label>
        <div class="relative">
          <input
            id="usage-filter-account"
            v-model="accountKeyword"
            type="text"
            class="input pr-8"
            :placeholder="t('admin.usage.searchAccountPlaceholder')"
            autocomplete="off"
            @input="debounceAccountSearch"
            @focus="showAccountDropdown = true"
          />
          <button
            v-if="filters.account_id"
            type="button"
            @click="clearAccount"
            class="usage-filter-clear"
            aria-label="Clear account filter"
          >
            <Icon name="x" size="xs" :stroke-width="2" />
          </button>
        </div>
        <div
          v-if="showAccountDropdown && (accountResults.length > 0 || accountKeyword)"
          class="usage-filter-results"
        >
          <button
            v-for="a in accountResults"
            :key="a.id"
            type="button"
            @click="selectAccount(a)"
            class="usage-filter-result"
          >
            <span class="truncate">{{ a.name }}</span>
            <span class="ml-2 shrink-0 text-xs text-gray-400">#{{ a.id }}</span>
          </button>
        </div>
      </div>

      <!-- Request Type Filter (usage only) -->
      <div v-if="mode !== 'errors'" class="w-full sm:w-auto sm:min-w-[160px]">
        <label class="usage-filter-label">{{ t('usage.type') }}</label>
        <Select v-model="filters.request_type" :options="requestTypeOptions" :aria-label="t('usage.type')" @change="emitChange" />
      </div>

      <!-- Native compaction is independent of the transport request type. -->
      <div v-if="mode !== 'errors'" class="w-full sm:w-auto sm:min-w-[160px]">
        <label class="usage-filter-label">{{ t('usage.compactionFilter') }}</label>
        <Select v-model="filters.native_compaction_v2" :options="compactionOptions" :aria-label="t('usage.compactionFilter')" @change="emitChange" />
      </div>

      <!-- Billing Type Filter (usage only) -->
      <div v-if="mode !== 'errors'" class="w-full sm:w-auto sm:min-w-[160px]">
        <label class="usage-filter-label">{{ t('admin.usage.billingType') }}</label>
        <Select v-model="filters.billing_type" :options="billingTypeOptions" :aria-label="t('admin.usage.billingType')" @change="emitChange" />
      </div>

      <!-- Billing Mode Filter (usage only；用户排行的 user-breakdown 接口不支持该维度) -->
      <div v-if="mode === 'usage'" class="w-full sm:w-auto sm:min-w-[160px]">
        <label class="usage-filter-label">{{ t('admin.usage.billingMode') }}</label>
        <Select v-model="filters.billing_mode" :options="billingModeOptions" :aria-label="t('admin.usage.billingMode')" @change="emitChange" />
      </div>

      <div v-if="mode === 'usage'" class="w-full sm:w-auto sm:min-w-[200px]">
        <label class="usage-filter-label">{{ t('admin.usage.upstreamModelAudit') }}</label>
        <Select v-model="filters.upstream_model_mismatch" :options="upstreamModelMismatchOptions" :aria-label="t('admin.usage.upstreamModelAudit')" @change="emitChange" />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, toRef, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import { COMMON_ERROR_STATUS_CODES } from '@/utils/errorBadges'
import type { SimpleApiKey, SimpleUser } from '@/api/admin/usage'

type ModelValue = Record<string, any>

interface Props {
  modelValue: ModelValue
  exporting: boolean
  startDate: string
  endDate: string
  showActions?: boolean
  modelOptions?: string[]
  /**
   * errors 模式:隐藏用量专属字段/按钮,显示错误类型+状态码(错误请求 tab 用)
   * ranking 模式:同 usage 但隐藏计费模式筛选与清理/导出按钮(用户排行 tab 用)
   */
  mode?: 'usage' | 'errors' | 'ranking'
  /** 嵌入统一卡片内使用：去掉自身卡片外观 */
  flat?: boolean
  /** 低频筛选收进“更多筛选”折叠区（统计页用；清理对话框保持全部展开） */
  collapsible?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  showActions: true,
  mode: 'usage',
  flat: false,
  collapsible: false
})
const emit = defineEmits([
  'update:modelValue',
  'change',
  'refresh',
  'reset',
  'export',
  'cleanup'
])

const { t } = useI18n()
const filters = toRef(props, 'modelValue')

const userSearchRef = ref<HTMLElement | null>(null)
const apiKeySearchRef = ref<HTMLElement | null>(null)
const accountSearchRef = ref<HTMLElement | null>(null)

const userKeyword = ref('')
const userResults = ref<SimpleUser[]>([])
const showUserDropdown = ref(false)
let userSearchTimeout: ReturnType<typeof setTimeout> | null = null
let userSearchSequence = 0

const apiKeyKeyword = ref('')
const apiKeyResults = ref<SimpleApiKey[]>([])
const showApiKeyDropdown = ref(false)
let apiKeySearchTimeout: ReturnType<typeof setTimeout> | null = null

interface SimpleAccount {
  id: number
  name: string
}
const accountKeyword = ref('')
const accountResults = ref<SimpleAccount[]>([])
const showAccountDropdown = ref(false)
let accountSearchTimeout: ReturnType<typeof setTimeout> | null = null

const modelOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('admin.usage.allModels') },
  ...(props.modelOptions ?? []).map((m) => ({ value: m, label: m })),
])
const groupOptions = ref<SelectOption[]>([{ value: null, label: t('admin.usage.allGroups') }])

const requestTypeOptions = ref<SelectOption[]>([
  { value: null, label: t('admin.usage.allTypes') },
  { value: 'ws_v2', label: t('usage.ws') },
  { value: 'live', label: t('usage.live') },
  { value: 'stream', label: t('usage.stream') },
  { value: 'sync', label: t('usage.sync') },
  { value: 'cyber', label: t('usage.cyber') }
])

const compactionOptions = ref<SelectOption[]>([
  { value: null, label: t('usage.allCompactionTypes') },
  { value: true, label: t('usage.compactionOnly') }
])

const billingTypeOptions = ref<SelectOption[]>([
  { value: null, label: t('admin.usage.allBillingTypes') },
  { value: 0, label: t('admin.usage.billingTypeBalance') },
  { value: 1, label: t('admin.usage.billingTypeSubscription') }
])

// 错误类型对应后端 phase 参数(与错误表"类型"徽章同语义)
const errorPhaseOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('admin.usage.allTypes') },
  { value: 'upstream', label: t('admin.ops.errorLog.typeUpstream') },
  { value: 'account_auth', label: t('admin.ops.errorLog.typeAccountAuth') },
  { value: 'request', label: t('admin.ops.errorLog.typeRequest') },
  { value: 'auth', label: t('admin.ops.errorLog.typeAuth') },
  { value: 'routing', label: t('admin.ops.errorLog.typeRouting') },
  { value: 'internal', label: t('admin.ops.errorLog.typeInternal') },
])

// 分类码同用户端 /usage 错误筛选;"other" 无法反查为过滤条件,刻意不列
const errorCategoryCodes = ['auth', 'rate_limit', 'quota', 'invalid_request', 'service_unavailable', 'upstream', 'internal', 'cyber']

const errorCategoryOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('usage.errors.allCategories') },
  ...errorCategoryCodes.map((c) => ({ value: c, label: t('usage.errors.categories.' + c) })),
])

const statusCodeOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('usage.errors.allStatuses') },
  ...COMMON_ERROR_STATUS_CODES.map((c) => ({ value: c, label: String(c) })),
])

const billingModeOptions = ref<SelectOption[]>([
  { value: null, label: t('admin.usage.allBillingModes') },
  { value: 'token', label: t('admin.usage.billingModeToken') },
  { value: 'per_request', label: t('admin.usage.billingModePerRequest') },
  { value: 'image', label: t('admin.usage.billingModeImage') },
  { value: 'video', label: t('admin.usage.billingModeVideo') }
])

const upstreamModelMismatchOptions = ref<SelectOption[]>([
  { value: null, label: t('admin.usage.allUpstreamModelAudit') },
  { value: true, label: t('admin.usage.upstreamModelMismatchOnly') },
  { value: false, label: t('admin.usage.upstreamModelMatchedOnly') }
])

const emitChange = () => emit('change')

// ==================== 更多筛选（折叠区） ====================
const advancedOpen = ref(false)

// 折叠区里当前生效的条件数，显示在按钮上，避免“看不见的筛选”
const advancedActiveCount = computed(() => {
  const f = filters.value
  let count = 0
  if (f.account_id) count++
  if (props.mode !== 'errors') {
    if (f.request_type != null && f.request_type !== '') count++
    if (f.native_compaction_v2 != null) count++
    if (f.billing_type != null) count++
  }
  if (props.mode === 'usage') {
    if (f.billing_mode) count++
    if (f.upstream_model_mismatch != null) count++
  }
  return count
})

// 带着条件进入（如从其他页面下钻）时自动展开，保证已生效的筛选可见
watch(
  advancedActiveCount,
  (count, previous) => {
    if (count > 0 && !previous) advancedOpen.value = true
  },
  { immediate: true }
)

const clearPendingUserSearch = () => {
  if (userSearchTimeout) {
    clearTimeout(userSearchTimeout)
    userSearchTimeout = null
  }
  userSearchSequence += 1
}

const debounceUserSearch = () => {
  clearPendingUserSearch()
  const query = userKeyword.value.trim()
  if (!query) {
    userResults.value = []
    return
  }

  const sequence = userSearchSequence
  userSearchTimeout = setTimeout(async () => {
    userSearchTimeout = null
    try {
      const results = await adminAPI.usage.searchUsers(query)
      if (sequence === userSearchSequence) {
        userResults.value = results.sort((a, b) => Number(a.deleted) - Number(b.deleted))
      }
    } catch {
      if (sequence === userSearchSequence) {
        userResults.value = []
      }
    }
  }, 300)
}

const debounceApiKeySearch = () => {
  if (apiKeySearchTimeout) clearTimeout(apiKeySearchTimeout)
  apiKeySearchTimeout = setTimeout(async () => {
    try {
      apiKeyResults.value = await adminAPI.usage.searchApiKeys(
        filters.value.user_id,
        apiKeyKeyword.value || ''
      )
    } catch {
      apiKeyResults.value = []
    }
  }, 300)
}

const selectUser = async (u: SimpleUser) => {
  clearPendingUserSearch()
  userKeyword.value = u.email
  showUserDropdown.value = false
  filters.value.user_id = u.id
  clearApiKey()

  // Auto-load API keys for this user
  try {
    apiKeyResults.value = await adminAPI.usage.searchApiKeys(u.id, '')
  } catch {
    apiKeyResults.value = []
  }

  emitChange()
}

const clearUser = () => {
  clearPendingUserSearch()
  userKeyword.value = ''
  userResults.value = []
  showUserDropdown.value = false
  filters.value.user_id = undefined
  clearApiKey()
  emitChange()
}

const selectApiKey = (k: SimpleApiKey) => {
  apiKeyKeyword.value = k.name || String(k.id)
  showApiKeyDropdown.value = false
  filters.value.api_key_id = k.id
  emitChange()
}

const clearApiKey = () => {
  apiKeyKeyword.value = ''
  apiKeyResults.value = []
  showApiKeyDropdown.value = false
  filters.value.api_key_id = undefined
}

const onClearApiKey = () => {
  clearApiKey()
  emitChange()
}

const debounceAccountSearch = () => {
  if (accountSearchTimeout) clearTimeout(accountSearchTimeout)
  accountSearchTimeout = setTimeout(async () => {
    if (!accountKeyword.value) {
      accountResults.value = []
      return
    }
    try {
      const res = await adminAPI.accounts.list(1, 20, { search: accountKeyword.value })
      accountResults.value = res.items.map((a) => ({ id: a.id, name: a.name }))
    } catch {
      accountResults.value = []
    }
  }, 300)
}

const selectAccount = (a: SimpleAccount) => {
  accountKeyword.value = a.name
  showAccountDropdown.value = false
  filters.value.account_id = a.id
  emitChange()
}

const clearAccount = () => {
  accountKeyword.value = ''
  accountResults.value = []
  showAccountDropdown.value = false
  filters.value.account_id = undefined
  emitChange()
}

const onApiKeyFocus = () => {
  showApiKeyDropdown.value = true
  // Trigger search if no results yet
  if (apiKeyResults.value.length === 0) {
    debounceApiKeySearch()
  }
}

const onDocumentClick = (e: MouseEvent) => {
  const target = e.target as Node | null
  if (!target) return

  const clickedInsideUser = userSearchRef.value?.contains(target) ?? false
  const clickedInsideApiKey = apiKeySearchRef.value?.contains(target) ?? false
  const clickedInsideAccount = accountSearchRef.value?.contains(target) ?? false

  if (!clickedInsideUser) showUserDropdown.value = false
  if (!clickedInsideApiKey) showApiKeyDropdown.value = false
  if (!clickedInsideAccount) showAccountDropdown.value = false
}

watch(
  () => props.startDate,
  (value) => {
    filters.value.start_date = value
  },
  { immediate: true }
)

watch(
  () => props.endDate,
  (value) => {
    filters.value.end_date = value
  },
  { immediate: true }
)

watch(
  () => filters.value.user_id,
  (userId) => {
    if (!userId) {
      clearPendingUserSearch()
      userKeyword.value = ''
      userResults.value = []
    }
  }
)

watch(
  () => filters.value.api_key_id,
  (apiKeyId) => {
    if (!apiKeyId) {
      apiKeyKeyword.value = ''
      apiKeyResults.value = []
    }
  }
)

watch(
  () => filters.value.account_id,
  (accountId) => {
    if (!accountId) {
      accountKeyword.value = ''
      accountResults.value = []
    }
  }
)

onMounted(async () => {
  document.addEventListener('click', onDocumentClick)
  try {
    const gs = await adminAPI.groups.list(1, 1000)
    groupOptions.value.push(...gs.items.map((g: any) => ({ value: g.id, label: g.name })))
  } catch {
    // Ignore filter option loading errors (page still usable)
  }
})

onUnmounted(() => {
  clearPendingUserSearch()
  document.removeEventListener('click', onDocumentClick)
})

// 供外部(如用户排行下钻)在程序化设置 user_id 后回显选中的用户邮箱
const setUserKeyword = (email: string) => {
  clearPendingUserSearch()
  userKeyword.value = email
  userResults.value = []
  showUserDropdown.value = false
}

const getUserSearchRevision = () => userSearchSequence

defineExpose({ getUserSearchRevision, setUserKeyword })
</script>

<style scoped>
.usage-filter-label {
  @apply mb-1 block text-xs font-medium text-gray-500 dark:text-dark-400;
}

.usage-filter-clear {
  @apply absolute right-1.5 top-1/2 flex h-6 w-6 -translate-y-1/2 items-center justify-center rounded text-gray-400 transition-colors;
  @apply hover:bg-gray-100 hover:text-gray-600 dark:hover:bg-dark-700 dark:hover:text-gray-200;
}

.usage-filter-results {
  @apply absolute z-50 mt-1 max-h-60 w-full min-w-[16rem] overflow-auto rounded-lg border border-gray-200 bg-white p-1 shadow-lg;
  @apply dark:border-dark-700 dark:bg-dark-800;
}

.usage-filter-result {
  @apply flex w-full items-center justify-between rounded-md px-2.5 py-1.5 text-left text-sm text-gray-700 transition-colors;
  @apply hover:bg-gray-100 dark:text-gray-200 dark:hover:bg-dark-700;
}
</style>
