<template>
  <fieldset data-testid="group-kind-field">
    <legend class="input-label">{{ t('admin.groups.groupKind.label') }}</legend>
    <div class="grid gap-3 sm:grid-cols-2">
      <label
        v-for="option in options"
        :key="option.value"
        class="flex cursor-pointer items-start gap-3 rounded-xl border p-3 transition-colors duration-150"
        :class="modelValue === option.value
          ? 'border-primary-400 bg-primary-50/60 ring-1 ring-primary-400 dark:border-primary-500 dark:bg-primary-900/10 dark:ring-primary-500'
          : 'border-gray-200 hover:border-gray-300 dark:border-dark-600 dark:hover:border-dark-500'"
        :data-testid="`group-kind-${option.value}`"
      >
        <input
          type="radio"
          :name="name"
          :value="option.value"
          :checked="modelValue === option.value"
          class="mt-0.5 h-4 w-4 shrink-0 cursor-pointer accent-primary-600"
          @change="emit('update:modelValue', option.value)"
        />
        <span class="min-w-0">
          <span class="flex items-center gap-1.5 text-sm font-medium text-gray-900 dark:text-white">
            <Icon :name="option.icon" size="sm" />
            {{ option.label }}
          </span>
          <span class="mt-0.5 block text-xs leading-relaxed text-gray-500 dark:text-dark-400">
            {{ option.description }}
          </span>
        </span>
      </label>
    </div>
    <p class="input-hint">{{ t('admin.groups.groupKind.immutable') }}</p>
  </fieldset>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { GroupKind } from '@/types'

withDefaults(defineProps<{
  modelValue: GroupKind
  name?: string
}>(), {
  name: 'group-kind'
})

const emit = defineEmits<{
  (e: 'update:modelValue', value: GroupKind): void
}>()

const { t } = useI18n()

const options = computed(() => [
  {
    value: 'channel' as const,
    icon: 'server' as const,
    label: t('admin.groups.groupKind.channel'),
    description: t('admin.groups.groupKind.channelDesc')
  },
  {
    value: 'managed' as const,
    icon: 'shield' as const,
    label: t('admin.groups.groupKind.managed'),
    description: t('admin.groups.groupKind.managedDesc')
  }
])
</script>
