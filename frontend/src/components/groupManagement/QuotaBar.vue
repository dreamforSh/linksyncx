<template>
  <div :class="size === 'lg' ? 'w-full' : 'w-full min-w-[8rem] max-w-[12rem]'">
    <div
      class="flex items-baseline justify-between gap-2"
      :class="size === 'lg' ? 'text-sm' : 'text-xs'"
    >
      <span class="tabular-nums text-gray-900 dark:text-gray-100">{{ usedLabel }} / {{ limitLabel }}</span>
      <span v-if="limited" class="font-medium tabular-nums" :class="toneText">{{ percent }}%</span>
    </div>
    <div
      class="mt-1.5 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700"
      :class="size === 'lg' ? 'h-2.5' : 'h-1.5'"
      role="progressbar"
      :aria-label="label"
      aria-valuemin="0"
      :aria-valuemax="limited ? limit : undefined"
      :aria-valuenow="limited ? Math.min(used, limit) : undefined"
      :aria-valuetext="`${usedLabel} / ${limitLabel}`"
    >
      <div
        v-if="limited"
        class="h-full rounded-full transition-[width] duration-300 ease-out motion-reduce:transition-none"
        :class="toneBar"
        :style="{ width: `${barWidth}%` }"
      />
    </div>
    <p v-if="hint" class="mt-1 truncate text-[11px] text-gray-500 dark:text-dark-400">{{ hint }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { quotaPercent, quotaTone } from './helpers'

const props = withDefaults(defineProps<{
  used: number
  limit: number
  label: string
  size?: 'sm' | 'lg'
  // 数值格式化（如美元金额）；缺省按原样显示
  format?: (value: number) => string
  // 条下方的补充说明（如窗口重置时间）
  hint?: string
}>(), {
  size: 'sm',
  format: undefined,
  hint: ''
})

const { t } = useI18n()

const display = (value: number) => props.format ? props.format(value) : String(value)
const limited = computed(() => props.limit > 0)
const usedLabel = computed(() => display(props.used))
const limitLabel = computed(() => limited.value ? display(props.limit) : t('groupManagement.unlimited'))
const percent = computed(() => quotaPercent(props.used, props.limit))
// Keep a sliver visible once anything has been used, even when it rounds to 0%.
const barWidth = computed(() => props.used > 0 ? Math.max(percent.value, 2) : 0)
const tone = computed(() => quotaTone(props.used, props.limit))
const toneBar = computed(() => ({
  normal: 'bg-primary-500',
  warning: 'bg-amber-500',
  danger: 'bg-red-500'
}[tone.value]))
const toneText = computed(() => ({
  normal: 'text-gray-500 dark:text-dark-400',
  warning: 'text-amber-600 dark:text-amber-400',
  danger: 'text-red-600 dark:text-red-400'
}[tone.value]))
</script>
