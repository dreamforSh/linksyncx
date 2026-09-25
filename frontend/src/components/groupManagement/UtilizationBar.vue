<template>
  <div class="min-w-0">
    <div class="flex items-baseline justify-between gap-2 text-xs">
      <span class="font-medium text-gray-600 dark:text-dark-300">{{ label }}</span>
      <span v-if="window" class="font-semibold tabular-nums" :class="toneText">{{ percentLabel }}</span>
      <span v-else class="text-gray-400 dark:text-dark-500">{{ t('groupManagement.accountUsage.noData') }}</span>
    </div>
    <div
      class="mt-1.5 h-1.5 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700"
      role="progressbar"
      :aria-label="label"
      aria-valuemin="0"
      aria-valuemax="100"
      :aria-valuenow="window ? Math.min(Math.round(window.utilization), 100) : undefined"
      :aria-valuetext="window ? percentLabel : t('groupManagement.accountUsage.noData')"
    >
      <div
        v-if="window"
        class="h-full rounded-full transition-[width] duration-300 ease-out motion-reduce:transition-none"
        :class="toneBar"
        :style="{ width: `${barWidth}%` }"
      />
    </div>
    <p v-if="resetLabel" class="mt-1 truncate text-[11px] text-gray-500 dark:text-dark-400">{{ resetLabel }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { GroupAccountWindow } from '@/api/groupManagement'
import { formatCountdown } from '@/utils/format'
import { quotaTone } from './helpers'

const props = defineProps<{
  label: string
  window?: GroupAccountWindow | null
}>()

const { t } = useI18n()

const utilization = computed(() => Math.max(props.window?.utilization ?? 0, 0))
const percentLabel = computed(() => `${Math.round(utilization.value)}%`)
const barWidth = computed(() => utilization.value > 0 ? Math.min(Math.max(utilization.value, 2), 100) : 0)
const tone = computed(() => quotaTone(utilization.value, 100))
const toneBar = computed(() => ({
  normal: 'bg-primary-500',
  warning: 'bg-amber-500',
  danger: 'bg-red-500'
}[tone.value]))
const toneText = computed(() => ({
  normal: 'text-gray-700 dark:text-gray-200',
  warning: 'text-amber-600 dark:text-amber-400',
  danger: 'text-red-600 dark:text-red-400'
}[tone.value]))
const resetLabel = computed(() => {
  const countdown = formatCountdown(props.window?.resets_at)
  return countdown ? t('groupManagement.accountUsage.resetsIn', { time: countdown }) : ''
})
</script>
