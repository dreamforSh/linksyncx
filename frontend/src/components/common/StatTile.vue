<template>
  <div class="stat-tile" :class="{ 'stat-tile-compact': compact }">
    <div class="flex items-center justify-between gap-2">
      <div class="flex min-w-0 items-center gap-2">
        <span v-if="icon" class="stat-tile-icon" aria-hidden="true">
          <Icon :name="icon" size="sm" :stroke-width="1.75" />
        </span>
        <p class="truncate text-[13px] font-medium text-gray-500 dark:text-dark-300">{{ label }}</p>
      </div>
      <slot name="badge" />
    </div>

    <template v-if="loading">
      <div class="skeleton mt-3 h-7 w-28 rounded-md" />
      <div class="skeleton mt-2 h-3.5 w-36 rounded" />
    </template>
    <template v-else>
      <p class="stat-tile-value" :title="valueTitle || value">
        {{ value }}<span v-if="unit" class="ml-1 text-sm font-medium text-gray-400 dark:text-dark-400">{{ unit }}</span>
      </p>
      <div v-if="$slots.default" class="mt-1 text-xs text-gray-500 dark:text-dark-400">
        <slot />
      </div>
    </template>

    <div v-if="$slots.footer && !loading" class="mt-3">
      <slot name="footer" />
    </div>

    <div v-if="trend && trend.length > 1" class="mt-auto pt-3">
      <Sparkline :values="trend" :height="32" :aria-label="trendLabel" />
    </div>
  </div>
</template>

<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'
import Sparkline from '@/components/charts/Sparkline.vue'

type IconName = InstanceType<typeof Icon>['$props']['name']

withDefaults(
  defineProps<{
    label: string
    value: string
    unit?: string
    valueTitle?: string
    icon?: IconName
    loading?: boolean
    trend?: number[]
    trendLabel?: string
    compact?: boolean
  }>(),
  {
    unit: undefined,
    valueTitle: undefined,
    icon: undefined,
    loading: false,
    trend: undefined,
    trendLabel: undefined,
    compact: false
  }
)
</script>
