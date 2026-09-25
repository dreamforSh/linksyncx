<template>
  <BaseDialog :show="show" :title="t('groupManagement.assign.title')" width="normal" @close="emit('close')">
    <div class="space-y-5">
      <div>
        <label :for="memberSelectId" class="input-label">{{ t('groupManagement.assign.member') }}</label>
        <Select
          :id="memberSelectId"
          :model-value="memberId"
          :options="memberOptions"
          :placeholder="t('groupManagement.assign.selectMember')"
          :aria-label="t('groupManagement.assign.member')"
          @update:model-value="selectMember"
        />
      </div>

      <fieldset :disabled="memberId === null" class="disabled:opacity-60">
        <legend class="sr-only">{{ t('groupManagement.assign.accounts') }}</legend>
        <div class="mb-2 flex flex-wrap items-center justify-between gap-2">
          <p class="text-sm font-medium text-gray-700 dark:text-gray-300" aria-hidden="true">
            {{ t('groupManagement.assign.accounts') }}
            <span class="ml-1 font-normal tabular-nums text-gray-500 dark:text-dark-400">
              {{ t('groupManagement.assign.selected', { count: selectedIds.length, total: accounts.length }) }}
            </span>
          </p>
          <div class="flex gap-1">
            <button type="button" class="btn btn-ghost btn-sm" @click="selectAll">{{ t('groupManagement.assign.selectAll') }}</button>
            <button type="button" class="btn btn-ghost btn-sm" @click="selectedIds = []">{{ t('groupManagement.assign.clear') }}</button>
          </div>
        </div>
        <ul class="max-h-80 divide-y divide-gray-100 overflow-y-auto rounded-xl border border-gray-200 dark:divide-dark-700 dark:border-dark-600">
          <li v-for="account in accounts" :key="account.id">
            <label
              class="flex cursor-pointer items-center gap-3 px-4 py-3 transition-colors duration-150 hover:bg-gray-50 dark:hover:bg-dark-700/50"
              :class="selectedIds.includes(account.id) && 'bg-primary-50/50 dark:bg-primary-900/10'"
            >
              <input
                v-model="selectedIds"
                type="checkbox"
                :value="account.id"
                class="h-4 w-4 shrink-0 cursor-pointer rounded accent-primary-600"
                :aria-label="account.name"
              />
              <span class="min-w-0 flex-1">
                <span class="block truncate text-sm font-medium text-gray-900 dark:text-white">{{ account.name }}</span>
                <span class="block text-xs text-gray-500 dark:text-dark-400">#{{ account.id }}</span>
              </span>
              <PlatformChip :platform="account.platform" />
              <AccountStatus :status="account.status" class="hidden sm:flex" />
            </label>
          </li>
        </ul>
      </fieldset>

      <p
        v-if="memberId !== null && !selectedIds.length"
        class="flex items-start gap-2 rounded-lg bg-amber-50 px-3 py-2 text-xs leading-relaxed text-amber-800 dark:bg-amber-500/10 dark:text-amber-200"
      >
        <Icon name="exclamationTriangle" size="sm" class="shrink-0" />
        <span>{{ t('groupManagement.assign.emptyWarning') }}</span>
      </p>
    </div>

    <template #footer>
      <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
      <button type="button" class="btn btn-primary" :disabled="memberId === null || !changed || saving" @click="submit">
        <span v-if="saving" class="spinner h-4 w-4" aria-hidden="true" />
        {{ saving ? t('common.saving') : t('groupManagement.assign.submit') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import type { GroupManagementAccount, GroupManagementMember } from '@/api/groupManagement'
import AccountStatus from './AccountStatus.vue'
import PlatformChip from './PlatformChip.vue'
import { memberDisplayName } from './helpers'

const props = defineProps<{
  show: boolean
  saving: boolean
  members: GroupManagementMember[]
  accounts: GroupManagementAccount[]
  initialMemberId: number | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'submit', payload: { userId: number; accountIds: number[] }): void
}>()

const { t } = useI18n()

const memberSelectId = 'group-assign-member'
const memberId = ref<number | null>(null)
const selectedIds = ref<number[]>([])

const memberOptions = computed(() => props.members.map(member => ({
  value: member.user_id,
  label: `${memberDisplayName(member)} · #${member.user_id}`
})))

function assignedIdsFor(userId: number) {
  return props.accounts.filter(account => account.assigned_user_ids.includes(userId)).map(account => account.id)
}

function syncSelection() {
  selectedIds.value = memberId.value === null ? [] : assignedIdsFor(memberId.value)
}

const changed = computed(() => {
  if (memberId.value === null) return false
  const current = new Set(assignedIdsFor(memberId.value))
  return selectedIds.value.length !== current.size || selectedIds.value.some(id => !current.has(id))
})

watch(() => props.show, open => {
  if (!open) return
  memberId.value = props.initialMemberId
  syncSelection()
}, { immediate: true })

function selectMember(value: string | number | boolean | null) {
  memberId.value = typeof value === 'number' ? value : null
  syncSelection()
}

function selectAll() {
  selectedIds.value = props.accounts.map(account => account.id)
}

function submit() {
  if (memberId.value === null || !changed.value || props.saving) return
  emit('submit', { userId: memberId.value, accountIds: [...selectedIds.value] })
}
</script>
