<template>
  <div class="min-w-[190px] max-w-[280px] space-y-2 text-xs" data-testid="perplexity-usage">
    <p class="text-[10px] text-gray-500 dark:text-gray-400">{{ t('admin.accounts.perplexity.usage.snapshot') }}</p>
    <p :class="sessionValid === false ? 'text-red-600 dark:text-red-400' : 'text-gray-600 dark:text-gray-400'" data-testid="perplexity-session-status">
      {{ t(`admin.accounts.perplexity.usage.${sessionValid === true ? 'sessionValid' : sessionValid === false ? 'sessionInvalid' : 'sessionUnknown'}`) }}
    </p>
    <div v-for="section in sections" :key="section.name" :data-testid="`perplexity-usage-${section.name}`" class="border-l-2 border-gray-200 pl-2 dark:border-dark-600">
      <div class="flex flex-wrap items-center gap-x-2">
        <span class="font-medium text-gray-700 dark:text-gray-300">{{ t(`admin.accounts.perplexity.usage.${section.name}`) }}</span>
        <span :class="section.status === 'stale' ? 'text-amber-600 dark:text-amber-400' : 'text-gray-500 dark:text-gray-400'">
          {{ t(`admin.accounts.perplexity.usage.${section.status === 'success' ? 'success' : section.status === 'stale' ? 'stale' : 'unknown'}`) }}
        </span>
      </div>
      <div v-if="section.fetchedAt" class="text-[10px] text-gray-500 dark:text-gray-400" :title="section.fetchedAt">
        {{ t('admin.accounts.perplexity.usage.lastGood') }} {{ formatTime(section.fetchedAt) }}
      </div>
      <div v-if="section.error" class="break-words text-[10px] text-amber-600 dark:text-amber-400" :title="section.error" data-testid="perplexity-section-error">{{ section.error }}</div>
      <p v-if="section.summary" class="break-words text-[10px] text-gray-600 dark:text-gray-400">{{ section.summary }}</p>
      <details v-if="section.snapshot" class="mt-0.5 text-gray-600 dark:text-gray-400">
        <summary class="cursor-pointer text-[10px]">{{ t('admin.accounts.perplexity.usage.details') }}</summary>
        <pre class="mt-1 max-h-36 overflow-auto whitespace-pre-wrap break-all rounded bg-gray-50 p-1 text-[10px] dark:bg-dark-700">{{ JSON.stringify(section.snapshot, null, 2) }}</pre>
      </details>
      <div v-else class="text-[10px] text-gray-400">{{ t('admin.accounts.perplexity.usage.noFields') }}</div>
      <div v-if="section.attemptedAt && section.status !== 'success'" class="text-[10px] text-gray-400" :title="section.attemptedAt">
        {{ t('admin.accounts.perplexity.usage.attempted') }} {{ formatTime(section.attemptedAt) }}
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PerplexityUsageInfo, PerplexityUsageSectionName } from '@/types'

const props = defineProps<{ usage?: PerplexityUsageInfo | null; unavailable?: boolean }>()
const { t, locale } = useI18n()
const sessionValid = computed(() => props.unavailable ? undefined : props.usage?.session_valid)
const sectionNames: PerplexityUsageSectionName[] = ['session', 'credits', 'rate_limits']
const sections = computed(() => sectionNames.map(name => ({
  name,
  status: props.unavailable ? (props.usage?.snapshot?.[name] ? 'stale' : undefined) : props.usage?.sections?.[name]?.status,
  fetchedAt: props.usage?.sections?.[name]?.fetched_at,
  attemptedAt: props.usage?.sections?.[name]?.attempted_at,
  error: props.usage?.sections?.[name]?.error,
  snapshot: props.usage?.snapshot?.[name],
  summary: snapshotSummary(props.usage?.snapshot?.[name])
})))

function snapshotSummary(snapshot?: Record<string, unknown>): string {
  if (!snapshot) return ''
  return Object.entries(snapshot)
    .filter(([key, value]) => key !== 'email' && (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean'))
    .slice(0, 3)
    .map(([key, value]) => `${key}: ${String(value)}`)
    .join(' · ')
}

function formatTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString(locale.value)
}
</script>
