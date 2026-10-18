<template>
  <div class="space-y-1" data-testid="perplexity-usage">
    <!-- 会话失效：与其他平台同款"需要重新授权"徽章 -->
    <div v-if="sessionValid === false" data-testid="perplexity-session-status">
      <span
        class="inline-block rounded bg-orange-100 px-1.5 py-0.5 text-[10px] font-medium text-orange-700 dark:bg-orange-900/40 dark:text-orange-300"
        :title="t('admin.accounts.perplexity.usage.sessionInvalid')"
      >
        {{ t('admin.accounts.needsReauth') }}
      </span>
    </div>

    <!-- 限额：上游只给剩余次数（无总量），按桶一行，标签与 UsageProgressBar 定宽对齐 -->
    <div
      v-for="bucket in buckets"
      :key="bucket.key"
      :class="['flex items-center gap-1', rateLimitsStale ? 'opacity-60' : '']"
      :data-testid="`perplexity-bucket-${bucket.key}`"
      :title="bucket.title"
    >
      <span :class="['w-[32px] shrink-0 rounded px-1 text-center text-[10px] font-medium', bucket.labelClass]">
        {{ bucket.label }}
      </span>
      <span :class="['text-[10px] font-medium tabular-nums', bucket.valueClass]">{{ bucket.value }}</span>
      <span class="text-[10px] text-gray-400">{{ t('admin.accounts.perplexity.usage.remaining') }}</span>
    </div>

    <div
      v-if="credits"
      :class="['text-[10px] text-gray-500 dark:text-gray-400', creditsStale ? 'opacity-60' : '']"
      data-testid="perplexity-credits"
    >
      💳 {{ t('admin.accounts.perplexity.usage.creditsBalance') }}: {{ credits }}
    </div>

    <div
      v-if="problem"
      class="max-w-[200px] truncate text-[10px] text-amber-600 dark:text-amber-400"
      :title="problem.detail"
      data-testid="perplexity-usage-problem"
    >
      {{ problem.label }}
    </div>

    <div
      v-if="!buckets.length && !credits && !problem && sessionValid !== false"
      class="text-xs text-gray-400"
    >
      -
    </div>

    <div v-if="updatedAt" class="text-[10px] text-gray-400" :title="formatDateTime(updatedAt)" data-testid="perplexity-usage-updated">
      {{ t('admin.accounts.perplexity.usage.updated', { time: formatRelativeTime(updatedAt) }) }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PerplexityUsageInfo, PerplexityUsageSectionName } from '@/types'
import { formatDateTime, formatRelativeTime } from '@/utils/format'

const props = defineProps<{ usage?: PerplexityUsageInfo | null; unavailable?: boolean }>()
const { t, te, locale } = useI18n()

// 刷新失败时会话状态按未知处理，只有后端明确 false 才判失效。
const sessionValid = computed(() => props.unavailable ? undefined : props.usage?.session_valid)

function sectionStale(name: PerplexityUsageSectionName): boolean {
  return Boolean(props.unavailable) || (props.usage?.sections?.[name]?.status ?? 'success') !== 'success'
}
const rateLimitsStale = computed(() => sectionStale('rate_limits'))
const creditsStale = computed(() => sectionStale('credits'))

const BUCKET_STYLES: Record<string, { label: string; labelClass: string }> = {
  pro: { label: 'Pro', labelClass: 'bg-indigo-100 text-indigo-700 dark:bg-indigo-900/40 dark:text-indigo-300' },
  research: { label: 'Deep', labelClass: 'bg-purple-100 text-purple-700 dark:bg-purple-900/40 dark:text-purple-300' },
  labs: { label: 'Labs', labelClass: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300' }
}
const FALLBACK_LABEL_CLASS = 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300'
const BUCKET_ORDER = ['pro', 'research', 'labs']

const numberFormat = computed(() => new Intl.NumberFormat(locale.value))

const buckets = computed(() => {
  const limits = props.usage?.snapshot?.rate_limits
  if (!limits) return []
  return Object.entries(limits)
    .filter(([key, value]) => key.startsWith('remaining_') && typeof value === 'number' && Number.isFinite(value))
    .map(([key, value]) => {
      const name = key.slice('remaining_'.length)
      const remaining = value as number
      const style = BUCKET_STYLES[name]
      const titleKey = `admin.accounts.perplexity.usage.buckets.${name}`
      const fullName = te(titleKey) ? t(titleKey) : name.replace(/_/g, ' ')
      return {
        key: name,
        order: BUCKET_ORDER.includes(name) ? BUCKET_ORDER.indexOf(name) : BUCKET_ORDER.length,
        label: style?.label ?? (name.charAt(0).toUpperCase() + name.slice(1, 4)),
        labelClass: style?.labelClass ?? FALLBACK_LABEL_CLASS,
        // 负数是上游的"不限量"约定，不是耗尽。
        value: remaining < 0 ? '∞' : numberFormat.value.format(remaining),
        valueClass: remaining === 0 ? 'text-red-600 dark:text-red-400' : 'text-gray-600 dark:text-gray-400',
        title: t('admin.accounts.perplexity.usage.bucketTitle', { name: fullName })
      }
    })
    .sort((a, b) => a.order - b.order || a.key.localeCompare(b.key))
})

const BALANCE_KEYS = ['balance', 'remaining_credits', 'remaining', 'available', 'credits']
const TOTAL_KEYS = ['total', 'total_credits', 'limit', 'granted']

const credits = computed(() => {
  const snapshot = props.usage?.snapshot?.credits
  if (!snapshot) return ''
  const pick = (keys: string[]) => {
    for (const key of keys) {
      const value = snapshot[key]
      if (typeof value === 'number' && Number.isFinite(value)) return value
    }
    return undefined
  }
  const balance = pick(BALANCE_KEYS)
  if (balance === undefined) return ''
  const total = pick(TOTAL_KEYS)
  const fmt = (n: number) => numberFormat.value.format(n)
  return total !== undefined && total > 0 ? `${fmt(balance)} / ${fmt(total)}` : fmt(balance)
})

// 失败/过期的分区汇总成一行，悬停看明细；会话失效已有徽章，不重复提示。
const problem = computed(() => {
  if (props.unavailable) {
    return { label: t('admin.accounts.perplexity.usage.staleSnapshot'), detail: t('admin.accounts.perplexity.usage.snapshot') }
  }
  const sections = props.usage?.sections
  if (!sections) return null
  const failed = (['session', 'credits', 'rate_limits'] as PerplexityUsageSectionName[])
    .filter(name => sections[name] && sections[name]?.status !== 'success')
  if (!failed.length || (sessionValid.value === false && failed.every(name => name === 'session'))) return null
  const detail = failed
    .map(name => `${t(`admin.accounts.perplexity.usage.${name}`)}: ${sections[name]?.error || t('admin.accounts.perplexity.usage.stale')}`)
    .join('\n')
  return { label: t('admin.accounts.perplexity.usage.partialFailure', { sections: failed.map(name => t(`admin.accounts.perplexity.usage.${name}`)).join(locale.value.startsWith('zh') ? '、' : ', ') }), detail }
})

// 各分区里最早的成功时间才是整份快照的真实新鲜度。
const updatedAt = computed(() => {
  const times = Object.values(props.usage?.sections ?? {})
    .map(section => section?.fetched_at)
    .filter((value): value is string => Boolean(value) && !Number.isNaN(new Date(value as string).getTime()))
    .sort()
  return times[0]
})
</script>
