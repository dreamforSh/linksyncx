<template>
  <section class="space-y-4 border-t border-gray-200 pt-4 dark:border-dark-600" data-testid="perplexity-options">
    <div>
      <h3 class="input-label">{{ t('admin.accounts.perplexity.searchOptions') }}</h3>
      <p class="input-hint">{{ t('admin.accounts.perplexity.searchOptionsHint') }}</p>
    </div>
    <fieldset>
      <legend class="input-label">{{ t('admin.accounts.perplexity.sources') }}</legend>
      <div class="flex flex-wrap gap-3">
        <label v-for="source in sources" :key="source" class="inline-flex cursor-pointer items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
          <input
            type="checkbox"
            class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-500"
            :data-testid="`perplexity-source-${source}`"
            :checked="modelValue.sources.includes(source)"
            @change="toggleSource(source, ($event.target as HTMLInputElement).checked)"
          />
          {{ t(`admin.accounts.perplexity.sourceLabels.${source}`) }}
        </label>
      </div>
    </fieldset>
    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
      <label>
        <span class="input-label">{{ t('admin.accounts.perplexity.language') }}</span>
        <input class="input" type="text" data-testid="perplexity-language" placeholder="en-US / zh-CN" :value="modelValue.language" @input="update('language', ($event.target as HTMLInputElement).value)" />
      </label>
      <label>
        <span class="input-label">{{ t('admin.accounts.perplexity.timezone') }}</span>
        <input class="input" type="text" data-testid="perplexity-timezone" placeholder="UTC / Asia/Shanghai" :value="modelValue.timezone" @input="update('timezone', ($event.target as HTMLInputElement).value)" />
      </label>
    </div>
    <label class="inline-flex cursor-pointer items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
      <input type="checkbox" class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500" data-testid="perplexity-incognito" :checked="modelValue.is_incognito" @change="update('is_incognito', ($event.target as HTMLInputElement).checked)" />
      {{ t('admin.accounts.perplexity.incognito') }}
    </label>
    <details class="rounded-lg border border-gray-200 px-3 py-2 dark:border-dark-600">
      <summary class="cursor-pointer text-sm font-medium text-gray-700 focus-visible:outline-primary-500 dark:text-gray-300">{{ t('admin.accounts.perplexity.advanced') }}</summary>
      <div class="mt-3 space-y-3">
        <label v-for="option in localOptions" :key="option" class="flex cursor-pointer items-start gap-2 text-sm text-gray-700 dark:text-gray-300">
          <input type="checkbox" class="mt-0.5 h-4 w-4 shrink-0 rounded border-gray-300 text-primary-600 focus:ring-primary-500" :data-testid="`perplexity-${option}`" :checked="modelValue[option]" @change="update(option, ($event.target as HTMLInputElement).checked)" />
          <span>{{ t(`admin.accounts.perplexity.${option}`) }}</span>
        </label>
        <p v-if="modelValue.keep_system_prompt" class="input-hint">{{ t('admin.accounts.perplexity.keepSystemHint') }}</p>
        <label class="block">
          <span class="input-label">{{ t('admin.accounts.perplexity.maxQueryChars') }}</span>
          <input class="input" type="number" min="0" max="96000" step="1" data-testid="perplexity-max-query-chars" :aria-invalid="validationError === 'invalidQueryChars'" :value="Number.isFinite(modelValue.max_query_chars) ? modelValue.max_query_chars : ''" @input="updateQueryLimit(($event.target as HTMLInputElement).value)" />
        </label>
        <p class="input-hint">{{ t('admin.accounts.perplexity.queryLimitHint') }}</p>
        <p v-if="modelValue.max_query_chars === 0" class="text-xs text-amber-600 dark:text-amber-400" role="note" data-testid="perplexity-unlimited-warning">{{ t('admin.accounts.perplexity.unlimitedWarning') }}</p>
        <p class="input-hint">{{ t('admin.accounts.perplexity.strictHint') }}</p>
      </div>
    </details>
    <p v-if="validationError" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ t(`admin.accounts.perplexity.${validationError}`) }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PerplexityRequestOptions, PerplexitySource } from '@/types'
import { validatePerplexityOptions } from './credentialsBuilder'

const props = defineProps<{ modelValue: PerplexityRequestOptions }>()
const emit = defineEmits<{ 'update:modelValue': [value: PerplexityRequestOptions] }>()
const { t } = useI18n()
const sources: PerplexitySource[] = ['web', 'scholar', 'social']
const localOptions = ['include_sources', 'include_search_progress', 'native_followups', 'strict_parameters', 'keep_system_prompt'] as const
const validationError = computed(() => validatePerplexityOptions(props.modelValue))

function update<K extends keyof PerplexityRequestOptions>(key: K, value: PerplexityRequestOptions[K]): void {
  emit('update:modelValue', { ...props.modelValue, [key]: value })
}

function updateQueryLimit(value: string): void {
  update('max_query_chars', value === '' ? Number.NaN : Number(value))
}

function toggleSource(source: PerplexitySource, checked: boolean): void {
  update('sources', checked
    ? [...new Set([...props.modelValue.sources, source])]
    : props.modelValue.sources.filter(value => value !== source))
}
</script>
