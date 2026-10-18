import { onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { CodeBuddyLoginSite, CodeBuddyProfile } from '@/api/admin/codebuddy'
import { useClipboard } from '@/composables/useClipboard'
import { extractApiErrorMessage } from '@/utils/apiError'

/** Base gap between two poll requests, in milliseconds. */
const POLL_INTERVAL_MS = 2000
/** Cap for the backoff applied after consecutive poll failures. */
const POLL_MAX_INTERVAL_MS = 15000
/** Fallback window when the backend does not report one. */
const DEFAULT_EXPIRES_IN = 600

/**
 * Drives a CodeBuddy / WorkBuddy scan login: start → open the official login
 * page → poll until the backend holds the credentials under `loginId`, which
 * the create / re-auth call then consumes.
 */
export function useCodeBuddyOAuth() {
  const appStore = useAppStore()
  const { t } = useI18n()
  const { copyToClipboard } = useClipboard()

  const loginId = ref('')
  const verificationUri = ref('')
  const loading = ref(false)
  const polling = ref(false)
  const error = ref('')
  const done = ref(false)
  const uid = ref('')
  const nickname = ref('')
  const profile = ref<CodeBuddyProfile | ''>('')
  const remainingSeconds = ref(0)

  // setTimeout rather than setInterval: an in-flight poll must never overlap
  // with the next one, and the delay grows as failures back off.
  let pollTimer: ReturnType<typeof setTimeout> | null = null
  let countdownTimer: ReturnType<typeof setInterval> | null = null
  let deadline = 0
  let consecutiveFailures = 0

  const stopPolling = () => {
    if (pollTimer) {
      clearTimeout(pollTimer)
      pollTimer = null
    }
    if (countdownTimer) {
      clearInterval(countdownTimer)
      countdownTimer = null
    }
    polling.value = false
  }

  const resetState = () => {
    stopPolling()
    loginId.value = ''
    verificationUri.value = ''
    loading.value = false
    error.value = ''
    done.value = false
    uid.value = ''
    nickname.value = ''
    profile.value = ''
    remainingSeconds.value = 0
    deadline = 0
    consecutiveFailures = 0
  }

  const pollOnce = async () => {
    if (!loginId.value) return
    try {
      const result = await adminAPI.codebuddy.pollLogin(loginId.value)
      consecutiveFailures = 0
      error.value = ''
      if (!result.done) return
      stopPolling()
      if (result.error) {
        error.value = result.error
        appStore.showError(result.error)
        return
      }
      done.value = true
      uid.value = result.uid || ''
      nickname.value = result.nickname || ''
      profile.value = result.profile || ''
    } catch (err: unknown) {
      // A transient poll failure is not a login failure: surface it but keep
      // polling on a widening interval until the login window closes.
      consecutiveFailures += 1
      error.value = extractApiErrorMessage(err, t('admin.accounts.codebuddyOAuth.failedToPoll'))
    }
  }

  const scheduleNextPoll = () => {
    if (pollTimer) clearTimeout(pollTimer)
    const backoff = Math.min(POLL_INTERVAL_MS * 2 ** consecutiveFailures, POLL_MAX_INTERVAL_MS)
    const delay = Math.max(0, Math.min(backoff, deadline - Date.now()))
    pollTimer = setTimeout(async () => {
      if (!polling.value) return
      if (Date.now() >= deadline) {
        stopPolling()
        remainingSeconds.value = 0
        error.value = t('admin.accounts.codebuddyOAuth.loginTimeout')
        return
      }
      await pollOnce()
      if (polling.value) scheduleNextPoll()
    }, delay)
  }

  const startPolling = (expiresIn: number) => {
    stopPolling()
    polling.value = true
    consecutiveFailures = 0
    deadline = Date.now() + expiresIn * 1000
    remainingSeconds.value = expiresIn
    countdownTimer = setInterval(() => {
      remainingSeconds.value = Math.max(0, Math.ceil((deadline - Date.now()) / 1000))
    }, 1000)
    scheduleNextPoll()
  }

  const start = async (site: CodeBuddyLoginSite, proxyId?: number | null) => {
    resetState()
    loading.value = true
    try {
      const result = await adminAPI.codebuddy.startLogin(site, proxyId)
      loginId.value = result.login_id
      verificationUri.value = result.verification_uri
      startPolling(result.expires_in > 0 ? result.expires_in : DEFAULT_EXPIRES_IN)
      return true
    } catch (err: unknown) {
      error.value = extractApiErrorMessage(err, t('admin.accounts.codebuddyOAuth.failedToStart'))
      appStore.showError(error.value)
      return false
    } finally {
      loading.value = false
    }
  }

  const copyVerificationUri = async () => {
    if (!verificationUri.value) return
    await copyToClipboard(verificationUri.value, t('admin.accounts.codebuddyOAuth.linkCopied'))
  }

  // Closing the modal without resetting would otherwise leave both timers alive.
  onUnmounted(stopPolling)

  return {
    loginId,
    verificationUri,
    loading,
    polling,
    error,
    done,
    uid,
    nickname,
    profile,
    remainingSeconds,
    resetState,
    stopPolling,
    start,
    copyVerificationUri
  }
}
