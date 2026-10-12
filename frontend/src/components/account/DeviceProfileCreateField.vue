<template>
  <div class="space-y-3" data-testid="device-profile-create-field">
    <div>
      <label class="input-label mb-0">{{ t('admin.accounts.deviceProfile.title') }}</label>
      <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
        {{ platform === 'anthropic'
          ? t('admin.accounts.deviceProfile.claudeHint')
          : t('admin.accounts.deviceProfile.codexHint') }}
      </p>
    </div>

    <div class="flex flex-wrap gap-2">
      <button
        v-for="option in modeOptions"
        :key="option.value"
        type="button"
        :data-testid="`device-profile-mode-${option.value}`"
        :class="[
          'px-3 py-1.5 text-sm rounded-md border transition-colors',
          mode === option.value
            ? 'bg-primary-600 text-white border-primary-600'
            : 'bg-white dark:bg-dark-700 text-gray-700 dark:text-gray-300 border-gray-300 dark:border-dark-500 hover:bg-gray-50 dark:hover:bg-dark-600'
        ]"
        @click="emit('update:mode', option.value)"
      >
        {{ option.label }}
      </button>
    </div>
    <p class="text-xs text-gray-500 dark:text-gray-400" data-testid="device-profile-mode-hint">
      {{ modeHint }}
    </p>

    <DeviceProfileEditor
      v-if="mode === 'manual'"
      :platform="platform"
      :model-value="modelValue"
      @update:model-value="emit('update:modelValue', $event)"
    />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import DeviceProfileEditor from './DeviceProfileEditor.vue'
import type { DeviceProfileCreateMode, DeviceProfileValue } from './credentialsBuilder'

const props = defineProps<{
  platform: 'anthropic' | 'openai'
  mode: DeviceProfileCreateMode
  modelValue: DeviceProfileValue
}>()

const emit = defineEmits<{
  (e: 'update:mode', value: DeviceProfileCreateMode): void
  (e: 'update:modelValue', value: DeviceProfileValue): void
}>()

const { t } = useI18n()

const modeOptions = computed<{ value: DeviceProfileCreateMode; label: string }[]>(() => [
  { value: 'default', label: t('admin.accounts.deviceProfile.create.default') },
  { value: 'random', label: t('admin.accounts.deviceProfile.create.random') },
  { value: 'manual', label: t('admin.accounts.deviceProfile.create.manual') }
])

const modeHint = computed(() => {
  switch (props.mode) {
    case 'random':
      return t('admin.accounts.deviceProfile.create.randomHint')
    case 'manual':
      return t('admin.accounts.deviceProfile.create.manualHint')
    default:
      return t('admin.accounts.deviceProfile.create.defaultHint')
  }
})
</script>
