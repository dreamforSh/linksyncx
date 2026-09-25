<template>
  <BaseDialog :show="show" :title="title" width="narrow" @close="emit('close')">
    <form :id="formId" class="space-y-5" novalidate @submit.prevent="submit">
      <template v-if="usdLimits">
        <div class="grid gap-4 sm:grid-cols-2">
          <div>
            <label :for="`${formId}-5h`" class="input-label">{{ t('groupManagement.limit.limit5h') }}</label>
            <div class="relative">
              <span class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-sm text-gray-400 dark:text-dark-400">$</span>
              <input
                :id="`${formId}-5h`"
                v-model.number="form.limit_5h_usd"
                type="number"
                inputmode="decimal"
                min="0"
                step="0.01"
                class="input pl-7"
                :class="limit5hError && 'input-error'"
                :aria-invalid="limit5hError ? 'true' : undefined"
                :aria-describedby="`${formId}-5h-msg`"
              />
            </div>
            <p :id="`${formId}-5h-msg`" :class="limit5hError ? 'input-error-text' : 'input-hint'">
              {{ limit5hError || t('groupManagement.limit.usdHint', { value: usdLabel(defaults.limit_5h_usd) }) }}
            </p>
          </div>
          <div>
            <label :for="`${formId}-7d`" class="input-label">{{ t('groupManagement.limit.limit7d') }}</label>
            <div class="relative">
              <span class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-sm text-gray-400 dark:text-dark-400">$</span>
              <input
                :id="`${formId}-7d`"
                v-model.number="form.limit_7d_usd"
                type="number"
                inputmode="decimal"
                min="0"
                step="0.01"
                class="input pl-7"
                :class="limit7dError && 'input-error'"
                :aria-invalid="limit7dError ? 'true' : undefined"
                :aria-describedby="`${formId}-7d-msg`"
              />
            </div>
            <p :id="`${formId}-7d-msg`" :class="limit7dError ? 'input-error-text' : 'input-hint'">
              {{ limit7dError || t('groupManagement.limit.usdHint', { value: usdLabel(defaults.limit_7d_usd) }) }}
            </p>
          </div>
        </div>
        <div v-if="member" class="grid gap-4 rounded-xl bg-gray-50 px-4 py-3 dark:bg-dark-900/40 sm:grid-cols-2">
          <QuotaBar
            size="lg"
            :used="member.usage_5h_usd ?? 0"
            :limit="isValidUSDLimit(form.limit_5h_usd) ? form.limit_5h_usd : (member.limit_5h_usd ?? 0)"
            :format="formatUsdCompact"
            :label="t('groupManagement.members.usage5h')"
          />
          <QuotaBar
            size="lg"
            :used="member.usage_7d_usd ?? 0"
            :limit="isValidUSDLimit(form.limit_7d_usd) ? form.limit_7d_usd : (member.limit_7d_usd ?? 0)"
            :format="formatUsdCompact"
            :label="t('groupManagement.members.usage7d')"
          />
        </div>
      </template>

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
import { formatCurrency } from '@/utils/format'
import QuotaBar from './QuotaBar.vue'
import { formatUsdCompact, isValidLimit, isValidUSDLimit, memberDisplayName } from './helpers'

const props = withDefaults(defineProps<{
  show: boolean
  saving: boolean
  member: GroupManagementMember | null
  defaults: MemberLimit
  // 额度组：可设置组内 5h / 7d 美元上限
  usdLimits?: boolean
}>(), {
  usdLimits: false
})

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'submit', limit: MemberLimit): void
}>()

const { t } = useI18n()

const formId = 'group-member-limit-form'
const form = reactive<Required<MemberLimit>>({ max_concurrent: 1, daily_limit: 0, limit_5h_usd: 0, limit_7d_usd: 0 })

const title = computed(() => t('groupManagement.limit.title', { name: props.member ? memberDisplayName(props.member) : '' }))
const defaultDailyLabel = computed(() => props.defaults.daily_limit > 0 ? props.defaults.daily_limit : t('groupManagement.unlimited'))
const concurrencyError = computed(() =>
  isValidLimit(form.max_concurrent, 1) ? '' : t('groupManagement.limit.invalidConcurrency'))
const dailyError = computed(() =>
  isValidLimit(form.daily_limit, 0) ? '' : t('groupManagement.limit.invalidDaily'))
const limit5hError = computed(() =>
  !props.usdLimits || isValidUSDLimit(form.limit_5h_usd) ? '' : t('groupManagement.limit.invalidUsd'))
const limit7dError = computed(() =>
  !props.usdLimits || isValidUSDLimit(form.limit_7d_usd) ? '' : t('groupManagement.limit.invalidUsd'))
const valid = computed(() => !concurrencyError.value && !dailyError.value && !limit5hError.value && !limit7dError.value)
const changed = computed(() => {
  const member = props.member
  if (!member) return false
  if (form.max_concurrent !== member.max_concurrent || form.daily_limit !== member.daily_limit) return true
  return props.usdLimits &&
    (form.limit_5h_usd !== (member.limit_5h_usd ?? 0) || form.limit_7d_usd !== (member.limit_7d_usd ?? 0))
})

watch(() => [props.show, props.member] as const, ([open, member]) => {
  if (open && member) {
    form.max_concurrent = member.max_concurrent
    form.daily_limit = member.daily_limit
    form.limit_5h_usd = member.limit_5h_usd ?? 0
    form.limit_7d_usd = member.limit_7d_usd ?? 0
  }
}, { immediate: true })

function usdLabel(value?: number) {
  return value && value > 0 ? formatCurrency(value) : t('groupManagement.unlimited')
}

function submit() {
  if (!valid.value || !changed.value || props.saving) return
  const limit: MemberLimit = { max_concurrent: form.max_concurrent, daily_limit: form.daily_limit }
  if (props.usdLimits) {
    limit.limit_5h_usd = form.limit_5h_usd
    limit.limit_7d_usd = form.limit_7d_usd
  }
  emit('submit', limit)
}
</script>
