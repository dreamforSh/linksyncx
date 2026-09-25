<template>
  <BaseDialog :show="show" :title="t('groupManagement.addOwned.title')" width="normal" @close="emit('close')">
    <div class="space-y-4">
      <p class="text-sm text-gray-600 dark:text-dark-300">{{ t('groupManagement.addOwned.description') }}</p>

      <div v-if="loading" class="space-y-2" role="status" :aria-label="t('common.loading')">
        <div v-for="index in 3" :key="index" class="skeleton h-12 rounded-xl" />
      </div>

      <div
        v-else-if="!candidates.length"
        class="flex flex-col items-center rounded-xl border border-dashed border-gray-300 px-6 py-8 text-center dark:border-dark-600"
      >
        <Icon name="users" size="lg" class="mb-2 text-gray-400 dark:text-dark-400" />
        <p class="text-sm font-medium text-gray-900 dark:text-white">{{ t('groupManagement.addOwned.empty') }}</p>
        <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('groupManagement.addOwned.emptyHint') }}</p>
      </div>

      <template v-else>
        <div v-if="candidates.length > 6" class="relative">
          <Icon name="search" size="sm" class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
          <input
            v-model.trim="query"
            type="search"
            class="input pl-9"
            :placeholder="t('groupManagement.addOwned.search')"
            :aria-label="t('groupManagement.addOwned.search')"
          />
        </div>
        <fieldset>
          <legend class="sr-only">{{ t('groupManagement.addOwned.title') }}</legend>
          <ul class="max-h-80 divide-y divide-gray-100 overflow-y-auto rounded-xl border border-gray-200 dark:divide-dark-700 dark:border-dark-600">
            <li v-for="candidate in filtered" :key="candidate.user_id">
              <label
                class="flex cursor-pointer items-center gap-3 px-4 py-3 transition-colors duration-150 hover:bg-gray-50 dark:hover:bg-dark-700/50"
                :class="selectedId === candidate.user_id && 'bg-primary-50/60 dark:bg-primary-900/10'"
              >
                <input
                  v-model="selectedId"
                  type="radio"
                  name="group-owned-candidate"
                  :value="candidate.user_id"
                  class="h-4 w-4 shrink-0 cursor-pointer accent-primary-600"
                />
                <span class="min-w-0 flex-1">
                  <span class="block truncate text-sm font-medium text-gray-900 dark:text-white">{{ memberDisplayName(candidate) }}</span>
                  <span class="block truncate text-xs text-gray-500 dark:text-dark-400">
                    {{ memberSubtitle(candidate) }} · {{ t('groupManagement.addOwned.from', { group: candidate.group_name }) }}
                  </span>
                </span>
                <span v-if="candidate.status === 'disabled'" class="badge badge-gray">{{ t('groupManagement.members.disabled') }}</span>
              </label>
            </li>
          </ul>
        </fieldset>
      </template>
    </div>

    <template #footer>
      <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
      <button type="button" class="btn btn-primary" :disabled="saving || selectedId === null" @click="submit">
        <span v-if="saving" class="spinner h-4 w-4" aria-hidden="true" />
        {{ t('groupManagement.addOwned.submit') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import type { GroupOwnedUser } from '@/api/groupManagement'
import { memberDisplayName, memberSubtitle } from './helpers'

const props = defineProps<{
  show: boolean
  saving: boolean
  loading: boolean
  candidates: GroupOwnedUser[]
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'submit', userId: number): void
}>()

const { t } = useI18n()

const query = ref('')
const selectedId = ref<number | null>(null)

const filtered = computed(() => {
  const keyword = query.value.toLowerCase()
  if (!keyword) return props.candidates
  return props.candidates.filter(candidate =>
    [candidate.username, candidate.email, String(candidate.user_id)].some(value => value.toLowerCase().includes(keyword))
  )
})

watch(() => props.show, open => {
  if (!open) return
  query.value = ''
  selectedId.value = null
}, { immediate: true })

function submit() {
  if (selectedId.value === null || props.saving) return
  emit('submit', selectedId.value)
}
</script>
