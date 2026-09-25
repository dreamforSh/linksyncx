<template>
  <form class="card overflow-hidden" novalidate @submit.prevent="submit">
    <div class="flex items-start justify-between gap-6 px-5 py-5 sm:px-6">
      <div class="min-w-0">
        <h3 :id="`${idPrefix}-enabled`" class="text-sm font-semibold text-gray-900 dark:text-white">
          {{ t('groupManagement.settings.enabled') }}
        </h3>
        <p :id="`${idPrefix}-enabled-hint`" class="mt-1 max-w-2xl text-sm leading-relaxed text-gray-500 dark:text-dark-400">
          {{ settings.managed ? t('groupManagement.settings.managedLocked') : t('groupManagement.settings.enabledHint') }}
        </p>
      </div>
      <Toggle
        v-model="draft.enabled"
        class="mt-0.5 disabled:cursor-not-allowed disabled:opacity-60"
        :disabled="settings.managed"
        :aria-labelledby="`${idPrefix}-enabled`"
        :aria-describedby="`${idPrefix}-enabled-hint`"
      />
    </div>

    <div class="border-t border-gray-100 px-5 py-5 dark:border-dark-700 sm:px-6">
      <fieldset :disabled="modeLocked">
        <legend class="text-sm font-semibold text-gray-900 dark:text-white">
          {{ t('groupManagement.settings.mode') }}
        </legend>
        <p v-if="modeLocked" class="mt-1 max-w-2xl text-sm leading-relaxed text-gray-500 dark:text-dark-400" data-testid="settings-mode-locked">
          {{ t('groupManagement.settings.modeLockedByType', { type: managedTypeLabel }) }}
        </p>
        <div class="mt-3 grid gap-3 sm:grid-cols-2">
          <label
            v-for="option in modeOptions"
            :key="option.value"
            class="flex items-start gap-3 rounded-xl border p-4 transition-colors duration-150 focus-within:ring-2 focus-within:ring-primary-500/40"
            :class="[
              draft.allocation_mode === option.value
                ? 'border-primary-500 bg-primary-50/60 dark:border-primary-500/70 dark:bg-primary-900/15'
                : 'border-gray-200 dark:border-dark-600',
              modeLocked
                ? (draft.allocation_mode === option.value ? 'cursor-default' : 'cursor-not-allowed opacity-50')
                : (draft.allocation_mode === option.value ? 'cursor-pointer' : 'cursor-pointer hover:border-gray-300 dark:hover:border-dark-500')
            ]"
          >
            <input
              v-model="draft.allocation_mode"
              type="radio"
              :name="`${idPrefix}-mode`"
              :value="option.value"
              class="sr-only"
            />
            <span
              class="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg transition-colors duration-150"
              :class="draft.allocation_mode === option.value
                ? 'bg-primary-500 text-white'
                : 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-dark-300'"
              aria-hidden="true"
            >
              <Icon :name="option.icon" size="sm" />
            </span>
            <span class="min-w-0 flex-1">
              <span class="block text-sm font-medium text-gray-900 dark:text-white">{{ option.label }}</span>
              <span class="mt-0.5 block text-xs leading-relaxed text-gray-500 dark:text-dark-400">{{ option.description }}</span>
            </span>
            <Icon
              v-if="draft.allocation_mode === option.value"
              name="checkCircle"
              size="md"
              class="shrink-0 text-primary-500"
            />
          </label>
        </div>
      </fieldset>
      <p
        v-if="modeWarning"
        class="mt-3 flex items-start gap-2 rounded-lg bg-amber-50 px-3 py-2 text-xs leading-relaxed text-amber-800 dark:bg-amber-500/10 dark:text-amber-200"
      >
        <Icon name="exclamationTriangle" size="sm" class="shrink-0" />
        <span>{{ modeWarning }}</span>
      </p>
    </div>

    <div class="border-t border-gray-100 px-5 py-5 dark:border-dark-700 sm:px-6">
      <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('groupManagement.settings.defaults') }}</h3>
      <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('groupManagement.settings.defaultsHint') }}</p>
      <div class="mt-4 grid gap-4 sm:max-w-xl sm:grid-cols-2">
        <template v-if="quotaGroup">
          <div>
            <label :for="`${idPrefix}-limit-5h`" class="input-label">{{ t('groupManagement.settings.defaultLimit5h') }}</label>
            <div class="relative">
              <span class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-sm text-gray-400 dark:text-dark-400">$</span>
              <input
                :id="`${idPrefix}-limit-5h`"
                v-model.number="draft.default_limit_5h_usd"
                type="number"
                inputmode="decimal"
                min="0"
                step="0.01"
                class="input pl-7"
                :class="limit5hError && 'input-error'"
                :aria-invalid="limit5hError ? 'true' : undefined"
                :aria-describedby="`${idPrefix}-limit-5h-msg`"
              />
            </div>
            <p :id="`${idPrefix}-limit-5h-msg`" :class="limit5hError ? 'input-error-text' : 'input-hint'">
              {{ limit5hError || t('groupManagement.settings.usdLimitHint') }}
            </p>
          </div>
          <div>
            <label :for="`${idPrefix}-limit-7d`" class="input-label">{{ t('groupManagement.settings.defaultLimit7d') }}</label>
            <div class="relative">
              <span class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-sm text-gray-400 dark:text-dark-400">$</span>
              <input
                :id="`${idPrefix}-limit-7d`"
                v-model.number="draft.default_limit_7d_usd"
                type="number"
                inputmode="decimal"
                min="0"
                step="0.01"
                class="input pl-7"
                :class="limit7dError && 'input-error'"
                :aria-invalid="limit7dError ? 'true' : undefined"
                :aria-describedby="`${idPrefix}-limit-7d-msg`"
              />
            </div>
            <p :id="`${idPrefix}-limit-7d-msg`" :class="limit7dError ? 'input-error-text' : 'input-hint'">
              {{ limit7dError || t('groupManagement.settings.usdLimitHint') }}
            </p>
          </div>
        </template>
        <div>
          <label :for="`${idPrefix}-concurrency`" class="input-label">{{ t('groupManagement.settings.maxConcurrent') }}</label>
          <input
            :id="`${idPrefix}-concurrency`"
            v-model.number="draft.max_concurrent"
            type="number"
            inputmode="numeric"
            min="1"
            step="1"
            class="input"
            :class="concurrencyError && 'input-error'"
            :aria-invalid="concurrencyError ? 'true' : undefined"
            :aria-describedby="`${idPrefix}-concurrency-msg`"
          />
          <p :id="`${idPrefix}-concurrency-msg`" :class="concurrencyError ? 'input-error-text' : 'input-hint'">
            {{ concurrencyError || t('groupManagement.settings.maxConcurrentHint') }}
          </p>
        </div>
        <div>
          <label :for="`${idPrefix}-daily`" class="input-label">{{ t('groupManagement.settings.dailyLimit') }}</label>
          <input
            :id="`${idPrefix}-daily`"
            v-model.number="draft.daily_limit"
            type="number"
            inputmode="numeric"
            min="0"
            step="1"
            class="input"
            :class="dailyError && 'input-error'"
            :aria-invalid="dailyError ? 'true' : undefined"
            :aria-describedby="`${idPrefix}-daily-msg`"
          />
          <p :id="`${idPrefix}-daily-msg`" :class="dailyError ? 'input-error-text' : 'input-hint'">
            {{ dailyError || t('groupManagement.settings.dailyLimitHint') }}
          </p>
        </div>
      </div>
    </div>

    <div
      class="flex flex-wrap items-center gap-3 border-t border-gray-100 bg-gray-50/60 px-5 py-3 dark:border-dark-700 dark:bg-dark-900/30 sm:px-6"
    >
      <p
        class="flex items-center gap-2 text-xs"
        :class="dirty ? 'text-amber-700 dark:text-amber-300' : 'text-gray-500 dark:text-dark-400'"
        aria-live="polite"
      >
        <span
          class="h-1.5 w-1.5 rounded-full"
          :class="dirty ? 'bg-amber-500' : 'bg-gray-300 dark:bg-dark-500'"
          aria-hidden="true"
        />
        {{ dirty ? t('groupManagement.settings.unsaved') : t('groupManagement.settings.saved') }}
      </p>
      <div class="ml-auto flex gap-2">
        <button type="button" class="btn btn-secondary btn-md" :disabled="!dirty || saving" @click="reset">
          {{ t('common.reset') }}
        </button>
        <button type="submit" class="btn btn-primary btn-md" :disabled="!dirty || !valid || saving">
          <span v-if="saving" class="spinner h-4 w-4" aria-hidden="true" />
          {{ saving ? t('common.saving') : t('groupManagement.settings.save') }}
        </button>
      </div>
    </div>
  </form>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import Toggle from '@/components/common/Toggle.vue'
import type { AllocationMode, GroupManagementSettings } from '@/api/groupManagement'
import { isValidLimit, isValidUSDLimit } from './helpers'

const props = defineProps<{
  groupId: number
  settings: GroupManagementSettings
  saving: boolean
}>()

const emit = defineEmits<{
  (e: 'save', settings: GroupManagementSettings): void
}>()

const { t } = useI18n()

const BASE_FIELDS = ['enabled', 'allocation_mode', 'max_concurrent', 'daily_limit'] as const
const USD_FIELDS = ['default_limit_5h_usd', 'default_limit_7d_usd'] as const

const idPrefix = computed(() => `group-settings-${props.groupId}`)
const baseline = ref<GroupManagementSettings>(withDefaults(props.settings))
const draft = reactive<GroupManagementSettings>(withDefaults(props.settings))
let baselineGroupId = props.groupId

// 管理分组的分配方式由类型决定（额度组自动、订阅组手动），不能在这里切换
const modeLocked = computed(() => Boolean(props.settings.managed))
const quotaGroup = computed(() => Boolean(props.settings.managed) && props.settings.managed_type === 'quota')
const managedTypeLabel = computed(() => props.settings.managed_type === 'quota'
  ? t('groupManagement.type.quota')
  : t('groupManagement.type.subscription'))

const fields = computed(() => quotaGroup.value ? [...BASE_FIELDS, ...USD_FIELDS] : [...BASE_FIELDS])
const dirty = computed(() => fields.value.some(field => draft[field] !== baseline.value[field]))

function withDefaults(settings: GroupManagementSettings): GroupManagementSettings {
  return { ...settings, default_limit_5h_usd: settings.default_limit_5h_usd ?? 0, default_limit_7d_usd: settings.default_limit_7d_usd ?? 0 }
}

// Background refreshes (after member or assignment changes) must not wipe an
// unsaved draft; switching to another group always starts from its settings.
watch(
  () => [props.groupId, props.settings] as const,
  ([groupId, next]) => {
    const keepDraft = groupId === baselineGroupId && dirty.value
    baseline.value = withDefaults(next)
    baselineGroupId = groupId
    if (!keepDraft) Object.assign(draft, withDefaults(next))
  }
)

const modeOptions = computed<Array<{ value: AllocationMode; label: string; description: string; icon: 'sync' | 'link' }>>(() => [
  { value: 'auto', label: t('groupManagement.settings.auto'), description: t('groupManagement.settings.autoDesc'), icon: 'sync' },
  { value: 'manual', label: t('groupManagement.settings.manual'), description: t('groupManagement.settings.manualDesc'), icon: 'link' }
])

const modeWarning = computed(() => {
  if (modeLocked.value || draft.allocation_mode === baseline.value.allocation_mode) return ''
  return draft.allocation_mode === 'auto'
    ? t('groupManagement.settings.toAutoWarning')
    : t('groupManagement.settings.toManualWarning')
})

const concurrencyError = computed(() =>
  isValidLimit(draft.max_concurrent, 1) ? '' : t('groupManagement.settings.invalidConcurrency'))
const dailyError = computed(() =>
  isValidLimit(draft.daily_limit, 0) ? '' : t('groupManagement.settings.invalidDaily'))
const limit5hError = computed(() =>
  !quotaGroup.value || isValidUSDLimit(draft.default_limit_5h_usd) ? '' : t('groupManagement.settings.invalidUsd'))
const limit7dError = computed(() =>
  !quotaGroup.value || isValidUSDLimit(draft.default_limit_7d_usd) ? '' : t('groupManagement.settings.invalidUsd'))
const valid = computed(() => !concurrencyError.value && !dailyError.value && !limit5hError.value && !limit7dError.value)

function reset() {
  Object.assign(draft, baseline.value)
}

function submit() {
  if (!dirty.value || !valid.value || props.saving) return
  const next: GroupManagementSettings = {
    enabled: draft.enabled,
    allocation_mode: draft.allocation_mode,
    max_concurrent: draft.max_concurrent,
    daily_limit: draft.daily_limit
  }
  // 默认美元上限只对额度组生效；其他分组省略，后端保留原值
  if (quotaGroup.value) {
    next.default_limit_5h_usd = draft.default_limit_5h_usd
    next.default_limit_7d_usd = draft.default_limit_7d_usd
  }
  emit('save', next)
}
</script>
