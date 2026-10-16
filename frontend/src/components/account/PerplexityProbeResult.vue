<template>
  <section class="space-y-3 rounded-xl border p-3 text-xs" :class="tone" role="status" :aria-live="current ? 'polite' : 'off'" data-testid="perplexity-probe-result">
    <div class="flex flex-wrap items-start justify-between gap-2">
      <h3 class="font-semibold">{{ t(`admin.accounts.perplexity.probe.${current ? 'title' : 'recentTitle'}`) }}</h3>
      <span class="rounded-full border border-current/20 px-2 py-0.5 font-medium" data-testid="perplexity-probe-status">{{ t(`admin.accounts.perplexity.probe.statuses.${status}`) }}</span>
    </div>
    <dl class="grid grid-cols-1 gap-2 sm:grid-cols-2">
      <div><dt class="opacity-70">{{ t('admin.accounts.perplexity.probe.requested') }}</dt><dd class="break-all font-medium">{{ probe.requested_model }}</dd></div>
      <div><dt class="opacity-70">{{ t('admin.accounts.perplexity.probe.sent') }}</dt><dd class="break-all font-mono">{{ probe.preference }} / {{ probe.mode }}</dd></div>
      <div><dt class="opacity-70">{{ t('admin.accounts.perplexity.probe.actual') }}</dt><dd class="break-all font-mono" data-testid="perplexity-actual-model">{{ probe.identity?.actual_model || t('admin.accounts.perplexity.probe.unknownActual') }}</dd></div>
      <div><dt class="opacity-70">{{ t('admin.accounts.perplexity.probe.duration') }}</dt><dd>{{ probe.duration_ms }} ms</dd></div>
      <div v-if="probe.finished_at"><dt class="opacity-70">{{ t('admin.accounts.perplexity.probe.finishedAt') }}</dt><dd>{{ formatDateTime(probe.finished_at) }}</dd></div>
      <div v-if="probe.last_verified_at"><dt class="opacity-70">{{ t('admin.accounts.perplexity.probe.lastVerified') }}</dt><dd>{{ formatDateTime(probe.last_verified_at) }}</dd></div>
    </dl>
    <p v-if="probe.error_message" class="break-words" data-testid="perplexity-probe-error">{{ probe.error_message }}</p>
    <p v-if="probe.persistence !== 'stored'" class="font-medium" data-testid="perplexity-probe-persistence-warning">{{ t('admin.accounts.perplexity.probe.notPersisted') }}</p>
    <p class="text-[11px] leading-relaxed opacity-80">{{ t('admin.accounts.perplexity.probe.declarationHint') }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PerplexityModelProbeResult } from '@/types'
import { formatDateTime } from '@/utils/format'

const props = withDefaults(defineProps<{ probe: PerplexityModelProbeResult; current?: boolean }>(), { current: false })
const { t } = useI18n()
const statuses = ['verified', 'substituted', 'mixed', 'unknown', 'failed', 'unavailable', 'stale']
const status = computed(() => statuses.includes(props.probe.status) ? props.probe.status : 'unknown')
const tone = computed(() => {
  if (props.probe.verified && props.probe.status === 'verified') return 'border-green-200 bg-green-50 text-green-800 dark:border-green-800 dark:bg-green-900/20 dark:text-green-300'
  if (props.probe.success || props.probe.status === 'stale') return 'border-amber-200 bg-amber-50 text-amber-800 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-300'
  return 'border-red-200 bg-red-50 text-red-800 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300'
})
</script>
