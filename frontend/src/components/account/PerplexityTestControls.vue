<template>
  <div class="space-y-3" data-testid="perplexity-test-controls">
    <label class="block space-y-1.5">
      <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.accounts.perplexity.probe.mode') }}</span>
      <Select :model-value="modelValue" :options="options" :disabled="disabled" data-testid="perplexity-test-mode" @update:model-value="updateMode" />
    </label>
    <p class="rounded-lg border px-3 py-2 text-xs leading-relaxed" :class="modelValue === 'perplexity_model' ? 'border-amber-200 bg-amber-50 text-amber-800 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-300' : 'border-gray-200 text-gray-600 dark:border-dark-600 dark:text-gray-400'" data-testid="perplexity-test-hint">
      {{ t(`admin.accounts.perplexity.probe.${modelValue === 'perplexity_model' ? scheduled ? 'scheduleHint' : 'costHint' : 'sessionHint'}`) }}
    </p>
    <PerplexityProbeResult v-if="modelValue === 'perplexity_model' && probe" :probe="probe" :current="current" />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import PerplexityProbeResult from './PerplexityProbeResult.vue'
import type { PerplexityModelProbeResult } from '@/types'

withDefaults(defineProps<{
  modelValue: 'default' | 'perplexity_model'
  disabled?: boolean
  probe?: PerplexityModelProbeResult | null
  current?: boolean
  scheduled?: boolean
}>(), { disabled: false, probe: null, current: false, scheduled: false })
const emit = defineEmits<{ 'update:modelValue': [value: 'default' | 'perplexity_model'] }>()
const { t } = useI18n()
const options = computed(() => [
  { value: 'default', label: t('admin.accounts.perplexity.probe.sessionMode') },
  { value: 'perplexity_model', label: t('admin.accounts.perplexity.probe.modelMode') }
])
function updateMode(value: string | number | boolean | null): void {
  emit('update:modelValue', value === 'perplexity_model' ? 'perplexity_model' : 'default')
}
</script>
