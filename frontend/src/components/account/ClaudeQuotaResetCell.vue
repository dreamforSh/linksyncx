<template>
  <div v-if="visible" class="space-y-1">
    <!--
      Same layout as OpenAIQuotaResetCell: the parent passes its own "query usage"
      affordance through #pre-actions so related buttons share one row. This cell
      only owns the Claude limit resets (free reset grants + the weekly session
      reset); the 5h / 7d / Fable bars stay in AccountUsageCell.
    -->
    <div class="flex flex-wrap items-center gap-1.5">
      <slot name="pre-actions" />

      <button
        type="button"
        data-testid="claude-reset-count"
        class="inline-flex items-center gap-0.5 rounded px-1.5 py-0.5 text-[10px] font-medium text-blue-600 transition-colors hover:bg-blue-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-blue-400 dark:hover:bg-blue-900/30"
        :disabled="loading || resetting"
        :title="countButtonTitle"
        @click="handleQuery()"
      >
        <svg
          class="h-2.5 w-2.5"
          :class="{ 'animate-spin': loading }"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
          />
        </svg>
        {{ t('admin.accounts.claudeQuotaReset.count') }}<span v-if="snapshot" class="tabular-nums"> {{ availableCount }}</span>
      </button>

      <button
        type="button"
        data-testid="claude-reset-use"
        class="inline-flex items-center gap-0.5 rounded px-1.5 py-0.5 text-[10px] font-medium text-orange-600 transition-colors hover:bg-orange-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-orange-400 dark:hover:bg-orange-900/30"
        :disabled="resetting || loading || !canReset"
        :title="resetButtonTitle"
        @click="openResetConfirm"
      >
        <svg
          class="h-2.5 w-2.5"
          :class="{ 'animate-spin': resetting }"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M20 12a8 8 0 11-2.343-5.657L20 8m0 0V4m0 4h-4"
          />
        </svg>
        {{ t('admin.accounts.claudeQuotaReset.reset') }}
      </button>
    </div>

    <div v-if="snapshot && hasDetails" class="flex flex-wrap items-center gap-1" data-testid="claude-reset-details">
      <span
        v-if="primaryGrant"
        class="inline-flex max-w-full items-center rounded bg-gray-100 px-1.5 py-0.5 text-[10px] leading-4 text-gray-600 tabular-nums dark:bg-dark-800 dark:text-gray-300"
        :title="grantTitle(primaryGrant)"
      >
        {{ grantChipText(primaryGrant) }}
      </span>
      <button
        v-if="hiddenGrantCount > 0"
        type="button"
        data-testid="claude-reset-grant-toggle"
        class="inline-flex items-center rounded-full bg-gray-100 px-1.5 py-0.5 text-[10px] font-medium leading-4 text-gray-600 transition-colors hover:bg-gray-200 dark:bg-dark-800 dark:text-gray-300 dark:hover:bg-dark-700"
        :aria-expanded="showGrantDetails"
        :title="t('admin.accounts.claudeQuotaReset.expandGrants', { count: hiddenGrantCount })"
        @click="showGrantDetails = !showGrantDetails"
      >
        +{{ hiddenGrantCount }}
      </button>
      <span
        v-if="nextGrantNeedsLimit"
        class="inline-flex items-center rounded bg-amber-50 px-1.5 py-0.5 text-[10px] font-medium leading-4 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300"
        :title="t('admin.accounts.claudeQuotaReset.resetTooltipRequiresLimit')"
      >
        {{ t('admin.accounts.claudeQuotaReset.requiresLimit') }}
      </span>
      <span
        v-if="juniperClaimable"
        data-testid="claude-reset-weekly"
        class="inline-flex items-center rounded bg-emerald-50 px-1.5 py-0.5 text-[10px] font-medium leading-4 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300"
        :title="t('admin.accounts.claudeQuotaReset.weeklyTitle')"
      >
        {{ t('admin.accounts.claudeQuotaReset.weeklyAvailable') }}
      </span>
      <span
        v-else-if="juniperNextAvailable"
        data-testid="claude-reset-weekly"
        class="inline-flex items-center rounded bg-gray-100 px-1.5 py-0.5 text-[10px] leading-4 text-gray-600 tabular-nums dark:bg-dark-800 dark:text-gray-300"
        :title="t('admin.accounts.claudeQuotaReset.weeklyTitle')"
      >
        {{ t('admin.accounts.claudeQuotaReset.weeklyNextAvailable', { time: formatTime(juniperNextAvailable, 'short') }) }}
      </span>
      <span
        v-if="ineligibleReason"
        class="text-[10px] text-gray-500 dark:text-gray-400"
        :title="t('admin.accounts.claudeQuotaReset.ineligibleTitle', { reason: ineligibleReason })"
      >
        {{ t('admin.accounts.claudeQuotaReset.ineligible') }}
      </span>
    </div>

    <div
      v-if="showGrantDetails && heldGrants.length > 1"
      data-testid="claude-reset-grant-details"
      class="inline-grid max-w-full gap-0.5 rounded border border-gray-200 bg-white px-1.5 py-1 text-[10px] leading-4 text-gray-600 shadow-sm dark:border-dark-700 dark:bg-dark-900 dark:text-gray-300"
    >
      <span
        v-for="(grant, index) in heldGrants"
        :key="`${grant.ends_at ?? ''}-${index}`"
        class="flex min-w-0 items-center gap-1 tabular-nums"
        :title="grantTitle(grant)"
      >
        <span class="h-1 w-1 shrink-0 rounded-full bg-gray-400 dark:bg-dark-500" />
        <span class="truncate">{{ grantChipText(grant) }}</span>
      </span>
    </div>

    <div v-if="error" class="text-[10px] text-red-600 dark:text-red-400" :title="error">
      {{ truncatedError }}
    </div>
    <div v-else-if="resetWarning" class="text-[10px] text-amber-600 dark:text-amber-400">
      {{ resetWarning }}
    </div>
    <div v-else-if="resetMessage" class="text-[10px] text-emerald-600 dark:text-emerald-400">
      {{ resetMessage }}
    </div>

    <ConfirmDialog
      :show="showResetConfirm"
      :title="t('admin.accounts.claudeQuotaReset.confirmTitle')"
      :message="t('admin.accounts.claudeQuotaReset.confirmMessage')"
      :confirm-text="t('admin.accounts.claudeQuotaReset.reset')"
      :cancel-text="t('common.cancel')"
      danger
      @confirm="confirmReset"
      @cancel="showResetConfirm = false"
    >
      <div class="space-y-2" data-testid="claude-reset-programs">
        <p v-if="claimablePrograms.length > 1" class="text-xs font-medium text-gray-700 dark:text-gray-300">
          {{ t('admin.accounts.claudeQuotaReset.confirmChoose') }}
        </p>
        <label
          v-for="program in claimablePrograms"
          :key="program"
          class="flex cursor-pointer items-start gap-2 rounded-md border border-gray-200 px-2.5 py-2 text-xs dark:border-dark-600"
          :class="selectedProgram === program ? 'border-orange-300 bg-orange-50/60 dark:border-orange-500/60 dark:bg-orange-900/20' : ''"
        >
          <input
            v-model="selectedProgram"
            type="radio"
            class="mt-0.5"
            name="claude-reset-program"
            :value="program"
            :data-testid="`claude-reset-program-${program}`"
          />
          <span class="space-y-0.5">
            <span class="block font-medium text-gray-900 dark:text-gray-100">
              {{ t(`admin.accounts.claudeQuotaReset.programs.${program}`) }}
            </span>
            <span class="block text-gray-500 dark:text-gray-400">{{ programHint(program) }}</span>
          </span>
        </label>
      </div>
    </ConfirmDialog>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Account, AccountUsageInfo, ClaudeResetGrant, ClaudeResetSnapshot } from '@/types'
import {
  refreshClaudeQuota,
  resetClaudeQuota,
  type ClaudeQuotaResetResult,
  type ClaudeResetProgram
} from '@/api/admin/accounts'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import { extractApiErrorCode } from '@/utils/apiError'

const props = defineProps<{
  account: Account
}>()

const emit = defineEmits<{
  'account-updated': [account: Account]
  'usage-updated': [usage: AccountUsageInfo]
}>()

const { t, te } = useI18n()

// Only Claude OAuth accounts carry the profile scope that the reset endpoints need.
const visible = computed(() => props.account.platform === 'anthropic' && props.account.type === 'oauth')

const loading = ref(false)
const resetting = ref(false)
const error = ref<string | null>(null)
const resetMessage = ref<string | null>(null)
const resetWarning = ref<string | null>(null)
const showResetConfirm = ref(false)
const showGrantDetails = ref(false)
const selectedProgram = ref<ClaudeResetProgram | null>(null)

const readSnapshot = (account: Account): ClaudeResetSnapshot | null => {
  const raw = account.extra?.claude_reset_snapshot
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null
  if (typeof raw.fetched_at !== 'string') return null
  return raw
}
const snapshot = ref<ClaudeResetSnapshot | null>(readSnapshot(props.account))

const timeOf = (value?: string | null): number => {
  if (!value) return Number.POSITIVE_INFINITY
  const time = new Date(value).getTime()
  return Number.isNaN(time) ? Number.POSITIVE_INFINITY : time
}
// Unparsable end times are kept (rendered verbatim) so the count is never understated.
const isExpired = (value?: string | null) => timeOf(value) <= Date.now()

const cedar = computed(() => snapshot.value?.cedar_ember ?? null)
const juniper = computed(() => snapshot.value?.juniper_tide ?? null)

// Grants the account still holds: the snapshot has no freshness signal of its
// own, so expired ones are dropped on read instead of being offered for use.
const heldGrants = computed<ClaudeResetGrant[]>(() => {
  if (!cedar.value?.eligible) return []
  return (cedar.value.grants ?? [])
    .filter((grant) => grant.resets_left > 0 && !isExpired(grant.ends_at))
    .slice()
    .sort((a, b) => timeOf(a.ends_at) - timeOf(b.ends_at))
})
const primaryGrant = computed(() => heldGrants.value[0] ?? null)
const hiddenGrantCount = computed(() => Math.max(heldGrants.value.length - 1, 0))

const nextGrant = computed(() => heldGrants.value.find((grant) => grant.next) ?? null)
const coversExhaustedLimit = (grant: ClaudeResetGrant) =>
  grant.clears.some((limit) => (cedar.value?.exhausted ?? []).includes(limit))
const nextGrantNeedsLimit = computed(
  () => !!nextGrant.value && nextGrant.value.use_requires_limit && !coversExhaustedLimit(nextGrant.value)
)
const cedarClaimable = computed(() => {
  const grant = nextGrant.value
  return !!grant && !grant.paused && grant.usable_now && !nextGrantNeedsLimit.value
})
const juniperClaimable = computed(
  () => !!juniper.value?.eligible && !!juniper.value.available && juniper.value.arm === 'reset'
)
const juniperNextAvailable = computed(() => {
  const next = juniper.value?.next_available_at
  return juniper.value?.eligible && next && !isExpired(next) ? next : ''
})
const ineligibleReason = computed(() =>
  cedar.value && !cedar.value.eligible ? cedar.value.ineligible_reason || '-' : ''
)
const hasDetails = computed(
  () => !!primaryGrant.value || nextGrantNeedsLimit.value || juniperClaimable.value || !!juniperNextAvailable.value || !!ineligibleReason.value
)

const availableCount = computed(
  () => heldGrants.value.reduce((sum, grant) => sum + grant.resets_left, 0) + (juniperClaimable.value ? 1 : 0)
)

// The weekly session reset is listed first: an unused one lapses at the end of the week.
const claimablePrograms = computed<ClaudeResetProgram[]>(() => {
  const programs: ClaudeResetProgram[] = []
  if (juniperClaimable.value) programs.push('juniper_tide')
  if (cedarClaimable.value) programs.push('cedar_ember')
  return programs
})
const canReset = computed(() => claimablePrograms.value.length > 0)

const countButtonTitle = computed(() => {
  if (!snapshot.value) return t('admin.accounts.claudeQuotaReset.countTooltipLoad')
  return `${t('admin.accounts.claudeQuotaReset.countTooltipRefresh')}\n${t('admin.accounts.claudeQuotaReset.fetchedAt', {
    time: formatTime(snapshot.value.fetched_at, 'full')
  })}`
})

const resetButtonTitle = computed(() => {
  if (!snapshot.value) return t('admin.accounts.claudeQuotaReset.resetTooltipNeedQuery')
  if (canReset.value) return t('admin.accounts.claudeQuotaReset.resetTooltipReady')
  if (nextGrantNeedsLimit.value) return t('admin.accounts.claudeQuotaReset.resetTooltipRequiresLimit')
  return t('admin.accounts.claudeQuotaReset.resetTooltipNoResets')
})

const truncatedError = computed(() => {
  if (!error.value) return ''
  return error.value.length > 80 ? `${error.value.slice(0, 80)}…` : error.value
})

const limitLabel = (limit: string) => {
  const key = `admin.accounts.claudeQuotaReset.limits.${limit}`
  return te(key) ? t(key) : limit
}
const limitsText = (limits?: string[] | null) => {
  const labels = (limits ?? []).map(limitLabel)
  return labels.length > 0 ? labels.join(' / ') : '-'
}

function formatTime(value: string, style: 'short' | 'full'): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  const options: Intl.DateTimeFormatOptions = { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }
  if (style === 'full') options.year = 'numeric'
  return new Intl.DateTimeFormat(undefined, options).format(date)
}

const grantChipText = (grant: ClaudeResetGrant) => {
  const count = grant.resets_left > 1 ? ` ×${grant.resets_left}` : ''
  if (!grant.ends_at) return `${t('admin.accounts.claudeQuotaReset.grantNoExpiry')}${count}`
  return `${t('admin.accounts.claudeQuotaReset.expiresAt', { time: formatTime(grant.ends_at, 'short') })}${count}`
}

const grantTitle = (grant: ClaudeResetGrant) => {
  const lines: string[] = []
  if (grant.label) lines.push(grant.label)
  lines.push(t('admin.accounts.claudeQuotaReset.grantLeft', { count: grant.resets_left }))
  lines.push(t('admin.accounts.claudeQuotaReset.grantClears', { limits: limitsText(grant.clears) }))
  if (grant.ends_at) lines.push(t('admin.accounts.claudeQuotaReset.expiresAtFull', { time: formatTime(grant.ends_at, 'full') }))
  if (grant.paused) lines.push(t('admin.accounts.claudeQuotaReset.paused'))
  return lines.join('\n')
}

const programHint = (program: ClaudeResetProgram) => {
  if (program === 'juniper_tide') return t('admin.accounts.claudeQuotaReset.programHints.juniper_tide')
  return t('admin.accounts.claudeQuotaReset.programHints.cedar_ember', { limits: limitsText(nextGrant.value?.clears) })
}

const describeError = (e: unknown): string => {
  const code = extractApiErrorCode(e)
  if (code) {
    const key = `admin.accounts.claudeQuotaReset.errors.${code}`
    if (te(key)) return t(key)
  }
  const err = e as { message?: string; reason?: string; response?: { data?: { message?: string } } }
  return err?.message || err?.reason || err?.response?.data?.message || t('common.error')
}

const clearFeedback = () => {
  error.value = null
  resetMessage.value = null
  resetWarning.value = null
}

const handleQuery = async () => {
  if (loading.value || resetting.value) return
  const accountID = props.account.id
  loading.value = true
  clearFeedback()
  showGrantDetails.value = false
  try {
    const result = await refreshClaudeQuota(accountID)
    if (props.account.id !== accountID) return
    snapshot.value = result.snapshot ?? null
    if (!result.cache_persisted) {
      resetWarning.value = t('admin.accounts.claudeQuotaReset.refreshCachePersistFailed')
    }
    if (result.usage) emit('usage-updated', result.usage)
    if (result.account) emit('account-updated', result.account)
  } catch (e) {
    if (props.account.id !== accountID) return
    error.value = describeError(e)
  } finally {
    if (props.account.id === accountID) loading.value = false
  }
}

const openResetConfirm = () => {
  if (resetting.value || loading.value) return
  if (!canReset.value) {
    error.value = nextGrantNeedsLimit.value
      ? t('admin.accounts.claudeQuotaReset.resetTooltipRequiresLimit')
      : t('admin.accounts.claudeQuotaReset.resetTooltipNoResets')
    return
  }
  selectedProgram.value = claimablePrograms.value[0]
  showResetConfirm.value = true
}

const resultMessage = (result: ClaudeQuotaResetResult) => {
  switch (result.result) {
    case 'reset':
      return result.cleared && result.cleared.length > 0
        ? t('admin.accounts.claudeQuotaReset.resetSuccess', { limits: limitsText(result.cleared) })
        : t('admin.accounts.claudeQuotaReset.resetSuccessGeneric')
    case 'already_used':
      return t('admin.accounts.claudeQuotaReset.resetAlreadyUsed')
    default:
      return ''
  }
}

const notAppliedMessage = (result: ClaudeQuotaResetResult) => {
  switch (result.result) {
    case 'not_limited':
      return t('admin.accounts.claudeQuotaReset.resetNotLimited')
    case 'cooldown':
      return t('admin.accounts.claudeQuotaReset.resetCooldown')
    default:
      return t('admin.accounts.claudeQuotaReset.resetNotApplied', { result: result.reason || result.result })
  }
}

const confirmReset = async () => {
  showResetConfirm.value = false
  const program = selectedProgram.value
  if (resetting.value || !program) return
  resetting.value = true
  const accountID = props.account.id
  clearFeedback()
  try {
    const result = await resetClaudeQuota(accountID, program)
    if (props.account.id !== accountID) return
    showGrantDetails.value = false
    const consumed = result.result === 'reset' || result.result === 'already_used'
    if (result.snapshot) {
      snapshot.value = result.snapshot
    } else if (consumed) {
      // A reset was spent but the new state could not be read back: whatever we
      // hold is one generation stale, so report it as unknown instead of letting
      // a second claim start from stale data.
      snapshot.value = null
    }
    if (result.usage) emit('usage-updated', result.usage)
    if (result.account) emit('account-updated', result.account)

    if (!consumed) {
      resetWarning.value = notAppliedMessage(result)
    } else if (result.warning_code === 'reset_credit_cache_refresh_failed') {
      resetWarning.value = t('admin.accounts.claudeQuotaReset.resetCacheRefreshFailed')
    } else if (result.warning_code === 'account_state_recovery_failed') {
      resetWarning.value = t('admin.accounts.claudeQuotaReset.resetAccountRecoveryFailed')
    } else if (result.warning_code === 'account_state_refresh_failed') {
      resetWarning.value = t('admin.accounts.claudeQuotaReset.resetAccountRefreshFailed')
    } else {
      resetMessage.value = resultMessage(result)
    }
  } catch (e) {
    if (props.account.id !== accountID) return
    error.value = describeError(e)
  } finally {
    if (props.account.id === accountID) resetting.value = false
  }
}

watch(
  () => props.account.id,
  () => {
    // Account rows are reused across paginated lists; reset local state.
    snapshot.value = readSnapshot(props.account)
    clearFeedback()
    loading.value = false
    resetting.value = false
    showResetConfirm.value = false
    showGrantDetails.value = false
    selectedProgram.value = null
  }
)

// A refreshed account row (e.g. after another tab queried it) carries a newer snapshot.
watch(
  () => props.account.extra?.claude_reset_snapshot,
  () => {
    const next = readSnapshot(props.account)
    if (next && (!snapshot.value || timeOf(next.fetched_at) > timeOf(snapshot.value.fetched_at))) {
      snapshot.value = next
    }
  }
)

watch(hiddenGrantCount, (count) => {
  if (count <= 0) showGrantDetails.value = false
})
</script>
