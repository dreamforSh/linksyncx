<template>
  <div class="space-y-1" data-testid="codebuddy-credits">
    <template v-if="snapshot">
      <div class="flex flex-wrap items-center gap-1" :title="detailTitle">
        <span class="text-[11px] font-medium" :class="balanceClass">
          <template v-if="snapshot.unlimited">{{ t('admin.accounts.codebuddyOAuth.credits.unlimited') }}</template>
          <template v-else>
            {{ t('admin.accounts.codebuddyOAuth.credits.remaining') }} {{ formatCredits(snapshot.credits) }}
            <span v-if="totalCredits > 0" class="text-gray-400">/ {{ formatCredits(totalCredits) }}</span>
          </template>
        </span>
        <span v-if="snapshot.enterprise" class="rounded bg-violet-100 px-1 text-[9px] text-violet-700 dark:bg-violet-900/40 dark:text-violet-300">
          {{ t('admin.accounts.codebuddyOAuth.credits.enterprise') }}
        </span>
        <span v-if="snapshot.exhausted" class="rounded bg-red-100 px-1 text-[9px] text-red-700 dark:bg-red-900/40 dark:text-red-300">
          {{ t('admin.accounts.codebuddyOAuth.credits.exhausted') }}
        </span>
        <span v-else-if="snapshot.estimated" class="rounded bg-amber-100 px-1 text-[9px] text-amber-700 dark:bg-amber-900/40 dark:text-amber-300">
          {{ t('admin.accounts.codebuddyOAuth.credits.estimated') }}
        </span>
        <span v-if="stale" class="rounded bg-gray-100 px-1 text-[9px] text-gray-500 dark:bg-dark-700 dark:text-gray-400">
          {{ t('admin.accounts.codebuddyOAuth.credits.stale') }}
        </span>
      </div>
      <div v-if="totalCredits > 0" class="h-1 w-28 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-700">
        <div class="h-full rounded-full transition-all" :class="barClass" :style="{ width: `${usedPercent}%` }" />
      </div>
      <div v-if="expiryLabel" class="text-[10px] text-gray-500 dark:text-gray-400">{{ expiryLabel }}</div>
    </template>
    <div v-else-if="!loading" class="text-[10px] text-gray-400">{{ t('admin.accounts.codebuddyOAuth.credits.notProbed') }}</div>

    <div v-if="todayUsage" class="text-[10px] text-gray-500 dark:text-gray-400">
      {{ todayUsage.official ? t('admin.accounts.codebuddyOAuth.credits.today') : t('admin.accounts.codebuddyOAuth.credits.gateway') }}:
      {{ formatCredits(todayUsage.credits) }} · {{ todayUsage.requests }} {{ t('admin.accounts.codebuddyOAuth.credits.requests') }}
    </div>

    <button
      type="button"
      class="inline-flex items-center gap-0.5 rounded px-1.5 py-0.5 text-[10px] font-medium text-violet-700 transition-colors hover:bg-violet-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-violet-300 dark:hover:bg-violet-900/30"
      :disabled="loading"
      data-testid="codebuddy-refresh-credits"
      @click="refresh"
    >
      <svg class="h-2.5 w-2.5" :class="{ 'animate-spin': loading }" fill="none" stroke="currentColor" viewBox="0 0 24 24">
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
      </svg>
      {{ t('admin.accounts.codebuddyOAuth.credits.refresh') }}
    </button>
    <div v-if="error" class="max-w-[200px] truncate text-[10px] text-red-600 dark:text-red-400" :title="error">{{ error }}</div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { CodeBuddyCreditsSnapshot, CodeBuddyCreditsUsage, CodeBuddyRequestUsage } from '@/api/admin/codebuddy'
import type { Account } from '@/types'

/** Matches the backend probe TTL so both sides agree on "stale". */
const SNAPSHOT_TTL_SECONDS = 10 * 60

const props = defineProps<{ account: Account }>()
const { t } = useI18n()

const loading = ref(false)
const error = ref('')
const snapshot = ref<CodeBuddyCreditsSnapshot | null>(null)
const usage = ref<CodeBuddyCreditsUsage | null>(null)
const official = ref<CodeBuddyRequestUsage | null>(null)

// The account row carries the last persisted probe and tally, so the cell
// shows a balance without a billing round trip on every page load.
function loadFromExtra() {
  const extra = (props.account.extra || {}) as Record<string, unknown>
  const pick = <T,>(key: string) => {
    const value = extra[key]
    return value && typeof value === 'object' ? (value as T) : null
  }
  snapshot.value = pick<CodeBuddyCreditsSnapshot>('codebuddy_credits')
  usage.value = pick<CodeBuddyCreditsUsage>('codebuddy_credits_usage')
  official.value = pick<CodeBuddyRequestUsage>('codebuddy_request_usage')
}

const formatCredits = (value: number | undefined) => {
  if (value == null || Number.isNaN(value)) return '0'
  return Number.isInteger(value) ? String(value) : value.toFixed(2)
}

const totalCredits = computed(() => {
  const snap = snapshot.value
  if (!snap) return 0
  if (snap.total && snap.total > 0) return snap.total
  return (snap.segments || []).reduce((sum, segment) => sum + (segment.total || 0), 0)
})

const remainingRatio = computed(() => {
  const total = totalCredits.value
  if (total <= 0) return 1
  return Math.max(0, (snapshot.value?.credits ?? 0) / total)
})

const usedPercent = computed(() => Math.min(100, Math.max(0, (1 - remainingRatio.value) * 100)))

const balanceClass = computed(() => {
  const snap = snapshot.value
  if (snap?.unlimited) return 'text-violet-700 dark:text-violet-300'
  if (snap?.exhausted || (snap?.credits ?? 0) <= 0) return 'text-red-600 dark:text-red-400'
  if (remainingRatio.value <= 0.1) return 'text-amber-600 dark:text-amber-400'
  return 'text-violet-700 dark:text-violet-300'
})

const barClass = computed(() => {
  if (remainingRatio.value <= 0.1) return 'bg-red-500'
  if (remainingRatio.value <= 0.3) return 'bg-amber-500'
  return 'bg-violet-500'
})

const formatDateTime = (unixSeconds?: number | null) => {
  if (!unixSeconds) return ''
  const date = new Date(unixSeconds * 1000)
  return Number.isNaN(date.getTime()) ? '' : date.toLocaleString()
}

const expiryLabel = computed(() => {
  const snap = snapshot.value
  if (!snap) return ''
  if (snap.enterprise && snap.reset_at) {
    return `${t('admin.accounts.codebuddyOAuth.credits.resetAt')} ${formatDateTime(snap.reset_at)}`
  }
  if (snap.soonest_expiry) {
    return `${t('admin.accounts.codebuddyOAuth.credits.expires')} ${formatDateTime(snap.soonest_expiry)}`
  }
  return ''
})

/** Native tooltip listing the credit packages. */
const detailTitle = computed(() =>
  (snapshot.value?.segments || [])
    .map((segment) => {
      const expires = segment.expires_at ? ` · ${formatDateTime(segment.expires_at)}` : ''
      return `${segment.source || 'credits'}: ${formatCredits(segment.remaining)} / ${formatCredits(segment.total)}${expires}`
    })
    .join('\n')
)

const stale = computed(() => {
  const fetchedAt = snapshot.value?.fetched_at
  return !fetchedAt || Date.now() / 1000 - fetchedAt > SNAPSHOT_TTL_SECONDS
})

const localDay = () => {
  const now = new Date()
  return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`
}

/** CodeBuddy's own billing report wins: it also counts clients outside this gateway. */
const todayUsage = computed(() => {
  const billed = official.value?.days?.find((entry) => entry.day === localDay())
  if (billed) return { credits: billed.credits, requests: billed.requests, official: true }
  if (!usage.value || usage.value.day !== localDay()) return null
  return { credits: usage.value.credits, requests: usage.value.requests, official: false }
})

async function refresh() {
  loading.value = true
  error.value = ''
  try {
    const report = await adminAPI.codebuddy.queryCredits(props.account.id, true)
    snapshot.value = report.credits !== undefined || report.unlimited ? (report as CodeBuddyCreditsSnapshot) : null
    usage.value = report.usage ?? null
    official.value = report.official ?? null
  } catch (err: unknown) {
    error.value = extractApiErrorMessage(err, t('admin.accounts.codebuddyOAuth.credits.failed'))
  } finally {
    loading.value = false
  }
}

watch(() => props.account.id, () => {
  error.value = ''
  loadFromExtra()
})

onMounted(loadFromExtra)
</script>
