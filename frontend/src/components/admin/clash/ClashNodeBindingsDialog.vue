<template>
  <BaseDialog
    :show="show"
    :title="t('admin.clash.nodes.bindings.title', { name: node?.name ?? '' })"
    width="wide"
    @close="close"
  >
    <div v-if="node" class="space-y-5 text-sm" data-testid="clash-node-bindings">
      <div class="flex flex-wrap items-center gap-x-4 gap-y-1.5 rounded-lg bg-gray-50 px-3 py-2.5 dark:bg-dark-800/70">
        <span class="flex items-center gap-1.5">
          <CountryFlag v-if="node.exit_ip" :code="node.exit_country_code" :label="node.exit_country" />
          <span v-if="node.exit_ip" class="font-mono text-gray-900 dark:text-gray-100">{{ node.exit_ip }}</span>
          <span v-else class="italic text-gray-400 dark:text-dark-500">{{ t('admin.clash.nodes.exitUnprobed') }}</span>
        </span>
        <span v-if="maxPerExit" class="tabular-nums text-gray-700 dark:text-gray-300" data-testid="clash-bindings-occupancy">
          {{ t('admin.clash.nodes.bindings.occupancy', { used: occupants.length, max: maxPerExit }) }}
        </span>
        <span v-if="otherNodeOccupants > 0" class="text-xs text-gray-500 dark:text-dark-400">
          {{ t('admin.clash.nodes.bindings.otherNodes', { count: otherNodeOccupants }) }}
        </span>
      </div>

      <div
        v-if="blockReason"
        role="alert"
        class="rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200"
        data-testid="clash-bindings-blocked"
      >
        {{ blockReason }}
      </div>

      <section>
        <h4 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.clash.nodes.bindings.current') }}</h4>
        <ul
          v-if="bound.length > 0"
          class="mt-2 divide-y divide-gray-100 rounded-lg border border-gray-200 dark:divide-dark-700 dark:border-dark-700"
          data-testid="clash-bindings-current"
        >
          <li v-for="account in bound" :key="account.id" class="flex items-center gap-2 px-3 py-2">
            <PlatformIcon :platform="account.platform as GroupPlatform" size="xs" />
            <span class="min-w-0 truncate text-gray-900 dark:text-gray-100" :title="account.name">{{ account.name }}</span>
            <span v-if="account.is_shadow" class="badge badge-gray">{{ t('admin.clash.nodes.shadowTag') }}</span>
            <div class="ml-auto flex-shrink-0">
              <span v-if="account.is_shadow" class="text-xs text-gray-400 dark:text-dark-500">
                {{ t('admin.clash.nodes.bindings.followsParent') }}
              </span>
              <button
                v-else-if="!readonly"
                type="button"
                class="btn btn-secondary btn-sm"
                :disabled="working"
                data-testid="clash-bindings-unbind"
                @click="askUnbind(account)"
              >
                {{ t('admin.clash.actions.unbind') }}
              </button>
            </div>
          </li>
        </ul>
        <p v-else class="mt-2 text-gray-500 dark:text-dark-400">{{ t('admin.clash.nodes.bindings.none') }}</p>

        <div
          v-if="pendingUnbind"
          class="mt-3 rounded-lg border border-amber-200 bg-amber-50 p-3 text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-100"
          data-testid="clash-bindings-unbind-confirm"
        >
          <p>{{ t('admin.clash.nodes.bindings.unbindConfirm', { name: pendingUnbind.name }) }}</p>
          <label class="mt-2 flex cursor-pointer items-center gap-2">
            <input
              v-model="pauseOnUnbind"
              type="checkbox"
              class="h-4 w-4 rounded border-amber-300 text-primary-600 focus:ring-primary-500"
              data-testid="clash-bindings-pause"
            />
            <span>{{ t('admin.clash.nodes.bindings.pauseOnUnbind') }}</span>
          </label>
          <div class="mt-3 flex justify-end gap-2">
            <button type="button" class="btn btn-secondary btn-sm" :disabled="working" @click="pendingUnbind = null">
              {{ t('common.cancel') }}
            </button>
            <button
              type="button"
              class="btn btn-danger btn-sm"
              :disabled="working"
              data-testid="clash-bindings-unbind-submit"
              @click="confirmUnbind"
            >
              {{ working ? t('common.processing') : t('admin.clash.nodes.bindings.confirmUnbind') }}
            </button>
          </div>
        </div>
      </section>

      <section v-if="!readonly">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <h4 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.clash.nodes.bindings.add') }}</h4>
          <span v-if="maxPerExit && !blockReason" class="text-xs" :class="remaining > 0 ? 'text-gray-500 dark:text-dark-400' : 'text-amber-700 dark:text-amber-300'">
            {{ remaining > 0 ? t('admin.clash.nodes.bindings.remaining', { count: remaining - selected.size }) : t('admin.clash.nodes.bindings.full') }}
          </span>
        </div>
        <div class="relative mt-2">
          <Icon
            name="search"
            size="sm"
            class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 dark:text-dark-400"
          />
          <input
            v-model="search"
            type="text"
            class="input pl-9"
            :placeholder="t('admin.clash.nodes.bindings.searchPlaceholder')"
            :aria-label="t('admin.clash.nodes.bindings.searchPlaceholder')"
            data-testid="clash-bindings-search"
            @input="onSearchInput"
          />
        </div>
        <p v-if="candidatesError" class="mt-2 text-red-600 dark:text-red-400">{{ candidatesError }}</p>
        <ul
          class="mt-2 max-h-72 divide-y divide-gray-100 overflow-y-auto rounded-lg border border-gray-200 dark:divide-dark-700 dark:border-dark-700"
          data-testid="clash-bindings-candidates"
        >
          <li v-if="candidatesLoading && candidates.length === 0" class="px-3 py-6 text-center text-gray-400 dark:text-dark-500">
            {{ t('common.loading') }}
          </li>
          <li v-else-if="candidates.length === 0" class="px-3 py-6 text-center text-gray-400 dark:text-dark-500">
            {{ t('admin.clash.nodes.bindings.noResults') }}
          </li>
          <li v-for="account in candidates" :key="account.id">
            <label
              class="flex items-center gap-2 px-3 py-2"
              :class="candidateBlocked(account) ? 'cursor-not-allowed opacity-60' : 'cursor-pointer hover:bg-gray-50 dark:hover:bg-dark-800'"
            >
              <input
                type="checkbox"
                class="h-4 w-4 flex-shrink-0 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-500 dark:bg-dark-800"
                :checked="selected.has(account.id)"
                :disabled="candidateBlocked(account) || (!selected.has(account.id) && selectionFull)"
                data-testid="clash-bindings-candidate"
                @change="toggle(account)"
              />
              <PlatformIcon :platform="account.platform as GroupPlatform" size="xs" />
              <div class="min-w-0 flex-1">
                <p class="truncate text-gray-900 dark:text-gray-100" :title="account.name">{{ account.name }}</p>
                <p class="truncate text-xs text-gray-500 dark:text-dark-400">{{ candidateNote(account) }}</p>
              </div>
              <span
                v-if="platformWarning(account)"
                class="flex-shrink-0 text-amber-500"
                :title="t('admin.clash.nodes.bindings.platformWarning')"
                data-testid="clash-bindings-platform-warning"
              >
                <Icon name="exclamationTriangle" size="sm" />
              </span>
            </label>
          </li>
        </ul>
        <div v-if="pages > 1" class="mt-2 flex items-center justify-end gap-2 text-xs text-gray-500 dark:text-dark-400">
          <button
            type="button"
            class="icon-btn"
            :disabled="page <= 1 || candidatesLoading"
            :aria-label="t('admin.clash.nodes.bindings.prevPage')"
            @click="goToPage(page - 1)"
          >
            <Icon name="chevronLeft" size="sm" />
          </button>
          <span class="tabular-nums">{{ page }} / {{ pages }}</span>
          <button
            type="button"
            class="icon-btn"
            :disabled="page >= pages || candidatesLoading"
            :aria-label="t('admin.clash.nodes.bindings.nextPage')"
            @click="goToPage(page + 1)"
          >
            <Icon name="chevronRight" size="sm" />
          </button>
        </div>
      </section>
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="working" @click="close">{{ t('common.close') }}</button>
        <button
          v-if="!readonly"
          type="button"
          class="btn btn-primary"
          :disabled="selected.size === 0 || working || !!blockReason"
          data-testid="clash-bindings-submit"
          @click="bindSelected"
        >
          {{ working ? t('admin.clash.nodes.bindings.binding') : t('admin.clash.nodes.bindings.submit', { count: selected.size }) }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import BaseDialog from '@/components/common/BaseDialog.vue'
import CountryFlag from '@/components/common/CountryFlag.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import Icon from '@/components/icons/Icon.vue'
import type { AccountListItem, ClashBoundAccount, ClashExitList, ClashNode, GroupPlatform } from '@/types'
import { extractApiErrorMessage } from '@/utils/apiError'
import {
  clashCheckPlatformFor,
  clashErrorMessage,
  isBlockingPlatformCheck,
  localizeClashUnavailableReason
} from '@/utils/clash'

const PAGE_SIZE = 8

const props = withDefaults(
  defineProps<{
    show: boolean
    node: ClashNode | null
    readonly?: boolean
  }>(),
  { readonly: false }
)

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'changed'): void
}>()

const { t } = useI18n()
const appStore = useAppStore()

const bound = ref<ClashBoundAccount[]>([])
const exits = ref<ClashExitList | null>(null)
const manualProxyNames = ref(new Map<number, string>())
const candidates = ref<AccountListItem[]>([])
const candidatesLoading = ref(false)
const candidatesError = ref('')
const search = ref('')
const page = ref(1)
const total = ref(0)
const selected = ref(new Set<number>())
const working = ref(false)
const pendingUnbind = ref<ClashBoundAccount | null>(null)
const pauseOnUnbind = ref(true)

const describeError = (error: unknown, fallbackKey: string) =>
  clashErrorMessage(error, t) ?? extractApiErrorMessage(error, t(fallbackKey))

const exitOption = computed(() => exits.value?.exits.find((exit) => exit.node_id === props.node?.id))
const maxPerExit = computed(() => exits.value?.max_accounts_per_exit ?? null)
// Occupancy is counted per egress IP across nodes; shadows never count.
const occupants = computed(() => exitOption.value?.occupants ?? bound.value.filter((account) => !account.is_shadow))
const otherNodeOccupants = computed(() => {
  const here = new Set(bound.value.map((account) => account.id))
  return occupants.value.filter((account) => !here.has(account.id)).length
})
const remaining = computed(() => (maxPerExit.value ? Math.max(maxPerExit.value - occupants.value.length, 0) : Infinity))
const selectionFull = computed(() => selected.value.size >= remaining.value)
const pages = computed(() => Math.max(1, Math.ceil(total.value / PAGE_SIZE)))

const blockReason = computed(() => {
  const node = props.node
  if (!node || props.readonly) return ''
  if (!node.available) {
    return t('admin.clash.nodes.bindings.unavailable', {
      reason: localizeClashUnavailableReason(node.unavailable_reason, t) || '-'
    })
  }
  if (!node.exit_ip && exits.value && !exits.value.allow_unprobed_exit_binding) {
    return t('admin.clash.nodes.bindings.unprobed')
  }
  return ''
})

const exitNodeNames = computed(() => {
  const names = new Map<number, string>()
  for (const exit of exits.value?.exits ?? []) names.set(exit.proxy_id, exit.node_name)
  return names
})

const candidateBlocked = (account: AccountListItem) =>
  !!account.parent_account_id || account.custom_base_url_enabled === true || account.proxy_id === props.node?.proxy_id

function candidateNote(account: AccountListItem): string {
  if (account.parent_account_id) return t('admin.clash.nodes.bindings.shadowHint')
  if (account.custom_base_url_enabled === true) return t('admin.clash.nodes.bindings.relayHint')
  if (account.proxy_id == null) return t('admin.clash.nodes.bindings.currentNone')
  if (account.proxy_id === props.node?.proxy_id) return t('admin.clash.nodes.bindings.currentHere')
  const clashNode = exitNodeNames.value.get(account.proxy_id)
  if (clashNode) return t('admin.clash.nodes.bindings.currentClash', { name: clashNode })
  const manual = manualProxyNames.value.get(account.proxy_id)
  return t('admin.clash.nodes.bindings.currentManual', { name: manual ?? `#${account.proxy_id}` })
}

function platformWarning(account: AccountListItem): boolean {
  const target = clashCheckPlatformFor(account.platform)
  return !!target && isBlockingPlatformCheck(props.node?.platform_checks.results[target])
}

function toggle(account: AccountListItem) {
  const next = new Set(selected.value)
  if (next.has(account.id)) next.delete(account.id)
  else if (!candidateBlocked(account) && !selectionFull.value) next.add(account.id)
  selected.value = next
}

async function loadExits() {
  try {
    exits.value = await adminAPI.clash.listExits()
  } catch {
    // Occupancy is informative; the server still enforces the limit on save.
    exits.value = null
  }
}

async function loadManualProxies() {
  try {
    const proxies = await adminAPI.proxies.getAll()
    manualProxyNames.value = new Map(proxies.map((proxy) => [proxy.id, proxy.name]))
  } catch {
    manualProxyNames.value = new Map()
  }
}

let candidatesRequest = 0
async function loadCandidates() {
  const request = ++candidatesRequest
  candidatesLoading.value = true
  try {
    const response = await adminAPI.accounts.list(page.value, PAGE_SIZE, {
      search: search.value.trim() || undefined,
      lite: '1'
    })
    if (request !== candidatesRequest) return
    candidates.value = Array.isArray(response?.items) ? response.items : []
    total.value = response?.total ?? 0
    candidatesError.value = ''
  } catch (error) {
    if (request !== candidatesRequest) return
    candidatesError.value = describeError(error, 'admin.clash.nodes.bindings.loadFailed')
  } finally {
    if (request === candidatesRequest) candidatesLoading.value = false
  }
}

let searchTimer: ReturnType<typeof setTimeout> | undefined
function onSearchInput() {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    page.value = 1
    void loadCandidates()
  }, 300)
}

function goToPage(next: number) {
  page.value = Math.min(Math.max(next, 1), pages.value)
  void loadCandidates()
}

function reset() {
  bound.value = [...(props.node?.accounts ?? [])]
  exits.value = null
  candidates.value = []
  candidatesError.value = ''
  search.value = ''
  page.value = 1
  total.value = 0
  selected.value = new Set()
  pendingUnbind.value = null
  pauseOnUnbind.value = true
}

watch(
  () => props.show,
  (open) => {
    if (!open || !props.node) return
    reset()
    void loadExits()
    void loadManualProxies()
    if (!props.readonly) void loadCandidates()
  },
  { immediate: true }
)

function close() {
  if (working.value) return
  clearTimeout(searchTimer)
  emit('close')
}

async function bindSelected() {
  const node = props.node
  if (!node || working.value || selected.value.size === 0) return
  working.value = true
  const chosen = candidates.value.filter((account) => selected.value.has(account.id))
  let success = 0
  const errors: string[] = []
  try {
    // One at a time: the exit guard serializes bindings and enforces the limit.
    for (const account of chosen) {
      try {
        await adminAPI.accounts.update(account.id, { proxy_id: node.proxy_id })
        success++
        bound.value = [...bound.value, { id: account.id, name: account.name, platform: account.platform, is_shadow: false }]
      } catch (error) {
        errors.push(`${account.name}: ${describeError(error, 'admin.clash.nodes.bindings.bindFailed')}`)
      }
    }
    selected.value = new Set()
    if (errors.length === 0) {
      appStore.showSuccess(t('admin.clash.nodes.bindings.bound', { count: success }))
    } else if (success > 0) {
      appStore.showWarning(t('admin.clash.nodes.bindings.partial', { success, failed: errors.length, error: errors[0] }))
    } else {
      appStore.showError(errors[0])
    }
    if (success > 0) emit('changed')
    await Promise.all([loadExits(), loadCandidates()])
  } finally {
    working.value = false
  }
}

function askUnbind(account: ClashBoundAccount) {
  pendingUnbind.value = account
  pauseOnUnbind.value = true
}

async function confirmUnbind() {
  const account = pendingUnbind.value
  if (!account || working.value) return
  working.value = true
  try {
    // Pause first so the account never serves requests without its exit.
    if (pauseOnUnbind.value) await adminAPI.accounts.setSchedulable(account.id, false)
    await adminAPI.accounts.update(account.id, { proxy_id: 0 })
    bound.value = bound.value.filter((item) => item.id !== account.id)
    pendingUnbind.value = null
    appStore.showSuccess(
      pauseOnUnbind.value
        ? t('admin.clash.nodes.bindings.unboundPaused', { name: account.name })
        : t('admin.clash.nodes.bindings.unbound', { name: account.name })
    )
    emit('changed')
    await Promise.all([loadExits(), props.readonly ? Promise.resolve() : loadCandidates()])
  } catch (error) {
    appStore.showError(describeError(error, 'admin.clash.nodes.bindings.unbindFailed'))
  } finally {
    working.value = false
  }
}

// The panel hands over the reloaded node after a change; its binding list is
// authoritative (e.g. shadows that followed their parent's new proxy).
watch(
  () => props.node,
  (node, previous) => {
    if (props.show && node && previous && node.id === previous.id && node !== previous) {
      bound.value = [...node.accounts]
    }
  }
)

onUnmounted(() => clearTimeout(searchTimer))
</script>
