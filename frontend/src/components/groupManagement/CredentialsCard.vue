<template>
  <div class="space-y-4">
    <div class="flex items-start gap-3 rounded-xl bg-emerald-50 px-4 py-3 text-sm text-emerald-800 dark:bg-emerald-500/10 dark:text-emerald-200">
      <Icon name="checkCircle" size="md" class="shrink-0" />
      <div class="min-w-0">
        <p class="font-medium">{{ title }}</p>
        <p class="mt-0.5 leading-relaxed opacity-90">{{ hint }}</p>
      </div>
    </div>
    <dl class="divide-y divide-gray-100 rounded-xl border border-gray-200 dark:divide-dark-700 dark:border-dark-600">
      <div v-if="email" class="flex flex-wrap items-center justify-between gap-2 px-4 py-3">
        <dt class="text-xs font-medium text-gray-500 dark:text-dark-400">{{ t('groupManagement.createUser.email') }}</dt>
        <dd class="min-w-0 break-all font-mono text-sm text-gray-900 dark:text-white">{{ email }}</dd>
      </div>
      <div class="flex flex-wrap items-center justify-between gap-2 px-4 py-3">
        <dt class="text-xs font-medium text-gray-500 dark:text-dark-400">{{ t('groupManagement.createUser.password') }}</dt>
        <dd class="min-w-0 break-all font-mono text-sm text-gray-900 dark:text-white" data-testid="credentials-password">{{ password }}</dd>
      </div>
    </dl>
    <button type="button" class="btn btn-secondary w-full" @click="copy">
      <Icon :name="copied ? 'check' : 'copy'" size="sm" />
      {{ t('groupManagement.createUser.copy') }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { useClipboard } from '@/composables/useClipboard'

const props = defineProps<{
  title: string
  hint: string
  email?: string
  password: string
}>()

const { t } = useI18n()
const { copied, copyToClipboard } = useClipboard()

function copy() {
  const lines = [
    props.email ? `${t('groupManagement.createUser.email')}: ${props.email}` : '',
    `${t('groupManagement.createUser.password')}: ${props.password}`
  ].filter(Boolean)
  void copyToClipboard(lines.join('\n'), t('groupManagement.createUser.copied'))
}
</script>
