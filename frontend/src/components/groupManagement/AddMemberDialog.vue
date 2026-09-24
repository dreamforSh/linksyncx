<template>
  <BaseDialog :show="show" :title="t('groupManagement.addMember.title')" width="narrow" @close="emit('close')">
    <form :id="formId" novalidate @submit.prevent="submit">
      <label :for="`${formId}-input`" class="input-label">{{ t('groupManagement.addMember.userId') }}</label>
      <input
        :id="`${formId}-input`"
        ref="inputRef"
        v-model.number="userId"
        type="number"
        inputmode="numeric"
        min="1"
        step="1"
        autocomplete="off"
        class="input"
        :class="error && 'input-error'"
        :placeholder="t('groupManagement.addMember.placeholder')"
        :aria-invalid="error ? 'true' : undefined"
        :aria-describedby="`${formId}-msg`"
        @input="attempted = false"
      />
      <p :id="`${formId}-msg`" :class="error ? 'input-error-text' : 'input-hint'" :role="error ? 'alert' : undefined">
        {{ error || t('groupManagement.addMember.hint', { concurrency: defaults.max_concurrent, daily: dailyLabel }) }}
      </p>
    </form>

    <template #footer>
      <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
      <button type="submit" :form="formId" class="btn btn-primary" :disabled="saving">
        <span v-if="saving" class="spinner h-4 w-4" aria-hidden="true" />
        {{ t('groupManagement.addMember.submit') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import type { MemberLimit } from '@/api/groupManagement'
import { isValidLimit } from './helpers'

const props = defineProps<{
  show: boolean
  saving: boolean
  defaults: MemberLimit
  existingIds: number[]
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'submit', userId: number): void
}>()

const { t } = useI18n()

const formId = 'group-add-member-form'
const inputRef = ref<HTMLInputElement | null>(null)
const userId = ref<number | ''>('')
const attempted = ref(false)

const dailyLabel = computed(() => props.defaults.daily_limit > 0 ? props.defaults.daily_limit : t('groupManagement.unlimited'))
const error = computed(() => {
  if (!attempted.value) return ''
  if (!isValidLimit(userId.value, 1)) return t('groupManagement.addMember.invalid')
  if (props.existingIds.includes(userId.value)) return t('groupManagement.addMember.exists')
  return ''
})

watch(() => props.show, open => {
  if (!open) return
  userId.value = ''
  attempted.value = false
  // BaseDialog focuses its first focusable element (the close button) on open;
  // move focus to the only field once that has happened.
  window.setTimeout(() => inputRef.value?.focus(), 0)
})

function submit() {
  attempted.value = true
  if (error.value || props.saving || !isValidLimit(userId.value, 1)) return
  emit('submit', userId.value)
}
</script>
