<template>
  <BaseDialog :show="show" :title="t('groupManagement.createUser.title')" width="normal" @close="emit('close')">
    <CredentialsCard
      v-if="created"
      :title="t('groupManagement.createUser.createdTitle')"
      :hint="t('groupManagement.createUser.createdHint')"
      :email="created.email"
      :password="created.password"
    />

    <form v-else :id="formId" class="space-y-5" novalidate @submit.prevent="submit">
      <p class="flex items-start gap-2 rounded-lg bg-gray-50 px-3 py-2 text-xs leading-relaxed text-gray-600 dark:bg-dark-900/40 dark:text-dark-300">
        <Icon name="infoCircle" size="sm" class="shrink-0" />
        <span>{{ t('groupManagement.createUser.restrictedNote') }}</span>
      </p>

      <div class="grid gap-4 sm:grid-cols-2">
        <div>
          <label :for="`${formId}-email`" class="input-label">{{ t('groupManagement.createUser.email') }}</label>
          <input
            :id="`${formId}-email`"
            ref="emailRef"
            v-model.trim="form.email"
            type="email"
            autocomplete="off"
            class="input"
            :class="errors.email && 'input-error'"
            :placeholder="t('groupManagement.createUser.emailPlaceholder')"
            :aria-invalid="errors.email ? 'true' : undefined"
            :aria-describedby="`${formId}-email-msg`"
          />
          <p v-if="errors.email" :id="`${formId}-email-msg`" class="input-error-text" role="alert">{{ errors.email }}</p>
        </div>
        <div>
          <label :for="`${formId}-username`" class="input-label">{{ t('groupManagement.createUser.username') }}</label>
          <input
            :id="`${formId}-username`"
            v-model.trim="form.username"
            type="text"
            maxlength="100"
            autocomplete="off"
            class="input"
            :placeholder="t('groupManagement.createUser.usernamePlaceholder')"
          />
        </div>
      </div>

      <PasswordField
        :id="`${formId}-password`"
        v-model="form.password"
        :label="t('groupManagement.createUser.password')"
        :hint="t('groupManagement.createUser.passwordHint')"
        :error="errors.password"
      />

      <fieldset>
        <legend class="input-label">{{ t('groupManagement.createUser.limits') }}</legend>
        <div class="grid gap-4 sm:grid-cols-2">
          <div>
            <label :for="`${formId}-concurrency`" class="text-xs text-gray-500 dark:text-dark-400">{{ t('groupManagement.limit.concurrency') }}</label>
            <input
              :id="`${formId}-concurrency`"
              v-model.number="form.max_concurrent"
              type="number"
              inputmode="numeric"
              min="1"
              step="1"
              class="input mt-1"
              :class="errors.concurrency && 'input-error'"
              :aria-invalid="errors.concurrency ? 'true' : undefined"
            />
            <p v-if="errors.concurrency" class="input-error-text" role="alert">{{ errors.concurrency }}</p>
          </div>
          <div>
            <label :for="`${formId}-daily`" class="text-xs text-gray-500 dark:text-dark-400">{{ t('groupManagement.limit.dailyLimit') }}</label>
            <input
              :id="`${formId}-daily`"
              v-model.number="form.daily_limit"
              type="number"
              inputmode="numeric"
              min="0"
              step="1"
              class="input mt-1"
              :class="errors.daily && 'input-error'"
              :aria-invalid="errors.daily ? 'true' : undefined"
            />
            <p :class="errors.daily ? 'input-error-text' : 'input-hint'" :role="errors.daily ? 'alert' : undefined">
              {{ errors.daily || t('groupManagement.limit.dailyLimitHint') }}
            </p>
          </div>
        </div>
      </fieldset>

      <div v-if="canTransfer">
        <label :for="`${formId}-amount`" class="input-label">{{ t('groupManagement.createUser.initialAmount') }}</label>
        <div class="relative">
          <span class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-sm text-gray-400 dark:text-dark-400">$</span>
          <input
            :id="`${formId}-amount`"
            v-model.number="form.initial_amount"
            type="number"
            inputmode="decimal"
            min="0"
            step="0.01"
            class="input pl-7 tabular-nums"
            :class="errors.amount && 'input-error'"
            :aria-invalid="errors.amount ? 'true' : undefined"
            :aria-describedby="`${formId}-amount-msg`"
          />
        </div>
        <p :id="`${formId}-amount-msg`" :class="errors.amount ? 'input-error-text' : 'input-hint'" :role="errors.amount ? 'alert' : undefined">
          {{ errors.amount || t('groupManagement.createUser.initialAmountHint', { balance: formatCurrency(availableBalance) }) }}
        </p>
      </div>
    </form>

    <template #footer>
      <template v-if="created">
        <button type="button" class="btn btn-primary" @click="emit('close')">{{ t('groupManagement.createUser.done') }}</button>
      </template>
      <template v-else>
        <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
        <button type="submit" :form="formId" class="btn btn-primary" :disabled="saving">
          <span v-if="saving" class="spinner h-4 w-4" aria-hidden="true" />
          {{ t('groupManagement.createUser.submit') }}
        </button>
      </template>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import type { CreateGroupUserInput, MemberLimit } from '@/api/groupManagement'
import { formatCurrency } from '@/utils/format'
import CredentialsCard from './CredentialsCard.vue'
import PasswordField from './PasswordField.vue'
import { exceedsBalance, generatePassword, isValidEmail, isValidLimit, isValidPassword } from './helpers'

const props = defineProps<{
  show: boolean
  saving: boolean
  defaults: MemberLimit
  canTransfer: boolean
  availableBalance: number
  created: { email: string; password: string } | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'submit', input: CreateGroupUserInput & { password: string }): void
}>()

const { t } = useI18n()

const formId = 'group-create-user-form'
const emailRef = ref<HTMLInputElement | null>(null)
const attempted = ref(false)
const form = reactive({
  email: '',
  username: '',
  password: '',
  max_concurrent: 1 as number | '',
  daily_limit: 0 as number | '',
  initial_amount: 0 as number | ''
})

const errors = computed(() => {
  if (!attempted.value) return { email: '', password: '', concurrency: '', daily: '', amount: '' }
  const amount = form.initial_amount === '' ? 0 : form.initial_amount
  return {
    email: isValidEmail(form.email) ? '' : t('groupManagement.createUser.invalidEmail'),
    password: isValidPassword(form.password) ? '' : t('groupManagement.createUser.invalidPassword'),
    concurrency: isValidLimit(form.max_concurrent, 1) ? '' : t('groupManagement.limit.invalidConcurrency'),
    daily: isValidLimit(form.daily_limit, 0) ? '' : t('groupManagement.limit.invalidDaily'),
    amount: !props.canTransfer || (Number.isFinite(amount) && amount >= 0 && !exceedsBalance(amount, props.availableBalance))
      ? ''
      : t('groupManagement.createUser.invalidAmount')
  }
})
const hasErrors = computed(() => Object.values(errors.value).some(Boolean))

watch(() => props.show, open => {
  if (!open) return
  attempted.value = false
  Object.assign(form, {
    email: '',
    username: '',
    password: generatePassword(),
    max_concurrent: props.defaults.max_concurrent,
    daily_limit: props.defaults.daily_limit,
    initial_amount: 0
  })
  // BaseDialog 打开时先聚焦关闭按钮，这里把焦点移到第一个输入框
  window.setTimeout(() => emailRef.value?.focus(), 0)
}, { immediate: true })

function submit() {
  attempted.value = true
  if (hasErrors.value || props.saving) return
  const amount = form.initial_amount === '' ? 0 : form.initial_amount
  emit('submit', {
    email: form.email,
    username: form.username || undefined,
    password: form.password,
    max_concurrent: form.max_concurrent as number,
    daily_limit: form.daily_limit as number,
    ...(props.canTransfer && amount > 0 ? { initial_amount: amount } : {})
  })
}
</script>
