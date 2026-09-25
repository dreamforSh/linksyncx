<template>
  <section class="card overflow-hidden">
    <header class="flex flex-wrap items-start justify-between gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
      <p class="min-w-0 max-w-2xl flex-1 text-sm leading-relaxed text-gray-600 dark:text-dark-300">
        {{ t('groupManagement.managers.description') }}
      </p>
      <RouterLink to="/admin/users" class="btn btn-ghost btn-sm shrink-0">
        {{ t('groupManagement.managers.changeRole') }}
        <Icon name="arrowRight" size="xs" />
      </RouterLink>
    </header>

    <div class="flex flex-wrap items-end gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
      <div class="min-w-[14rem] flex-1 sm:max-w-sm">
        <label :for="selectId" class="input-label">{{ t('groupManagement.managers.select') }}</label>
        <Select
          :id="selectId"
          :model-value="candidateId"
          :options="candidates"
          :placeholder="t('groupManagement.managers.select')"
          :aria-label="t('groupManagement.managers.select')"
          :disabled="!candidates.length || busy"
          @update:model-value="selectCandidate"
        />
      </div>
      <button
        type="button"
        class="btn btn-primary btn-md"
        :disabled="candidateId === null || busy"
        @click="assign"
      >
        <Icon name="shield" size="sm" />
        {{ t('groupManagement.managers.assign') }}
      </button>
      <p v-if="!candidates.length" class="w-full text-xs text-gray-500 dark:text-dark-400">
        {{ t('groupManagement.managers.noCandidates') }}
      </p>
    </div>

    <ul v-if="managers.length" class="divide-y divide-gray-100 dark:divide-dark-700">
      <li v-for="manager in managers" :key="manager.id" class="flex items-center gap-3 px-5 py-3">
        <span
          class="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-purple-100 text-sm font-semibold text-purple-700 dark:bg-purple-900/30 dark:text-purple-300"
          aria-hidden="true"
        >
          {{ initialOf(manager.name) }}
        </span>
        <div class="min-w-0 flex-1">
          <p class="truncate text-sm font-medium text-gray-900 dark:text-white">{{ manager.name }}</p>
          <p class="truncate text-xs text-gray-500 dark:text-dark-400">
            {{ [manager.email, `#${manager.id}`].filter(Boolean).join(' · ') }}
          </p>
        </div>
        <button
          type="button"
          class="btn btn-secondary btn-sm text-red-600 hover:border-red-200 hover:bg-red-50 dark:text-red-400 dark:hover:border-red-500/30 dark:hover:bg-red-500/10"
          :disabled="busy"
          @click="emit('revoke', manager.id)"
        >
          {{ t('groupManagement.managers.revoke') }}
        </button>
      </li>
    </ul>
    <p v-else class="px-5 py-8 text-center text-sm text-gray-500 dark:text-dark-400">
      {{ t('groupManagement.managers.empty') }}
    </p>
  </section>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import Select from '@/components/common/Select.vue'
import { initialOf } from './helpers'

export interface ManagerEntry {
  id: number
  name: string
  email: string
}

const props = defineProps<{
  groupId: number
  managers: ManagerEntry[]
  candidates: Array<{ value: number; label: string }>
  busy: boolean
}>()

const emit = defineEmits<{
  (e: 'assign', userId: number): void
  (e: 'revoke', userId: number): void
}>()

const { t } = useI18n()

const selectId = `group-manager-select-${Math.random().toString(36).slice(2, 8)}`
const candidateId = ref<number | null>(null)

// A candidate disappears from the list once authorized or when the group changes.
watch(
  () => [props.groupId, props.candidates] as const,
  () => {
    if (!props.candidates.some(candidate => candidate.value === candidateId.value)) candidateId.value = null
  }
)

function selectCandidate(value: string | number | boolean | null) {
  candidateId.value = typeof value === 'number' ? value : null
}

function assign() {
  if (candidateId.value === null) return
  emit('assign', candidateId.value)
}
</script>
