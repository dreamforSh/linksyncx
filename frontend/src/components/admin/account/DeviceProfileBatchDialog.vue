<template>
  <BaseDialog
    :show="show"
    :title="t('admin.accounts.deviceProfile.batch.title')"
    width="normal"
    @close="handleClose"
  >
    <div class="space-y-4" data-testid="device-profile-batch-dialog">
      <p class="text-sm leading-relaxed text-gray-600 dark:text-dark-300">
        {{ t('admin.accounts.deviceProfile.batch.description', { count: accountIds.length }) }}
      </p>

      <template v-if="!result">
        <div class="space-y-2">
          <label
            v-for="option in actionOptions"
            :key="option.value"
            :class="[
              'flex cursor-pointer items-start gap-3 rounded-lg border p-3 transition-colors',
              action === option.value
                ? 'border-primary-500 bg-primary-50 dark:border-primary-400 dark:bg-primary-900/20'
                : 'border-gray-200 hover:bg-gray-50 dark:border-dark-600 dark:hover:bg-dark-700'
            ]"
          >
            <input
              v-model="action"
              type="radio"
              name="device-profile-batch-action"
              :value="option.value"
              class="mt-0.5 text-primary-600 focus:ring-primary-500"
              :data-testid="`device-profile-batch-${option.value}`"
            />
            <span>
              <span class="block text-sm font-medium text-gray-900 dark:text-white">{{ option.label }}</span>
              <span class="mt-0.5 block text-xs text-gray-500 dark:text-gray-400">{{ option.hint }}</span>
            </span>
          </label>
        </div>

        <label class="flex items-start gap-2 text-sm text-gray-700 dark:text-gray-300">
          <input
            v-model="resetIdentity"
            type="checkbox"
            class="mt-0.5 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-500"
            data-testid="device-profile-batch-reset-identity"
          />
          <span>
            {{ t('admin.accounts.deviceProfile.batch.resetIdentity') }}
            <span class="block text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.accounts.deviceProfile.batch.resetIdentityHint') }}
            </span>
          </span>
        </label>
      </template>

      <div v-else class="space-y-3" data-testid="device-profile-batch-result">
        <p class="text-sm font-medium text-gray-900 dark:text-white">
          {{ t('admin.accounts.deviceProfile.batch.summary', {
            updated: result.updated,
            skipped: result.skipped,
            failed: result.failed
          }) }}
        </p>
        <p v-if="identityResetCount > 0" class="text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.accounts.deviceProfile.batch.identityResetCount', { count: identityResetCount }) }}
        </p>
        <div v-if="unchangedItems.length > 0">
          <p class="mb-1 text-xs font-medium text-gray-700 dark:text-gray-300">
            {{ t('admin.accounts.deviceProfile.batch.detailsTitle') }}
          </p>
          <ul class="max-h-48 space-y-1 overflow-y-auto rounded-lg bg-gray-50 p-2 text-xs dark:bg-dark-800">
            <li v-for="item in unchangedItems" :key="item.account_id" class="flex justify-between gap-3">
              <span class="truncate text-gray-700 dark:text-gray-300">{{ item.name || `#${item.account_id}` }}</span>
              <span
                :class="item.status === 'failed' ? 'text-red-600 dark:text-red-400' : 'text-gray-500 dark:text-gray-400'"
                class="flex-shrink-0"
              >
                {{ describeItem(item) }}
              </span>
            </li>
          </ul>
        </div>
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <template v-if="!result">
          <button type="button" class="btn btn-secondary" :disabled="submitting" @click="handleClose">
            {{ t('common.cancel') }}
          </button>
          <button
            type="button"
            class="btn btn-primary"
            :disabled="submitting || accountIds.length === 0"
            data-testid="device-profile-batch-submit"
            @click="submit"
          >
            {{ t('admin.accounts.deviceProfile.batch.submit') }}
          </button>
        </template>
        <button v-else type="button" class="btn btn-primary" @click="handleClose">
          {{ t('common.close') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { adminAPI } from '@/api/admin'
import type {
  DeviceProfileBatchAction,
  DeviceProfileBatchItem,
  DeviceProfileBatchResult
} from '@/api/admin/accounts'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'

// 与后端 MaxDeviceProfileBatchAccounts 保持一致。
const MAX_BATCH_ACCOUNTS = 1000

const props = defineProps<{
  show: boolean
  accountIds: number[]
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'done', result: DeviceProfileBatchResult): void
}>()

const { t } = useI18n()
const appStore = useAppStore()

const action = ref<DeviceProfileBatchAction>('randomize')
const resetIdentity = ref(true)
const submitting = ref(false)
const result = ref<DeviceProfileBatchResult | null>(null)

const actionOptions = computed(() => [
  {
    value: 'randomize' as const,
    label: t('admin.accounts.deviceProfile.batch.randomize'),
    hint: t('admin.accounts.deviceProfile.batch.randomizeHint')
  },
  {
    value: 'clear' as const,
    label: t('admin.accounts.deviceProfile.batch.clear'),
    hint: t('admin.accounts.deviceProfile.batch.clearHint')
  }
])

const unchangedItems = computed(() =>
  (result.value?.results ?? []).filter((item) => item.status !== 'updated')
)
const identityResetCount = computed(() =>
  (result.value?.results ?? []).filter((item) => item.identity_reset).length
)

watch(
  () => props.show,
  (visible) => {
    if (!visible) return
    action.value = 'randomize'
    resetIdentity.value = true
    result.value = null
  }
)

const describeItem = (item: DeviceProfileBatchItem) => {
  if (item.status === 'failed') {
    return item.reason || t('admin.accounts.deviceProfile.batch.failed')
  }
  switch (item.reason) {
    case 'not_found':
      return t('admin.accounts.deviceProfile.batch.skipReasons.notFound')
    case 'shadow':
      return t('admin.accounts.deviceProfile.batch.skipReasons.shadow')
    default:
      return t('admin.accounts.deviceProfile.batch.skipReasons.unsupported')
  }
}

const submit = async () => {
  if (props.accountIds.length > MAX_BATCH_ACCOUNTS) {
    appStore.showError(t('admin.accounts.deviceProfile.batch.tooMany', { max: MAX_BATCH_ACCOUNTS }))
    return
  }
  submitting.value = true
  try {
    const response = await adminAPI.accounts.batchDeviceProfile(
      [...props.accountIds],
      action.value,
      resetIdentity.value
    )
    result.value = response
    if (response.failed > 0) {
      appStore.showError(t('admin.accounts.deviceProfile.batch.summary', {
        updated: response.updated,
        skipped: response.skipped,
        failed: response.failed
      }))
    } else {
      appStore.showSuccess(t('admin.accounts.deviceProfile.batch.summary', {
        updated: response.updated,
        skipped: response.skipped,
        failed: response.failed
      }))
    }
    emit('done', response)
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('admin.accounts.deviceProfile.batch.failed')))
  } finally {
    submitting.value = false
  }
}

const handleClose = () => {
  if (submitting.value) return
  emit('close')
}
</script>
