<template>
  <fieldset data-testid="group-managed-type-field">
    <legend class="input-label">{{ t('admin.groups.groupKind.managedType') }}</legend>
    <div class="grid gap-3 sm:grid-cols-2">
      <label
        v-for="option in options"
        :key="option.value"
        class="flex cursor-pointer items-start gap-3 rounded-xl border p-3 transition-colors duration-150"
        :class="modelValue === option.value
          ? 'border-primary-400 bg-primary-50/60 ring-1 ring-primary-400 dark:border-primary-500 dark:bg-primary-900/10 dark:ring-primary-500'
          : 'border-gray-200 hover:border-gray-300 dark:border-dark-600 dark:hover:border-dark-500'"
        :data-testid="`group-managed-type-${option.value}`"
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
    <p
      v-if="changeWarning"
      class="mt-2 flex items-start gap-2 rounded-lg bg-amber-50 px-3 py-2 text-xs leading-relaxed text-amber-800 dark:bg-amber-500/10 dark:text-amber-200"
      role="status"
      data-testid="group-managed-type-warning"
    >
      <Icon name="exclamationTriangle" size="sm" class="shrink-0" />
      <span>{{ changeWarning }}</span>
    </p>
  </fieldset>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { ManagedGroupType } from '@/types'

const props = withDefaults(defineProps<{
  modelValue: ManagedGroupType
  name?: string
  // 编辑时传入当前类型：切换类型会提示对成员与计费的影响
  original?: ManagedGroupType | null
}>(), {
  name: 'group-managed-type',
  original: null
})

const emit = defineEmits<{
  (e: 'update:modelValue', value: ManagedGroupType): void
}>()

const { t } = useI18n()

const options = computed(() => [
  {
    value: 'quota' as const,
    icon: 'dollar' as const,
    label: t('admin.groups.groupKind.quota'),
    description: t('admin.groups.groupKind.quotaDesc')
  },
  {
    value: 'subscription' as const,
    icon: 'server' as const,
    label: t('admin.groups.groupKind.subscription'),
    description: t('admin.groups.groupKind.subscriptionDesc')
  }
])

const changeWarning = computed(() => {
  if (!props.original || props.original === props.modelValue) return ''
  return props.modelValue === 'quota'
    ? t('admin.groups.groupKind.changeToQuota')
    : t('admin.groups.groupKind.changeToSubscription')
})
</script>
