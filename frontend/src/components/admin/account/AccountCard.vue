<template>
  <article
    class="flex min-w-0 flex-col rounded-xl border bg-white shadow-sm transition-shadow hover:shadow-md dark:bg-dark-900"
    :class="selected
      ? 'border-primary-300 ring-2 ring-primary-400/40 dark:border-primary-500/60'
      : platformBorderClass(account.platform)"
    data-testid="account-card"
    :data-account-id="account.id"
  >
    <!-- 不用 overflow-hidden 裁圆角：状态指示等单元格的悬浮提示需要溢出卡片 -->
    <div class="h-1 flex-shrink-0 rounded-t-[11px]" :class="platformAccentBarClass(account.platform)" aria-hidden="true"></div>

    <header class="flex items-start gap-2.5 px-4 pt-3">
      <input
        type="checkbox"
        class="mt-0.5 h-4 w-4 flex-shrink-0 cursor-pointer rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-500 dark:bg-dark-800"
        :checked="selected"
        :aria-label="t('admin.accounts.card.select', { name: account.name })"
        data-testid="account-card-select"
        @change="emit('toggle-select')"
      />
      <div class="min-w-0 flex-1">
        <div class="flex min-w-0 items-center gap-1.5">
          <a
            v-if="homepageUrl"
            :href="homepageUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="min-w-0 truncate border-b border-dotted border-gray-300 text-sm font-semibold text-gray-900 dark:border-dark-600 dark:text-white"
            :title="homepageUrl"
            data-testid="account-card-name"
          >{{ account.name }}</a>
          <h3
            v-else
            class="min-w-0 truncate text-sm font-semibold text-gray-900 dark:text-white"
            :title="account.name"
            data-testid="account-card-name"
          >{{ account.name }}</h3>
          <span class="flex-shrink-0 font-mono text-xs text-gray-400 dark:text-dark-500" data-testid="account-card-id">#{{ account.id }}</span>
          <span
            v-if="account.notes"
            class="inline-flex flex-shrink-0 cursor-help text-gray-400 transition-colors hover:text-gray-600 dark:text-dark-400 dark:hover:text-gray-200"
            role="img"
            :title="account.notes"
            :aria-label="`${t('admin.accounts.columns.notes')}: ${account.notes}`"
            data-testid="account-card-notes"
          >
            <Icon name="chatBubble" size="sm" />
          </span>
        </div>
        <p
          v-if="displayEmail"
          class="mt-0.5 truncate text-xs text-gray-500 dark:text-dark-400"
          :title="emailTitle"
          data-testid="account-card-email"
        >{{ displayEmail }}</p>
      </div>
      <div v-if="show('schedulable')" class="flex flex-shrink-0 items-center gap-1.5">
        <span class="text-[11px] text-gray-500 dark:text-dark-400">{{ t('admin.accounts.columns.schedulable') }}</span>
        <button
          type="button"
          role="switch"
          :aria-checked="account.schedulable"
          :aria-label="t('admin.accounts.schedulable')"
          :disabled="togglingSchedulable"
          class="relative inline-flex h-5 w-9 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50 dark:focus:ring-offset-dark-900"
          :class="account.schedulable ? 'bg-primary-500 hover:bg-primary-600' : 'bg-gray-200 hover:bg-gray-300 dark:bg-dark-600 dark:hover:bg-dark-500'"
          :title="account.schedulable ? t('admin.accounts.schedulableEnabled') : t('admin.accounts.schedulableDisabled')"
          data-testid="account-card-schedulable"
          @click="emit('toggle-schedulable')"
        >
          <span
            class="pointer-events-none inline-block h-4 w-4 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out"
            :class="account.schedulable ? 'translate-x-4' : 'translate-x-0'"
          />
        </button>
      </div>
    </header>

    <div v-if="show('platform_type')" class="mt-2 flex flex-wrap items-center gap-1 px-4" data-testid="account-card-platform">
      <PlatformTypeBadge
        :platform="account.platform"
        :type="account.type"
        :auth-mode="authMode"
        :plan-type="planType"
        :privacy-mode="privacyMode"
        :subscription-expires-at="subscriptionExpiresAt"
      />
      <span v-if="tierLabel" :class="['inline-block rounded px-1.5 py-0.5 text-[10px] font-medium', tierClass]">
        {{ tierLabel }}
      </span>
      <span
        v-if="compactMeta"
        :class="['inline-flex items-center gap-1.5 pl-0.5 text-[11px] font-medium leading-4', compactMeta.className]"
        :title="compactTitle"
      >
        <span :class="['h-1.5 w-1.5 rounded-full', compactMeta.dotClass]" />
        <span>{{ compactMeta.label }}</span>
      </span>
    </div>

    <div class="flex flex-1 flex-col gap-3 px-4 pb-3 pt-3">
      <div v-if="show('status') || show('capacity')" class="grid grid-cols-2 gap-3">
        <section v-if="show('status')" class="min-w-0" :class="show('capacity') ? '' : 'col-span-2'" data-testid="account-card-status">
          <h4 class="account-card-label">{{ t('admin.accounts.columns.status') }}</h4>
          <AccountStatusIndicator class="flex-wrap" :account="account" @show-temp-unsched="emit('show-temp-unsched', $event)" />
        </section>
        <section v-if="show('capacity')" class="min-w-0" :class="show('status') ? '' : 'col-span-2'" data-testid="account-card-capacity">
          <h4 class="account-card-label">{{ t('admin.accounts.columns.capacity') }}</h4>
          <AccountCapacityCell :account="account" />
        </section>
      </div>

      <!-- 用量窗口与表格同参：批量用量、今日统计与懒加载逻辑不区分视图 -->
      <section v-if="show('usage')" class="min-w-0" data-testid="account-card-usage">
        <h4 class="account-card-label">{{ t('admin.accounts.columns.usageWindows') }}</h4>
        <AccountUsageCell
          :account="account"
          :today-stats="todayStats"
          :today-stats-loading="todayStatsLoading"
          :manual-refresh-token="manualRefreshToken"
          :batched-usage="batchedUsage"
          :batched-usage-error="batchedUsageError"
          :batched-usage-loading="batchedUsageLoading"
          :request-batched-usage="requestBatchedUsage"
          @account-updated="emit('account-updated', $event)"
          @usage-loaded="emit('usage-loaded', $event)"
        />
      </section>

      <section v-if="show('today_stats')" class="min-w-0" data-testid="account-card-today-stats">
        <h4 class="account-card-label">{{ t('admin.accounts.columns.todayStats') }}</h4>
        <AccountTodayStatsCell :stats="todayStats" :loading="todayStatsLoading" :error="todayStatsError" />
      </section>

      <section v-if="show('custom_usage') && customUsageEligible" class="min-w-0" data-testid="account-card-custom-usage">
        <h4 class="account-card-label">{{ t('admin.accounts.customUsage.column') }}</h4>
        <!-- 换页/筛选后重新挂载，让单元格重新上报可见性（同表格的 key 规则） -->
        <CustomUsageCell
          :key="customUsageKey"
          :account="account"
          :state="customUsageState"
          @refresh="emit('custom-usage-refresh')"
          @configure="emit('custom-usage-configure')"
          @visibility="emit('custom-usage-visibility', $event)"
        />
      </section>

      <section v-if="show('groups')" class="min-w-0" data-testid="account-card-groups">
        <h4 class="account-card-label">{{ t('admin.accounts.columns.groups') }}</h4>
        <AccountGroupsCell :groups="groups" :max-display="4" />
      </section>

      <section v-if="show('proxy')" class="min-w-0" data-testid="account-card-proxy">
        <h4 class="account-card-label">{{ t('admin.accounts.columns.proxy') }}</h4>
        <div v-if="account.proxy?.source === 'clash'" class="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5" data-testid="account-card-clash-proxy">
          <ClashTag />
          <span class="min-w-0 truncate text-sm text-gray-700 dark:text-gray-300" :title="account.proxy.name">
            {{ clashExit?.node_name || account.proxy.name }}
          </span>
          <span class="flex items-center gap-1 text-xs text-gray-500 dark:text-gray-400">
            <CountryFlag v-if="clashExit?.exit_ip" :code="clashExit.exit_country_code" :label="clashExit.exit_country" />
            <span class="font-mono">{{ clashExit?.exit_ip || t('admin.clash.selector.exitUnprobed') }}</span>
          </span>
        </div>
        <div v-else-if="account.proxy" class="flex min-w-0 items-center gap-1.5">
          <span class="min-w-0 truncate text-sm text-gray-700 dark:text-gray-300" :title="account.proxy.name">{{ account.proxy.name }}</span>
          <span v-if="account.proxy.country_code" class="flex-shrink-0 text-xs text-gray-500 dark:text-gray-400">
            ({{ account.proxy.country_code }})
          </span>
        </div>
        <span v-else class="text-sm text-gray-400 dark:text-dark-500">-</span>
        <div v-if="account.proxy?.expires_at" class="mt-1 flex flex-wrap items-center gap-2 text-xs">
          <span class="text-gray-600 dark:text-gray-300">{{ formatDateTime(account.proxy.expires_at) }}</span>
          <span :class="proxyExpiryBadgeClass(account.proxy.expires_at, account.proxy.status)">{{ proxyExpiryText }}</span>
        </div>
        <div v-if="account.proxy_fallback_origin_id" class="mt-1 flex items-center gap-1">
          <span
            class="inline-flex items-center rounded px-1.5 py-0.5 text-xs font-medium bg-yellow-100 text-yellow-800 dark:bg-yellow-900 dark:text-yellow-200"
            :title="t('admin.accounts.fallbackActiveTip', { origin: account.proxy_fallback_origin_name })"
          >
            {{ t('admin.accounts.fallbackActive') }}
          </span>
          <button
            type="button"
            class="rounded border border-gray-300 px-1.5 py-0.5 text-xs text-gray-600 hover:bg-gray-100 dark:border-dark-600 dark:text-gray-300 dark:hover:bg-dark-700"
            @click="emit('revert-fallback')"
          >
            {{ t('admin.accounts.revertProxy') }}
          </button>
        </div>
      </section>

      <dl
        v-if="hasMeta"
        class="grid grid-cols-2 gap-x-3 gap-y-2 border-t border-gray-100 pt-3 text-xs dark:border-dark-700/70"
      >
        <div v-if="show('priority')" class="min-w-0" data-testid="account-card-priority">
          <dt class="account-card-label">{{ t('admin.accounts.columns.priority') }}</dt>
          <dd class="text-gray-700 dark:text-gray-300">{{ account.priority }}</dd>
        </div>
        <div v-if="show('rate_multiplier')" class="min-w-0">
          <dt class="account-card-label">{{ t('admin.accounts.columns.billingRateMultiplier') }}</dt>
          <dd class="inline-flex items-center gap-1 font-mono text-gray-700 dark:text-gray-300">
            <span>{{ formatMultiplier(account.rate_multiplier ?? 1) }}x</span>
            <span
              v-if="account.extra?.upstream_billing_rate_sync_enabled === true"
              class="inline-flex cursor-help text-emerald-600 dark:text-emerald-400"
              :aria-label="t('admin.accounts.upstreamBilling.syncedRateTooltip')"
              :title="t('admin.accounts.upstreamBilling.syncedRateTooltip')"
            >
              <Icon name="sync" size="xs" />
            </span>
          </dd>
        </div>
        <div v-if="showUpstreamBilling" class="min-w-0">
          <dt class="account-card-label">{{ t('admin.accounts.columns.upstreamBillingRate') }}</dt>
          <dd>
            <UpstreamBillingRateCell
              :account="account"
              :global-probe-enabled="upstreamBillingGlobalProbeEnabled"
              :now="upstreamBillingNow"
              :probing="upstreamBillingProbing"
              @probe="emit('probe-upstream-billing')"
            />
          </dd>
        </div>
        <div v-if="show('last_used_at')" class="min-w-0" data-testid="account-card-last-used">
          <dt class="account-card-label">{{ t('admin.accounts.columns.lastUsed') }}</dt>
          <dd class="text-gray-700 dark:text-gray-300">{{ formatRelativeTime(account.last_used_at) }}</dd>
        </div>
        <div v-if="show('created_at')" class="min-w-0">
          <dt class="account-card-label">{{ t('admin.accounts.columns.createdAt') }}</dt>
          <dd class="text-gray-700 dark:text-gray-300">{{ formatDateTime(account.created_at) }}</dd>
        </div>
        <div v-if="show('expires_at')" class="min-w-0" data-testid="account-card-expires">
          <dt class="account-card-label">{{ t('admin.accounts.columns.expiresAt') }}</dt>
          <dd class="flex flex-wrap items-center gap-1 text-gray-700 dark:text-gray-300">
            <span>{{ expiresAtText }}</span>
            <span
              v-if="expired"
              class="inline-flex items-center rounded-md bg-amber-100 px-1.5 py-0.5 font-medium text-amber-700 dark:bg-amber-900/30 dark:text-amber-300"
            >
              {{ t('admin.accounts.expired') }}
            </span>
            <span
              v-if="account.auto_pause_on_expired && account.expires_at"
              class="inline-flex items-center rounded-md bg-emerald-100 px-1.5 py-0.5 font-medium text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300"
            >
              {{ t('admin.accounts.autoPauseOnExpired') }}
            </span>
          </dd>
        </div>
      </dl>
    </div>

    <footer class="flex items-center justify-end gap-1 border-t border-gray-100 px-3 py-2 dark:border-dark-700/70">
      <button type="button" class="row-action" data-testid="account-card-edit" @click="emit('edit')">
        <Icon name="edit" size="sm" />
        {{ t('common.edit') }}
      </button>
      <button
        type="button"
        class="row-action hover:!bg-red-50 hover:!text-red-600 dark:hover:!bg-red-900/20 dark:hover:!text-red-400"
        data-testid="account-card-delete"
        @click="emit('delete')"
      >
        <Icon name="trash" size="sm" />
        {{ t('common.delete') }}
      </button>
      <!-- 行操作菜单以按钮的 getBoundingClientRect 定位，原生点击事件需原样交给父组件 -->
      <button type="button" class="row-action" aria-haspopup="menu" data-testid="account-card-more" @click="emit('more', $event)">
        <Icon name="more" size="sm" />
        {{ t('common.more') }}
      </button>
    </footer>
  </article>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import AccountCapacityCell from '@/components/account/AccountCapacityCell.vue'
import AccountGroupsCell from '@/components/account/AccountGroupsCell.vue'
import AccountStatusIndicator from '@/components/account/AccountStatusIndicator.vue'
import AccountTodayStatsCell from '@/components/account/AccountTodayStatsCell.vue'
import AccountUsageCell from '@/components/account/AccountUsageCell.vue'
import CustomUsageCell from '@/components/account/CustomUsageCell.vue'
import UpstreamBillingRateCell from '@/components/account/UpstreamBillingRateCell.vue'
import ClashTag from '@/components/common/ClashTag.vue'
import CountryFlag from '@/components/common/CountryFlag.vue'
import PlatformTypeBadge from '@/components/common/PlatformTypeBadge.vue'
import Icon from '@/components/icons/Icon.vue'
import type { CustomUsageRowState } from '@/composables/useCustomUsage'
import type { Account, AccountListItem, AccountUsageInfo, AdminGroup, ClashExitOption, WindowStats } from '@/types'
import { supportsCustomUsage } from '@/utils/customUsage'
import { formatDateTime, formatDateTimeToMinute, formatRelativeTime } from '@/utils/format'
import { formatMultiplier } from '@/utils/formatters'
import { platformAccentBarClass, platformBorderClass } from '@/utils/platformColors'
import { proxyExpiryBadgeClass, proxyExpiryLabelKey } from '@/utils/proxyExpiry'

const props = withDefaults(
  defineProps<{
    account: AccountListItem
    selected?: boolean
    /** 表格当前可见的列；可选区块跟随同一份列显示设置 */
    visibleColumns: string[]
    /** 以下由账号页按表格单元格的同一套逻辑算好传入 */
    displayEmail?: string
    homepageUrl?: string
    authMode?: string
    planType?: string
    privacyMode?: string
    tierLabel?: string | null
    tierClass?: string
    compactMeta?: { label: string; className: string; dotClass: string } | null
    compactTitle?: string
    groups?: AdminGroup[]
    clashExit?: ClashExitOption | null
    todayStats?: WindowStats | null
    todayStatsLoading?: boolean
    todayStatsError?: string | null
    manualRefreshToken?: number
    batchedUsage?: AccountUsageInfo | null
    batchedUsageError?: string | null
    batchedUsageLoading?: boolean
    requestBatchedUsage?: ((account: Account, options?: { force?: boolean }) => void) | null
    togglingSchedulable?: boolean
    customUsageState?: CustomUsageRowState
    customUsageKey?: string
    upstreamBillingNow?: number
    upstreamBillingProbing?: boolean
    upstreamBillingGlobalProbeEnabled?: boolean
  }>(),
  {
    selected: false,
    displayEmail: '',
    homepageUrl: '',
    tierLabel: null,
    tierClass: '',
    compactMeta: null,
    compactTitle: '',
    groups: () => [],
    clashExit: null,
    todayStats: null,
    todayStatsLoading: false,
    todayStatsError: null,
    manualRefreshToken: 0,
    batchedUsage: null,
    batchedUsageError: null,
    batchedUsageLoading: false,
    requestBatchedUsage: null,
    togglingSchedulable: false,
    upstreamBillingNow: () => Date.now(),
    upstreamBillingProbing: false,
    upstreamBillingGlobalProbeEnabled: true
  }
)

const emit = defineEmits<{
  (e: 'toggle-select'): void
  (e: 'toggle-schedulable'): void
  (e: 'show-temp-unsched', account: Account): void
  (e: 'account-updated', account: Account): void
  (e: 'usage-loaded', usage: AccountUsageInfo): void
  (e: 'custom-usage-refresh'): void
  (e: 'custom-usage-configure'): void
  (e: 'custom-usage-visibility', visible: boolean): void
  (e: 'probe-upstream-billing'): void
  (e: 'revert-fallback'): void
  (e: 'edit'): void
  (e: 'delete'): void
  (e: 'more', event: MouseEvent): void
}>()

const { t } = useI18n()

const visibleColumnSet = computed(() => new Set(props.visibleColumns))
const show = (key: string) => visibleColumnSet.value.has(key)

const emailTitle = computed(() =>
  props.displayEmail + (props.account.parent_chatgpt_account_id ? ` · ${props.account.parent_chatgpt_account_id}` : '')
)

// 与表格平台列一致：账号自身的订阅到期时间优先，影子账号回退母账号
const subscriptionExpiresAt = computed(() => {
  const own = props.account.credentials?.subscription_expires_at
  return (typeof own === 'string' && own) || props.account.parent_subscription_expires_at || undefined
})

const customUsageEligible = computed(() => supportsCustomUsage(props.account))
// 上游声明倍率只对 API Key 账号有意义，其余账号不占卡片空间
const showUpstreamBilling = computed(() => show('upstream_billing_rate') && props.account.type === 'apikey')
const hasMeta = computed(() =>
  showUpstreamBilling.value || ['priority', 'rate_multiplier', 'last_used_at', 'created_at', 'expires_at'].some(show)
)

const proxyExpiryText = computed(() => {
  const proxy = props.account.proxy
  if (!proxy?.expires_at) return ''
  const { key, params } = proxyExpiryLabelKey(proxy.expires_at, proxy.status)
  return params ? t(key, params) : t(key)
})

// expires_at 为秒级时间戳，格式与表格到期列一致
const expiresAtText = computed(() =>
  props.account.expires_at ? formatDateTimeToMinute(new Date(props.account.expires_at * 1000), 'sv-SE') : '-'
)
const expired = computed(() => !!props.account.expires_at && props.account.expires_at * 1000 <= Date.now())
</script>

<style scoped>
.account-card-label {
  @apply mb-1 text-[11px] font-medium text-gray-500 dark:text-dark-400;
}
</style>
