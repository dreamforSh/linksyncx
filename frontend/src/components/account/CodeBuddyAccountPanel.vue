<template>
  <div class="space-y-4 border-t border-gray-200 pt-4 dark:border-dark-600" data-testid="codebuddy-account-panel">
    <div class="flex flex-wrap items-center gap-2 text-sm text-gray-600 dark:text-gray-300">
      <span class="font-medium">{{ t('admin.accounts.codebuddyOAuth.title') }}</span>
      <span v-if="profile" class="rounded bg-violet-100 px-1.5 py-0.5 text-xs text-violet-700 dark:bg-violet-900/30 dark:text-violet-300">
        {{ t(`admin.accounts.codebuddyOAuth.profiles.${profile}`) }}
      </span>
      <span v-if="uid" class="font-mono text-xs text-gray-500">uid {{ uid }}</span>
    </div>

    <div>
      <label class="input-label">{{ t('admin.accounts.codebuddyOAuth.creditPolicy') }}</label>
      <div class="flex gap-2">
        <button
          v-for="option in creditPolicyOptions"
          :key="option.value"
          type="button"
          :class="[
            'flex-1 rounded-lg border px-3 py-2 text-sm transition-colors',
            creditPolicy === option.value
              ? 'border-violet-500 bg-violet-50 text-violet-700 dark:bg-violet-900/20 dark:text-violet-300'
              : 'border-gray-200 text-gray-700 dark:border-dark-600 dark:text-gray-300'
          ]"
          @click="creditPolicy = option.value"
        >
          {{ option.label }}
        </button>
      </div>
      <p class="input-hint">{{ t('admin.accounts.codebuddyOAuth.creditPolicyHint') }}</p>
    </div>

    <div>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="syncing" data-testid="codebuddy-sync-models" @click="syncModels">
        {{ t('admin.accounts.codebuddyOAuth.syncModels') }}
      </button>
    </div>

    <div class="rounded-lg border border-gray-200 p-3 dark:border-dark-600">
      <button type="button" class="text-sm font-medium text-violet-600 dark:text-violet-400" @click="showReauth = !showReauth">
        {{ t('admin.accounts.codebuddyOAuth.reauth') }}
      </button>
      <p class="input-hint">{{ t('admin.accounts.codebuddyOAuth.reauthHint') }}</p>
      <div v-if="showReauth" class="mt-3 space-y-3">
        <CodeBuddyCredentialFields ref="fieldsRef" :proxy-id="account.proxy_id ?? null" :show-credit-policy="false" />
        <button
          type="button"
          class="btn btn-primary btn-sm"
          :disabled="reauthing"
          data-testid="codebuddy-reauth-submit"
          @click="reauth"
        >
          {{ t('admin.accounts.codebuddyOAuth.reauth') }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { CodeBuddyCreditPolicy } from '@/api/admin/codebuddy'
import type { Account } from '@/types'
import { extractApiErrorMessage } from '@/utils/apiError'
import CodeBuddyCredentialFields from './CodeBuddyCredentialFields.vue'

const props = defineProps<{ account: Account }>()
const emit = defineEmits<{ reauthorized: [account: Account] }>()
const creditPolicy = defineModel<CodeBuddyCreditPolicy>('creditPolicy', { default: 'all' })

const { t } = useI18n()
const appStore = useAppStore()

const fieldsRef = ref<InstanceType<typeof CodeBuddyCredentialFields> | null>(null)
const showReauth = ref(false)
const reauthing = ref(false)
const syncing = ref(false)

const credentials = computed(() => (props.account.credentials || {}) as Record<string, unknown>)
const profile = computed(() => (typeof credentials.value.profile === 'string' ? credentials.value.profile : ''))
const uid = computed(() => (typeof credentials.value.uid === 'string' ? credentials.value.uid : ''))

const creditPolicyOptions = computed(() => [
  { value: 'all' as const, label: t('admin.accounts.codebuddyOAuth.creditPolicyAll') },
  { value: 'zero_only' as const, label: t('admin.accounts.codebuddyOAuth.creditPolicyZero') }
])

async function syncModels() {
  syncing.value = true
  try {
    const result = await adminAPI.codebuddy.syncModels(props.account.id)
    appStore.showSuccess(t('admin.accounts.codebuddyOAuth.syncModelsSuccess', { count: result.models.length }))
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  } finally {
    syncing.value = false
  }
}

async function reauth() {
  const built = fieldsRef.value?.buildSource()
  if (!built?.source) {
    appStore.showError(t(built?.error || 'admin.accounts.codebuddyOAuth.loginRequired'))
    return
  }
  reauthing.value = true
  try {
    const updated = await adminAPI.codebuddy.reAuthAccount(props.account.id, built.source)
    appStore.showSuccess(t('admin.accounts.codebuddyOAuth.reauthSuccess'))
    fieldsRef.value?.reset()
    showReauth.value = false
    emit('reauthorized', updated as Account)
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  } finally {
    reauthing.value = false
  }
}
</script>
