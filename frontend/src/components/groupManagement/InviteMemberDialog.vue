<template>
  <BaseDialog :show="show" :title="t('groupManagement.invitations.dialogTitle')" width="narrow" @close="emit('close')">
    <form :id="formId" novalidate @submit.prevent="submit">
      <label :for="`${formId}-email`" class="input-label">{{ t('groupManagement.invitations.email') }}</label>
      <input
        :id="`${formId}-email`"
        ref="inputRef"
        v-model.trim="email"
        type="email"
        autocomplete="off"
        maxlength="255"
        class="input"
        :class="error && 'input-error'"
        :placeholder="t('groupManagement.invitations.emailPlaceholder')"
        :aria-invalid="error ? 'true' : undefined"
        :aria-describedby="`${formId}-msg`"
        @input="attempted = false"
      />
      <p :id="`${formId}-msg`" :class="error ? 'input-error-text' : 'input-hint'" :role="error ? 'alert' : undefined">
        {{ error || t('groupManagement.invitations.dialogHint') }}
      </p>
    </form>

    <template #footer>
      <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
      <button type="submit" :form="formId" class="btn btn-primary" :disabled="saving">
        <span v-if="saving" class="spinner h-4 w-4" aria-hidden="true" />
        {{ t('groupManagement.invitations.send') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { isValidEmail } from './helpers'

const props = defineProps<{
  show: boolean
  saving: boolean
  // 已是成员的邮箱，避免重复邀请
  memberEmails: string[]
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'submit', email: string): void
}>()

const { t } = useI18n()

const formId = 'group-invite-member-form'
const inputRef = ref<HTMLInputElement | null>(null)
const email = ref('')
const attempted = ref(false)

const error = computed(() => {
  if (!attempted.value) return ''
  if (!isValidEmail(email.value)) return t('groupManagement.invitations.invalidEmail')
  const normalized = email.value.toLowerCase()
  if (props.memberEmails.some(item => item.toLowerCase() === normalized)) return t('groupManagement.invitations.alreadyMember')
  return ''
})

watch(() => props.show, open => {
  if (!open) return
  email.value = ''
  attempted.value = false
  // BaseDialog 打开时先聚焦关闭按钮，随后把焦点移到唯一的输入框
  window.setTimeout(() => inputRef.value?.focus(), 0)
})

function submit() {
  attempted.value = true
  if (error.value || props.saving) return
  emit('submit', email.value)
}
</script>
