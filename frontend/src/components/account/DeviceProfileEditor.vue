<template>
  <div class="space-y-3" data-testid="device-profile-editor">
    <div v-if="platform === 'anthropic'" class="grid grid-cols-2 gap-3">
      <div>
        <label class="input-label">{{ t('admin.accounts.deviceProfile.os') }}</label>
        <select
          :value="modelValue.device_os"
          class="input"
          data-testid="device-profile-os"
          @change="update({ device_os: ($event.target as HTMLSelectElement).value })"
        >
          <option value="">{{ t('admin.accounts.deviceProfile.defaultOption') }}</option>
          <option v-for="os in osOptions" :key="os" :value="os">{{ os }}</option>
        </select>
      </div>
      <div>
        <label class="input-label">{{ t('admin.accounts.deviceProfile.arch') }}</label>
        <select
          :value="modelValue.device_arch"
          class="input"
          data-testid="device-profile-arch"
          @change="update({ device_arch: ($event.target as HTMLSelectElement).value })"
        >
          <option value="">{{ t('admin.accounts.deviceProfile.defaultOption') }}</option>
          <option v-for="arch in archOptions" :key="arch" :value="arch">{{ arch }}</option>
        </select>
      </div>
    </div>

    <div>
      <label class="input-label">{{ t('admin.accounts.deviceProfile.userAgent') }}</label>
      <input
        :value="modelValue.user_agent"
        type="text"
        class="input font-mono text-xs"
        data-testid="device-profile-user-agent"
        :placeholder="platform === 'anthropic'
          ? t('admin.accounts.deviceProfile.claudeUserAgentPlaceholder')
          : t('admin.accounts.deviceProfile.codexUserAgentPlaceholder')"
        @input="update({ user_agent: ($event.target as HTMLInputElement).value })"
      />
      <p v-if="userAgentInvalid" class="mt-1 text-xs text-amber-600 dark:text-amber-400">
        {{ t('admin.accounts.deviceProfile.claudeUserAgentInvalid') }}
      </p>
      <p class="input-hint">
        {{ platform === 'anthropic'
          ? t('admin.accounts.deviceProfile.claudeUserAgentHint')
          : t('admin.accounts.deviceProfile.codexUserAgentHint') }}
      </p>
    </div>

    <div class="flex flex-wrap items-center gap-2">
      <button
        type="button"
        class="btn btn-secondary btn-sm"
        data-testid="device-profile-randomize"
        :disabled="generating"
        @click="randomize"
      >
        <Icon name="refresh" size="sm" :class="['mr-1', generating ? 'animate-spin' : '']" />
        {{ t('admin.accounts.deviceProfile.randomize') }}
      </button>
      <button
        type="button"
        class="btn btn-secondary btn-sm"
        data-testid="device-profile-clear"
        :disabled="isEmpty"
        @click="emit('update:modelValue', emptyDeviceProfile())"
      >
        {{ t('admin.accounts.deviceProfile.clear') }}
      </button>
      <span class="text-xs text-gray-500 dark:text-gray-400">
        {{ isEmpty ? t('admin.accounts.deviceProfile.usingDefault') : t('admin.accounts.deviceProfile.pinned') }}
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import Icon from '@/components/icons/Icon.vue'
import { extractApiErrorMessage } from '@/utils/apiError'
import {
  CLAUDE_DEVICE_ARCH_OPTIONS,
  CLAUDE_DEVICE_OS_OPTIONS,
  CLAUDE_DEVICE_USER_AGENT_PATTERN,
  emptyDeviceProfile,
  type DeviceProfileValue
} from './credentialsBuilder'

const props = defineProps<{
  platform: 'anthropic' | 'openai'
  modelValue: DeviceProfileValue
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', value: DeviceProfileValue): void
}>()

const { t } = useI18n()
const appStore = useAppStore()

const osOptions = CLAUDE_DEVICE_OS_OPTIONS
const archOptions = CLAUDE_DEVICE_ARCH_OPTIONS
const generating = ref(false)

const isEmpty = computed(() =>
  !props.modelValue.user_agent.trim() &&
  !props.modelValue.device_os.trim() &&
  !props.modelValue.device_arch.trim()
)

// Codex UA 的合法形态较多，由后端在保存时校验；Claude UA 形态固定，前端先给提示。
const userAgentInvalid = computed(() => {
  const ua = props.modelValue.user_agent.trim()
  return props.platform === 'anthropic' && ua !== '' && !CLAUDE_DEVICE_USER_AGENT_PATTERN.test(ua)
})

const update = (patch: Partial<DeviceProfileValue>) => {
  emit('update:modelValue', { ...props.modelValue, ...patch })
}

const randomize = async () => {
  generating.value = true
  try {
    const profile = await adminAPI.accounts.randomDeviceProfile(props.platform)
    // Claude 随机画像不带 UA：保留管理员手填的 UA 模板，只换 OS / 架构。
    emit('update:modelValue', {
      user_agent: props.platform === 'anthropic' ? props.modelValue.user_agent : profile.user_agent || '',
      device_os: profile.device_os || '',
      device_arch: profile.device_arch || ''
    })
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('admin.accounts.deviceProfile.randomizeFailed')))
  } finally {
    generating.value = false
  }
}
</script>
