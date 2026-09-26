<template>
  <div class="space-y-3" data-testid="clash-refresh-summary">
    <div
      v-if="result.status !== 'ok'"
      role="alert"
      :class="[
        'rounded-lg border px-3 py-2 text-sm',
        result.status === 'skipped'
          ? 'border-amber-200 bg-amber-50 text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200'
          : 'border-red-200 bg-red-50 text-red-700 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-300'
      ]"
    >
      <p class="font-medium">
        {{ result.status === 'skipped' ? t('admin.clash.refresh.skippedTitle') : t('admin.clash.refresh.failedTitle') }}
      </p>
      <p v-if="result.error" class="mt-0.5 break-all text-xs opacity-90">{{ result.error }}</p>
    </div>

    <dl class="grid grid-cols-3 gap-2 sm:grid-cols-6">
      <div v-for="item in items" :key="item.key" class="rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-800/60">
        <dt class="text-[11px] text-gray-500 dark:text-dark-400">{{ item.label }}</dt>
        <dd class="mt-0.5 text-sm font-semibold tabular-nums" :class="item.class">{{ item.value }}</dd>
      </div>
    </dl>

    <div v-if="result.skipped?.length" class="rounded-lg bg-amber-50 p-3 text-xs text-amber-800 dark:bg-amber-500/10 dark:text-amber-200">
      <p class="font-medium">{{ t('admin.clash.preview.skipped', { count: result.skipped.length }) }}</p>
      <ul class="mt-1 space-y-0.5">
        <li v-for="(item, index) in result.skipped" :key="`${item.name}-${index}`" class="break-all">
          {{ item.name || '-' }} — {{ item.reason }}
        </li>
      </ul>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ClashRefreshResult } from '@/types'

const props = defineProps<{ result: ClashRefreshResult }>()

const { t } = useI18n()

const items = computed(() => [
  { key: 'parsed', label: t('admin.clash.refresh.parsed'), value: props.result.parsed, class: 'text-gray-900 dark:text-white' },
  { key: 'filtered', label: t('admin.clash.refresh.filtered'), value: props.result.filtered, class: 'text-gray-500 dark:text-dark-300' },
  { key: 'private', label: t('admin.clash.refresh.private'), value: props.result.private, class: 'text-gray-500 dark:text-dark-300' },
  { key: 'inserted', label: t('admin.clash.refresh.inserted'), value: props.result.inserted, class: 'text-emerald-600 dark:text-emerald-400' },
  { key: 'updated', label: t('admin.clash.refresh.updated'), value: props.result.updated, class: 'text-primary-600 dark:text-primary-400' },
  { key: 'missing', label: t('admin.clash.refresh.missing'), value: props.result.missing, class: 'text-amber-600 dark:text-amber-400' }
])
</script>
