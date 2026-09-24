<template>
  <div data-testid="group-category-field">
    <label :id="labelId" class="input-label">{{ t('admin.groups.groupKind.category') }}</label>
    <div
      role="radiogroup"
      :aria-labelledby="labelId"
      class="inline-flex rounded-lg bg-gray-100 p-1 dark:bg-dark-700"
    >
      <button
        v-for="option in options"
        :key="option.value"
        type="button"
        role="radio"
        :aria-checked="modelValue === option.value"
        :data-testid="`group-category-${option.value}`"
        class="flex items-center gap-1.5 rounded-md px-4 py-1.5 text-sm font-medium transition-colors duration-150 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/50"
        :class="modelValue === option.value
          ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-600 dark:text-white'
          : 'text-gray-600 hover:text-gray-900 dark:text-dark-300 dark:hover:text-white'"
        @click="emit('update:modelValue', option.value)"
      >
        <Icon :name="option.icon" size="sm" />
        {{ option.label }}
      </button>
    </div>
    <p class="input-hint">{{ t('admin.groups.groupKind.managedHint') }}</p>
  </div>
</template>

<script lang="ts">
let categoryFieldCounter = 0
</script>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { GroupCategory } from '@/types'

defineProps<{
  modelValue: GroupCategory
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', value: GroupCategory): void
}>()

const { t } = useI18n()

const labelId = `group-category-label-${++categoryFieldCounter}`

const options = computed(() => [
  { value: 'enterprise' as const, icon: 'badge' as const, label: t('admin.groups.groupKind.enterprise') },
  { value: 'team' as const, icon: 'users' as const, label: t('admin.groups.groupKind.team') }
])
</script>
