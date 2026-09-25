<template>
  <BaseDialog :show="show" :title="title" width="narrow" @close="emit('close')">
    <form :id="formId" class="space-y-5" novalidate @submit.prevent="submit">
      <div>
        <label :for="`${formId}-concurrency`" class="input-label">{{ t('groupManagement.limit.concurrency') }}</label>
        <input
          :id="`${formId}-concurrency`"
          v-model.number="form.max_concurrent"
          type="number"
          inputmode="numeric"
          min="1"
          step="1"
          class="input"
          :class="concurrencyError && 'input-error'"
          :aria-invalid="concurrencyError ? 'true' : undefined"
          :aria-describedby="`${formId}-concurrency-msg`"
        />
        <p :id="`${formId}-concurrency-msg`" :class="concurrencyError ? 'input-error-text' : 'input-hint'">
          {{ concurrencyError || `${t('groupManagement.limit.concurrencyHint')} · ${t('groupManagement.limit.groupDefault', { value: defaults.max_concurrent })}` }}
        </p>
      </div>
      <div>
        <label :for="`${formId}-daily`" class="input-label">{{ t('groupManagement.limit.dailyLimit') }}</label>
        <input
          :id="`${formId}-daily`"
          v-model.number="form.daily_limit"
          type="number"
          inputmode="numeric"
          min="0"
          step="1"
          class="input"
          :class="dailyError && 'input-error'"
          :aria-invalid="dailyError ? 'true' : undefined"
          :aria-describedby="`${formId}-daily-msg`"
        />
        <p :id="`${formId}-daily-msg`" :class="dailyError ? 'input-error-text' : 'input-hint'">
          {{ dailyError || `${t('groupManagement.limit.dailyLimitHint')} · ${t('groupManagement.limit.groupDefault', { value: defaultDailyLabel })}` }}
        </p>
      </div>
      <div v-if="member" class="rounded-xl bg-gray-50 px-4 py-3 dark:bg-dark-900/40">
        <QuotaBar
          size="lg"
          :used="member.daily_used"
          :limit="isValidLimit(form.daily_limit, 0) ? form.daily_limit : member.daily_limit"
          :label="t('groupManagement.members.todayUsage')"
        />
      </div>
    </form>

    <template #footer>
      <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
      <button type="submit" :form="formId" class="btn btn-primary" :disabled="saving || !valid || !changed">
        <span v-if="saving" class="spinner h-4 w-4" aria-hidden="true" />
        {{ saving ? t('common.saving') : t('common.save') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import type { GroupManagementMember, MemberLimit } from '@/api/groupManagement'
import QuotaBar from './QuotaBar.vue'
import { isValidLimit, memberDisplayName } from './helpers'

const props = defineProps<{
  show: boolean
  saving: boolean
  member: GroupManagementMember | null
  defaults: MemberLimit
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'submit', limit: MemberLimit): void
}>()

const { t } = useI18n()

const formId = 'group-member-limit-form'
const form = reactive<MemberLimit>({ max_concurrent: 1, daily_limit: 0 })

const title = computed(() => t('groupManagement.limit.title', { name: props.member ? memberDisplayName(props.member) : '' }))
const defaultDailyLabel = computed(() => props.defaults.daily_limit > 0 ? props.defaults.daily_limit : t('groupManagement.unlimited'))
const concurrencyError = computed(() =>
  isValidLimit(form.max_concurrent, 1) ? '' : t('groupManagement.limit.invalidConcurrency'))
const dailyError = computed(() =>
  isValidLimit(form.daily_limit, 0) ? '' : t('groupManagement.limit.invalidDaily'))
const valid = computed(() => !concurrencyError.value && !dailyError.value)
const changed = computed(() => !!props.member &&
  (form.max_concurrent !== props.member.max_concurrent || form.daily_limit !== props.member.daily_limit))

watch(() => [props.show, props.member] as const, ([open, member]) => {
  if (open && member) {
    form.max_concurrent = member.max_concurrent
    form.daily_limit = member.daily_limit
  }
}, { immediate: true })

function submit() {
  if (!valid.value || !changed.value || props.saving) return
  emit('submit', { max_concurrent: form.max_concurrent, daily_limit: form.daily_limit })
}
</script>
