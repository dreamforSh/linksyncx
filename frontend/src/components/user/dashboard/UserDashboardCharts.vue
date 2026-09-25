<template>
  <!-- 用量分析：筛选放在它所控制的内容正上方 -->
  <section class="space-y-4" :aria-label="t('admin.dashboard.analytics.title')">
    <div class="flex flex-wrap items-end justify-between gap-3">
      <div>
        <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.dashboard.analytics.title') }}</h2>
        <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.dashboard.analytics.subtitle') }}</p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <DateRangePicker
          :start-date="startDate"
          :end-date="endDate"
          @update:startDate="$emit('update:startDate', $event)"
          @update:endDate="$emit('update:endDate', $event)"
          @change="$emit('dateRangeChange', $event)"
        />
        <SegmentedControl
          :model-value="granularity"
          :options="granularityOptions"
          :aria-label="t('dashboard.granularity')"
          @update:model-value="(value) => $emit('update:granularity', value)"
          @change="$emit('granularityChange')"
        />
        <button
          type="button"
          class="icon-btn border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900"
          :disabled="loading"
          :title="t('common.refresh')"
          :aria-label="t('common.refresh')"
          @click="$emit('refresh')"
        >
          <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
        </button>
      </div>
    </div>

    <TokenUsageTrend :trend-data="trend" :loading="loading" />

    <div class="grid grid-cols-1 gap-4 xl:grid-cols-2">
      <ModelDistributionChart
        v-model:metric="modelMetric"
        :model-stats="models"
        :loading="loading"
        :show-metric-toggle="true"
        :enable-breakdown="false"
        :show-account-cost="false"
      />
      <!-- 右侧卡片（最近使用）由页面传入 -->
      <slot />
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import Icon from '@/components/icons/Icon.vue'
import TokenUsageTrend from '@/components/charts/TokenUsageTrend.vue'
import ModelDistributionChart from '@/components/charts/ModelDistributionChart.vue'
import type { TrendDataPoint, ModelStat } from '@/types'

defineProps<{ loading: boolean, startDate: string, endDate: string, granularity: string, trend: TrendDataPoint[], models: ModelStat[] }>()
defineEmits(['update:startDate', 'update:endDate', 'update:granularity', 'dateRangeChange', 'granularityChange', 'refresh'])
const { t } = useI18n()

const modelMetric = ref<'tokens' | 'actual_cost'>('tokens')

const granularityOptions = computed(() => [
  { value: 'hour', label: t('dashboard.hour') },
  { value: 'day', label: t('dashboard.day') }
])
</script>
