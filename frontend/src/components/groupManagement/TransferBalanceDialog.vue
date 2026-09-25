<template>
  <BaseDialog :show="show" :title="title" width="narrow" @close="emit('close')">
    <form :id="formId" class="space-y-5" novalidate @submit.prevent="submit">
      <div role="radiogroup" :aria-label="t('groupManagement.transfer.direction')" class="grid grid-cols-2 gap-1 rounded-lg bg-gray-100 p-1 dark:bg-dark-700">
        <button
          v-for="option in directions"
          :key="option.value"
          type="button"
          role="radio"
          :aria-checked="direction === option.value"
          :data-testid="`transfer-direction-${option.value}`"
          class="flex items-center justify-center gap-1.5 rounded-md px-3 py-2 text-sm font-medium transition-colors duration-150 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/50"
          :class="direction === option.value
            ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-600 dark:text-white'
            : 'text-gray-600 hover:text-gray-900 dark:text-dark-300 dark:hover:text-white'"
          @click="direction = option.value"
        >
          <Icon :name="option.icon" size="sm" />
          {{ option.label }}
        </button>
      </div>

      <dl class="grid grid-cols-2 gap-3">
        <div class="rounded-xl bg-gray-50 px-4 py-3 dark:bg-dark-900/40">
          <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('groupManagement.transfer.yourBalance') }}</dt>
          <dd class="mt-1 text-lg font-semibold tabular-nums text-gray-900 dark:text-white">{{ formatCurrency(availableBalance) }}</dd>
        </div>
        <div class="rounded-xl bg-gray-50 px-4 py-3 dark:bg-dark-900/40">
          <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('groupManagement.transfer.memberBalance') }}</dt>
          <dd class="mt-1 text-lg font-semibold tabular-nums text-gray-900 dark:text-white">{{ formatCurrency(memberBalance) }}</dd>
        </div>
      </dl>

      <div>
        <label :for="`${formId}-amount`" class="input-label">{{ t('groupManagement.transfer.amount') }}</label>
        <div class="relative">
          <span class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-sm text-gray-400 dark:text-dark-400">$</span>
          <input
            :id="`${formId}-amount`"
            ref="amountRef"
            v-model.number="amount"
            type="number"
            inputmode="decimal"
            min="0.01"
            step="0.01"
            class="input pl-7 tabular-nums"
            :class="amountError && 'input-error'"
            :aria-invalid="amountError ? 'true' : undefined"
            :aria-describedby="`${formId}-amount-msg`"
          />
        </div>
        <p :id="`${formId}-amount-msg`" :class="amountError ? 'input-error-text' : 'input-hint'" :role="amountError ? 'alert' : undefined">
          {{ amountError || preview }}
        </p>
      </div>

      <div>
        <label :for="`${formId}-notes`" class="input-label">{{ t('groupManagement.transfer.notes') }}</label>
        <input
          :id="`${formId}-notes`"
          v-model.trim="notes"
          type="text"
          maxlength="500"
          class="input"
          :placeholder="t('groupManagement.transfer.notesPlaceholder')"
        />
      </div>
    </form>

    <template #footer>
      <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
      <button type="submit" :form="formId" class="btn btn-primary" :disabled="saving">
        <span v-if="saving" class="spinner h-4 w-4" aria-hidden="true" />
        {{ direction === 'grant' ? t('groupManagement.transfer.submitGrant') : t('groupManagement.transfer.submitReclaim') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import type { GroupManagementMember, GroupTransferDirection } from '@/api/groupManagement'
import { formatCurrency } from '@/utils/format'
import { exceedsBalance, memberDisplayName } from './helpers'

const props = defineProps<{
  show: boolean
  saving: boolean
  member: GroupManagementMember | null
  availableBalance: number
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'submit', payload: { direction: GroupTransferDirection; amount: number; notes: string }): void
}>()

const { t } = useI18n()

const formId = 'group-transfer-form'
const amountRef = ref<HTMLInputElement | null>(null)
const direction = ref<GroupTransferDirection>('grant')
const amount = ref<number | ''>('')
const notes = ref('')
const attempted = ref(false)

const title = computed(() => t('groupManagement.transfer.title', { name: props.member ? memberDisplayName(props.member) : '' }))
const memberBalance = computed(() => props.member?.balance ?? 0)
// 只能收回本分组划拨给 TA 的净额，TA 自己充值的余额不能动
const reclaimable = computed(() => Math.min(props.member?.reclaimable ?? 0, memberBalance.value))
const directions = computed(() => [
  { value: 'grant' as const, icon: 'arrowRight' as const, label: t('groupManagement.transfer.grant') },
  { value: 'reclaim' as const, icon: 'arrowLeft' as const, label: t('groupManagement.transfer.reclaim') }
])

const amountError = computed(() => {
  if (!attempted.value && amount.value === '') return ''
  const value = amount.value
  if (typeof value !== 'number' || !Number.isFinite(value) || value <= 0) {
    return attempted.value ? t('groupManagement.transfer.invalidAmount') : ''
  }
  if (direction.value === 'grant' && exceedsBalance(value, props.availableBalance)) return t('groupManagement.transfer.exceedsMine')
  if (direction.value === 'reclaim' && exceedsBalance(value, reclaimable.value)) return t('groupManagement.transfer.exceedsReclaimable')
  return ''
})

const preview = computed(() => {
  const value = typeof amount.value === 'number' && amount.value > 0 ? amount.value : 0
  const delta = direction.value === 'grant' ? value : -value
  const after = t('groupManagement.transfer.after', {
    mine: formatCurrency(props.availableBalance - delta),
    theirs: formatCurrency(memberBalance.value + delta)
  })
  if (direction.value !== 'reclaim') return after
  return `${t('groupManagement.transfer.reclaimable', { amount: formatCurrency(reclaimable.value) })} ${after}`
})

watch(() => props.show, open => {
  if (!open) return
  direction.value = 'grant'
  amount.value = ''
  notes.value = ''
  attempted.value = false
  window.setTimeout(() => amountRef.value?.focus(), 0)
}, { immediate: true })

function submit() {
  attempted.value = true
  if (amountError.value || props.saving || typeof amount.value !== 'number') return
  emit('submit', { direction: direction.value, amount: amount.value, notes: notes.value })
}
</script>
