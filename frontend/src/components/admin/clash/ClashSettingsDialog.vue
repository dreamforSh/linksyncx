<template>
  <BaseDialog :show="show" :title="t('admin.clash.settings.title')" width="wide" @close="handleClose">
    <div v-if="loading && !form" class="space-y-3" aria-busy="true">
      <div v-for="index in 4" :key="index" class="skeleton h-16 rounded-xl" />
    </div>

    <div
      v-else-if="loadError && !form"
      role="alert"
      class="rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-500/10 dark:text-red-300"
    >
      {{ loadError }}
      <button type="button" class="btn btn-secondary btn-sm ml-3" @click="load">{{ t('common.tryAgain') }}</button>
    </div>

    <form v-else-if="form" id="clash-settings-form" class="space-y-6" novalidate @submit.prevent="save">
      <section v-for="section in sections" :key="section.key" :aria-labelledby="`clash-settings-${section.key}`">
        <h3 :id="`clash-settings-${section.key}`" class="text-sm font-semibold text-gray-900 dark:text-white">
          {{ sectionTitle(section.key) }}
        </h3>
        <div class="mt-3 grid grid-cols-1 gap-4 md:grid-cols-2">
          <div
            v-for="field in section.fields"
            :key="field.key"
            :class="field.wide && 'md:col-span-2'"
          >
            <template v-if="field.type === 'toggle'">
              <div class="flex items-start justify-between gap-3 rounded-xl bg-gray-50 px-4 py-3 dark:bg-dark-800/50">
                <div>
                  <p class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ fieldLabel(field.key) }}</p>
                  <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">{{ fieldHint(field.key) }}</p>
                </div>
                <Toggle
                  :model-value="Boolean(form[field.key])"
                  :aria-label="fieldLabel(field.key)"
                  :data-testid="`clash-setting-${field.key}`"
                  @update:model-value="(value: boolean) => setField(field.key, value)"
                />
              </div>
            </template>
            <template v-else-if="field.type === 'select'">
              <label class="input-label">{{ fieldLabel(field.key) }}</label>
              <Select
                :model-value="String(form[field.key])"
                :options="exitPolicyOptions"
                :aria-label="fieldLabel(field.key)"
                @update:model-value="(value) => setField(field.key, String(value))"
              />
              <p class="input-hint">{{ fieldHint(field.key) }}</p>
            </template>
            <template v-else>
              <label class="input-label flex items-baseline justify-between gap-2" :for="`clash-setting-${field.key}`">
                <span>{{ fieldLabel(field.key) }}</span>
                <span v-if="field.type === 'number'" class="text-xs font-normal text-gray-400 dark:text-dark-500">
                  {{ t('admin.clash.settings.range', { min: field.min, max: field.max }) }}
                </span>
              </label>
              <input
                :id="`clash-setting-${field.key}`"
                :value="form[field.key]"
                :type="field.type === 'number' ? 'number' : 'text'"
                :min="field.min"
                :max="field.max"
                :maxlength="field.type === 'text' ? 200 : undefined"
                :class="['input', field.type === 'text' && 'font-mono text-sm', errors[field.key] && 'input-error']"
                :data-testid="`clash-setting-${field.key}`"
                @input="onInput(field, $event)"
              />
              <p v-if="errors[field.key]" class="input-error-text">{{ errors[field.key] }}</p>
              <p v-else class="input-hint">{{ fieldHint(field.key) }}</p>
            </template>
          </div>
        </div>
      </section>

      <p v-if="saveError" role="alert" class="rounded-lg bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-500/10 dark:text-red-300">
        {{ saveError }}
      </p>
    </form>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="saving" @click="handleClose">{{ t('common.cancel') }}</button>
        <button
          type="submit"
          form="clash-settings-form"
          class="btn btn-primary"
          :disabled="saving || !form"
          data-testid="clash-settings-save"
        >
          {{ saving ? t('common.saving') : t('common.save') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'
import type { ClashPoolSettings } from '@/types'
import { extractApiErrorMessage } from '@/utils/apiError'
import { clashErrorMessage } from '@/utils/clash'

type SettingKey = keyof ClashPoolSettings
type SectionKey = 'binding' | 'health' | 'exit' | 'subscription'

interface FieldDef {
  key: SettingKey
  type: 'number' | 'text' | 'toggle' | 'select'
  min?: number
  max?: number
  wide?: boolean
}

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'saved', settings: ClashPoolSettings): void
}>()

const { t } = useI18n()
const appStore = useAppStore()

/** Ranges mirror the server-side validation (validateClashPoolSettings). */
const sections: Array<{ key: SectionKey; fields: FieldDef[] }> = [
  {
    key: 'binding',
    fields: [
      { key: 'max_accounts_per_exit', type: 'number', min: 1, max: 100 },
      { key: 'exit_change_policy', type: 'select' },
      { key: 'allow_unprobed_exit_binding', type: 'toggle', wide: true }
    ]
  },
  {
    key: 'health',
    fields: [
      { key: 'automatic_probes_enabled', type: 'toggle', wide: true },
      { key: 'health_test_url', type: 'text', wide: true },
      { key: 'health_timeout_ms', type: 'number', min: 1000, max: 30000 },
      { key: 'pause_ttl_minutes', type: 'number', min: 15, max: 240 },
      { key: 'bound_check_interval_seconds', type: 'number', min: 30, max: 3600 },
      { key: 'unbound_check_interval_seconds', type: 'number', min: 60, max: 86400 },
      { key: 'failure_threshold', type: 'number', min: 1, max: 20 },
      { key: 'recovery_threshold', type: 'number', min: 1, max: 20 }
    ]
  },
  {
    key: 'exit',
    fields: [
      { key: 'exit_probe_interval_minutes', type: 'number', min: 30, max: 10080 },
      { key: 'exit_probe_per_minute', type: 'number', min: 1, max: 600 },
      { key: 'platform_checks_enabled', type: 'toggle', wide: true }
    ]
  },
  {
    key: 'subscription',
    fields: [
      { key: 'default_user_agent', type: 'text', wide: true },
      { key: 'drop_protection_percent', type: 'number', min: 0, max: 100 },
      { key: 'missing_retention_days', type: 'number', min: 1, max: 365 }
    ]
  }
]

const exitPolicyOptions = computed(() => [
  { value: 'pause', label: t('admin.clash.settings.exitPolicy.pause') },
  { value: 'accept', label: t('admin.clash.settings.exitPolicy.accept') }
])

const form = ref<ClashPoolSettings | null>(null)
const errors = reactive<Partial<Record<SettingKey, string>>>({})
const loading = ref(false)
const loadError = ref('')
const saving = ref(false)
const saveError = ref('')

const sectionTitle = (key: SectionKey) => t(`admin.clash.settings.sections.${key}`)
const fieldLabel = (key: SettingKey) => t(`admin.clash.settings.fields.${key}.label`)
const fieldHint = (key: SettingKey) => t(`admin.clash.settings.fields.${key}.hint`)

function setField(key: SettingKey, value: string | number | boolean) {
  if (!form.value) return
  ;(form.value as unknown as Record<string, unknown>)[key] = value
  delete errors[key]
}

function onInput(field: FieldDef, event: Event) {
  const raw = (event.target as HTMLInputElement).value
  if (field.type === 'number') {
    setField(field.key, raw === '' ? Number.NaN : Number(raw))
  } else {
    setField(field.key, raw)
  }
}

function validate(settings: ClashPoolSettings): boolean {
  for (const key of Object.keys(errors) as SettingKey[]) delete errors[key]
  for (const section of sections) {
    for (const field of section.fields) {
      if (field.type !== 'number') continue
      const value = settings[field.key] as number
      if (!Number.isInteger(value) || value < (field.min ?? 0) || value > (field.max ?? Number.MAX_SAFE_INTEGER)) {
        errors[field.key] = t('admin.clash.settings.rangeError', { min: field.min, max: field.max })
      }
    }
  }
  if (!/^https?:\/\/[^\s/]+/i.test(settings.health_test_url.trim())) {
    errors.health_test_url = t('admin.clash.settings.urlError')
  }
  return Object.keys(errors).length === 0
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const settings = await adminAPI.clash.getSettings()
    form.value = { ...settings, automatic_probes_enabled: settings.automatic_probes_enabled ?? true }
  } catch (error) {
    loadError.value = clashErrorMessage(error, t) ?? extractApiErrorMessage(error, t('admin.clash.settings.loadFailed'))
  } finally {
    loading.value = false
  }
}

async function save() {
  const settings = form.value
  if (!settings || saving.value || !validate(settings)) return
  saving.value = true
  saveError.value = ''
  try {
    const payload: ClashPoolSettings = {
      ...settings,
      health_test_url: settings.health_test_url.trim(),
      default_user_agent: settings.default_user_agent.trim()
    }
    const saved = await adminAPI.clash.updateSettings(payload)
    appStore.showSuccess(t('admin.clash.settings.saved'))
    emit('saved', saved)
    emit('close')
  } catch (error) {
    saveError.value = clashErrorMessage(error, t) ?? extractApiErrorMessage(error, t('admin.clash.settings.saveFailed'))
    appStore.showError(saveError.value)
  } finally {
    saving.value = false
  }
}

function handleClose() {
  if (saving.value) return
  emit('close')
}

watch(
  () => props.show,
  (visible) => {
    if (!visible) return
    form.value = null
    saveError.value = ''
    for (const key of Object.keys(errors) as SettingKey[]) delete errors[key]
    void load()
  },
  { immediate: true }
)
</script>
