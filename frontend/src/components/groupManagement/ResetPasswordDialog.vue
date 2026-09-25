<template>
  <BaseDialog :show="show" :title="title" width="narrow" @close="emit('close')">
    <CredentialsCard
      v-if="resetPassword"
      :title="t('groupManagement.resetPassword.doneTitle')"
      :hint="t('groupManagement.resetPassword.doneHint')"
      :email="member?.email"
      :password="resetPassword"
    />
    <form v-else :id="formId" novalidate @submit.prevent="submit">
      <PasswordField
        :id="`${formId}-password`"
        v-model="password"
        :label="t('groupManagement.resetPassword.password')"
        :hint="t('groupManagement.resetPassword.hint')"
        :error="error"
      />
    </form>

    <template #footer>
      <template v-if="resetPassword">
        <button type="button" class="btn btn-primary" @click="emit('close')">{{ t('groupManagement.createUser.done') }}</button>
      </template>
      <template v-else>
        <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
        <button type="submit" :form="formId" class="btn btn-primary" :disabled="saving">
          <span v-if="saving" class="spinner h-4 w-4" aria-hidden="true" />
          {{ t('groupManagement.resetPassword.submit') }}
        </button>
      </template>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import type { GroupManagementMember } from '@/api/groupManagement'
import CredentialsCard from './CredentialsCard.vue'
import PasswordField from './PasswordField.vue'
import { generatePassword, isValidPassword, memberDisplayName } from './helpers'

const props = defineProps<{
  show: boolean
  saving: boolean
  member: GroupManagementMember | null
  // 重置成功后由父组件回填，切换到展示新密码的状态
  resetPassword: string | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'submit', password: string): void
}>()

const { t } = useI18n()

const formId = 'group-reset-password-form'
const password = ref('')
const attempted = ref(false)

const title = computed(() => t('groupManagement.resetPassword.title', { name: props.member ? memberDisplayName(props.member) : '' }))
const error = computed(() => attempted.value && !isValidPassword(password.value) ? t('groupManagement.createUser.invalidPassword') : '')

watch(() => props.show, open => {
  if (!open) return
  password.value = generatePassword()
  attempted.value = false
}, { immediate: true })

function submit() {
  attempted.value = true
  if (error.value || props.saving) return
  emit('submit', password.value)
}
</script>
