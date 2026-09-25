<template>
  <div>
    <label :for="id" class="input-label">{{ label }}</label>
    <div class="flex gap-2">
      <div class="relative min-w-0 flex-1">
        <input
          :id="id"
          :value="modelValue"
          :type="visible ? 'text' : 'password'"
          autocomplete="new-password"
          spellcheck="false"
          class="input pr-10 font-mono"
          :class="error && 'input-error'"
          :aria-invalid="error ? 'true' : undefined"
          :aria-describedby="`${id}-msg`"
          @input="emit('update:modelValue', ($event.target as HTMLInputElement).value)"
        />
        <button
          type="button"
          class="absolute right-1.5 top-1/2 -translate-y-1/2 rounded-md p-1.5 text-gray-400 transition-colors hover:text-gray-600 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/50 dark:text-dark-400 dark:hover:text-dark-200"
          :aria-label="visible ? t('groupManagement.createUser.hidePassword') : t('groupManagement.createUser.showPassword')"
          :aria-pressed="visible"
          @click="visible = !visible"
        >
          <Icon :name="visible ? 'eyeOff' : 'eye'" size="sm" />
        </button>
      </div>
      <button type="button" class="btn btn-secondary shrink-0" @click="generate">
        <Icon name="refresh" size="sm" />
        {{ t('groupManagement.createUser.generate') }}
      </button>
    </div>
    <p :id="`${id}-msg`" :class="error ? 'input-error-text' : 'input-hint'" :role="error ? 'alert' : undefined">
      {{ error || hint }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { generatePassword } from './helpers'

defineProps<{
  id: string
  modelValue: string
  label: string
  hint: string
  error?: string
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
}>()

const { t } = useI18n()
const visible = ref(false)

function generate() {
  emit('update:modelValue', generatePassword())
  // 生成后直接显示，方便核对与转交
  visible.value = true
}
</script>
