<template>
  <div class="space-y-4" data-testid="codebuddy-credential-fields">
    <!-- Credential source -->
    <div>
      <label class="input-label">{{ t('admin.accounts.codebuddyOAuth.source') }}</label>
      <div class="flex flex-wrap rounded-lg bg-gray-100 p-1 dark:bg-dark-700">
        <button
          v-for="option in sourceOptions"
          :key="option.value"
          type="button"
          :data-testid="`codebuddy-source-${option.value}`"
          :class="[
            'flex flex-1 items-center justify-center rounded-md px-3 py-2 text-sm font-medium transition-all',
            source === option.value
              ? 'bg-white text-violet-600 shadow-sm dark:bg-dark-600 dark:text-violet-400'
              : 'text-gray-600 hover:text-gray-900 dark:text-gray-400 dark:hover:text-gray-200'
          ]"
          @click="source = option.value"
        >
          {{ option.label }}
        </button>
      </div>
    </div>

    <!-- Scan login -->
    <div v-if="source === 'scan'" class="space-y-3">
      <div>
        <label class="input-label">{{ t('admin.accounts.codebuddyOAuth.site') }}</label>
        <div class="grid grid-cols-1 gap-2 sm:grid-cols-3">
          <button
            v-for="option in siteOptions"
            :key="option.value"
            type="button"
            :data-testid="`codebuddy-site-${option.value}`"
            :class="[
              'rounded-lg border px-3 py-2 text-left text-sm transition-colors',
              site === option.value
                ? 'border-violet-500 bg-violet-50 text-violet-700 dark:bg-violet-900/20 dark:text-violet-300'
                : 'border-gray-200 text-gray-700 hover:border-gray-300 dark:border-dark-600 dark:text-gray-300'
            ]"
            :disabled="oauth.polling.value"
            @click="site = option.value"
          >
            <span class="block font-medium">{{ option.label }}</span>
            <span class="block text-xs text-gray-500 dark:text-gray-400">{{ option.hint }}</span>
          </button>
        </div>
      </div>

      <div class="flex flex-wrap items-center gap-2">
        <button
          type="button"
          class="btn btn-primary btn-sm"
          data-testid="codebuddy-start-login"
          :disabled="oauth.loading.value || oauth.polling.value"
          @click="oauth.start(site, proxyId)"
        >
          {{ oauth.verificationUri.value && !oauth.polling.value ? t('admin.accounts.codebuddyOAuth.restart') : t('admin.accounts.codebuddyOAuth.start') }}
        </button>
        <template v-if="oauth.verificationUri.value">
          <a
            :href="oauth.verificationUri.value"
            target="_blank"
            rel="noopener noreferrer"
            class="btn btn-secondary btn-sm"
          >
            {{ t('admin.accounts.codebuddyOAuth.openUrl') }}
          </a>
          <button type="button" class="btn btn-secondary btn-sm" @click="oauth.copyVerificationUri()">
            {{ t('admin.accounts.codebuddyOAuth.copyUrl') }}
          </button>
          <button v-if="oauth.polling.value" type="button" class="btn btn-secondary btn-sm" @click="oauth.stopPolling()">
            {{ t('admin.accounts.codebuddyOAuth.cancel') }}
          </button>
        </template>
      </div>
      <p v-if="oauth.verificationUri.value" class="break-all rounded-md bg-gray-50 px-3 py-2 font-mono text-xs text-gray-600 dark:bg-dark-800 dark:text-gray-400">
        {{ oauth.verificationUri.value }}
      </p>
      <p v-if="oauth.polling.value" class="text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.codebuddyOAuth.polling') }}
        <span v-if="countdown" class="ml-1 font-mono text-xs">{{ countdown }}</span>
      </p>
      <p v-if="oauth.done.value" class="text-sm text-emerald-600 dark:text-emerald-400" data-testid="codebuddy-login-done">
        {{ t('admin.accounts.codebuddyOAuth.done') }}
        <span v-if="oauth.nickname.value"> · {{ oauth.nickname.value }}</span>
        <span v-if="oauth.uid.value" class="ml-1 font-mono text-xs text-gray-500">uid {{ oauth.uid.value }}</span>
        <span v-if="oauth.profile.value" class="ml-1 text-xs text-gray-500">· {{ profileLabel(oauth.profile.value) }}</span>
      </p>
      <p v-if="oauth.error.value" class="text-sm text-red-500">{{ oauth.error.value }}</p>
    </div>

    <!-- .info import -->
    <div v-else-if="source === 'info'">
      <label class="input-label">{{ t('admin.accounts.codebuddyOAuth.importJson') }}</label>
      <textarea
        v-model="infoJson"
        class="input font-mono text-xs"
        rows="7"
        data-testid="codebuddy-info-json"
        :placeholder="t('admin.accounts.codebuddyOAuth.importPlaceholder')"
      />
      <p class="input-hint">{{ t('admin.accounts.codebuddyOAuth.importHint') }}</p>
    </div>

    <!-- Refresh token import -->
    <div v-else class="space-y-3">
      <div>
        <label class="input-label">{{ t('admin.accounts.codebuddyOAuth.profile') }}</label>
        <select v-model="profile" class="input" data-testid="codebuddy-profile">
          <option v-for="option in profileOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
        </select>
        <p class="input-hint">{{ t('admin.accounts.codebuddyOAuth.profileHint') }}</p>
      </div>
      <div>
        <label class="input-label">{{ t('admin.accounts.codebuddyOAuth.refreshToken') }}</label>
        <textarea
          v-model="refreshToken"
          class="input font-mono text-xs"
          rows="3"
          data-testid="codebuddy-refresh-token"
          :placeholder="t('admin.accounts.codebuddyOAuth.refreshTokenPlaceholder')"
        />
      </div>
    </div>

    <!-- Credit policy -->
    <div v-if="showCreditPolicy">
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
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useCodeBuddyOAuth } from '@/composables/useCodeBuddyOAuth'
import type {
  CodeBuddyCredentialSource,
  CodeBuddyCreditPolicy,
  CodeBuddyLoginSite,
  CodeBuddyProfile
} from '@/api/admin/codebuddy'

withDefaults(
  defineProps<{
    proxyId?: number | null
    /** Hidden on re-authorization, where the stored policy is kept. */
    showCreditPolicy?: boolean
  }>(),
  { proxyId: null, showCreditPolicy: true }
)

const { t } = useI18n()
const oauth = useCodeBuddyOAuth()

type Source = 'scan' | 'info' | 'refresh'

const source = ref<Source>('scan')
const site = ref<CodeBuddyLoginSite>('cn')
const infoJson = ref('')
const refreshToken = ref('')
const profile = ref<CodeBuddyProfile>('cn-cli')
const creditPolicy = ref<CodeBuddyCreditPolicy>('all')

const sourceOptions = computed(() => [
  { value: 'scan' as const, label: t('admin.accounts.codebuddyOAuth.sourceScan') },
  { value: 'info' as const, label: t('admin.accounts.codebuddyOAuth.sourceInfo') },
  { value: 'refresh' as const, label: t('admin.accounts.codebuddyOAuth.sourceRefreshToken') }
])

const siteOptions = computed(() => [
  { value: 'cn' as const, label: t('admin.accounts.codebuddyOAuth.siteCN'), hint: 'www.codebuddy.cn' },
  { value: 'intl' as const, label: t('admin.accounts.codebuddyOAuth.siteIntlWorkBuddy'), hint: 'www.workbuddy.ai' },
  { value: 'intl-codebuddy' as const, label: t('admin.accounts.codebuddyOAuth.siteIntlCodeBuddy'), hint: 'www.codebuddy.ai' }
])

const profileOptions = computed(() =>
  (['cn-cli', 'cn-work', 'intl-cli', 'intl-work'] as const).map((value) => ({ value, label: profileLabel(value) }))
)

const creditPolicyOptions = computed(() => [
  { value: 'all' as const, label: t('admin.accounts.codebuddyOAuth.creditPolicyAll') },
  { value: 'zero_only' as const, label: t('admin.accounts.codebuddyOAuth.creditPolicyZero') }
])

function profileLabel(value: string): string {
  return t(`admin.accounts.codebuddyOAuth.profiles.${value}`)
}

const countdown = computed(() => {
  const total = oauth.remainingSeconds.value
  if (total <= 0) return ''
  const minutes = Math.floor(total / 60)
  const seconds = String(total % 60).padStart(2, '0')
  return `${minutes}:${seconds}`
})

/** Whether the selected source has enough input to submit. */
const ready = computed(() => {
  if (source.value === 'scan') return oauth.done.value && !!oauth.loginId.value
  if (source.value === 'info') return infoJson.value.trim() !== ''
  return refreshToken.value.trim() !== ''
})

/**
 * Builds the credential source for the create / re-auth request, or returns
 * an i18n error key when the input is incomplete or invalid.
 */
function buildSource(): { source?: CodeBuddyCredentialSource; error?: string } {
  if (source.value === 'scan') {
    if (!oauth.done.value || !oauth.loginId.value) return { error: 'admin.accounts.codebuddyOAuth.loginRequired' }
    return { source: { login_id: oauth.loginId.value } }
  }
  if (source.value === 'info') {
    try {
      const parsed = JSON.parse(infoJson.value)
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('not an object')
      return { source: { credentials: parsed as Record<string, unknown> } }
    } catch {
      return { error: 'admin.accounts.codebuddyOAuth.invalidJson' }
    }
  }
  const token = refreshToken.value.trim()
  if (!token) return { error: 'admin.accounts.codebuddyOAuth.refreshTokenRequired' }
  return { source: { refresh_token: token, profile: profile.value } }
}

function reset() {
  oauth.resetState()
  source.value = 'scan'
  site.value = 'cn'
  infoJson.value = ''
  refreshToken.value = ''
  profile.value = 'cn-cli'
  creditPolicy.value = 'all'
}

defineExpose({ buildSource, reset, ready, creditPolicy })
</script>
