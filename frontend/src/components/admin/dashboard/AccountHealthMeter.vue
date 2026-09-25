<template>
  <div>
    <div class="flex items-baseline justify-between gap-2 text-xs">
      <span class="text-gray-500 dark:text-dark-400">{{ t('admin.dashboard.health.schedulable') }}</span>
      <span class="font-medium tabular-nums text-gray-700 dark:text-gray-200">{{ percentText }}</span>
    </div>
    <!-- 仪表：同色系浅色轨道 + 实色填充 -->
    <div
      class="mt-1.5 h-1.5 overflow-hidden rounded-full bg-primary-100 dark:bg-primary-900/40"
      role="meter"
      :aria-valuenow="normal"
      :aria-valuemin="0"
      :aria-valuemax="total"
      :aria-label="t('admin.dashboard.health.schedulable')"
    >
      <div class="h-full rounded-full bg-primary-600 transition-[width] duration-500 dark:bg-primary-500" :style="{ width: `${percent}%` }" />
    </div>
    <!-- 状态计数：图标 + 文字，颜色从不单独表达含义；三者可能与“可调度”重叠，不做加总 -->
    <ul class="mt-3 flex flex-wrap gap-x-3 gap-y-1.5 text-xs">
      <li
        v-for="item in items"
        :key="item.key"
        class="inline-flex items-center gap-1"
        :class="item.count > 0 ? 'text-gray-700 dark:text-gray-200' : 'text-gray-400 dark:text-dark-500'"
      >
        <Icon :name="item.icon" size="xs" class="h-3.5 w-3.5" :style="item.count > 0 ? { color: item.color } : undefined" :stroke-width="2" />
        <span>{{ item.label }}</span>
        <span class="font-medium tabular-nums">{{ item.count }}</span>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { STATUS_COLORS } from '@/components/charts/chartTheme'

const props = defineProps<{
  total: number
  normal: number
  ratelimit: number
  overload: number
  error: number
}>()

const { t } = useI18n()

const percent = computed(() => {
  if (!props.total) return 0
  return Math.min(100, Math.max(0, (props.normal / props.total) * 100))
})

const percentText = computed(() => `${props.normal} / ${props.total}`)

const items = computed(() => [
  { key: 'ratelimit', label: t('admin.dashboard.health.ratelimited'), count: props.ratelimit, icon: 'clock' as const, color: STATUS_COLORS.warning },
  { key: 'overload', label: t('admin.dashboard.health.overloaded'), count: props.overload, icon: 'fire' as const, color: STATUS_COLORS.serious },
  { key: 'error', label: t('admin.dashboard.health.error'), count: props.error, icon: 'exclamationCircle' as const, color: STATUS_COLORS.critical }
])
</script>
