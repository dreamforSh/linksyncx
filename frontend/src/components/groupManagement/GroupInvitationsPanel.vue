<template>
  <section class="card overflow-hidden">
    <header class="flex flex-wrap items-center gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
      <p class="flex min-w-0 flex-1 items-start gap-2 text-sm text-gray-600 dark:text-dark-300">
        <Icon name="mail" size="sm" class="mt-0.5 shrink-0 text-primary-500" />
        <span>{{ t('groupManagement.invitations.hint') }}</span>
      </p>
      <button type="button" class="btn btn-primary btn-md" :disabled="busy" @click="emit('invite')">
        <Icon name="userPlus" size="sm" />
        {{ t('groupManagement.invitations.invite') }}
      </button>
    </header>

    <div v-if="loading && !invitations.length" class="space-y-3 p-5" role="status" :aria-label="t('common.loading')">
      <div v-for="index in 3" :key="index" class="skeleton h-10 rounded-lg" />
    </div>

    <div v-else-if="!invitations.length" class="empty-state">
      <div class="mb-4 flex h-14 w-14 items-center justify-center rounded-2xl bg-gray-100 dark:bg-dark-700">
        <Icon name="mail" size="lg" class="text-gray-400 dark:text-dark-400" />
      </div>
      <h3 class="empty-state-title text-base">{{ t('groupManagement.invitations.empty') }}</h3>
      <p class="empty-state-description">{{ t('groupManagement.invitations.emptyHint') }}</p>
    </div>

    <div v-else class="overflow-x-auto">
      <table class="table min-w-[640px]">
        <thead>
          <tr class="whitespace-nowrap">
            <th scope="col">{{ t('groupManagement.invitations.email') }}</th>
            <th scope="col">{{ t('groupManagement.invitations.status') }}</th>
            <th scope="col">{{ t('groupManagement.invitations.inviter') }}</th>
            <th scope="col">{{ t('groupManagement.invitations.sentAt') }}</th>
            <th scope="col" class="!text-right">{{ t('groupManagement.members.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="invitation in invitations" :key="invitation.id" :data-invitation="invitation.id">
            <td class="max-w-[16rem] truncate font-medium text-gray-900 dark:text-white" :title="invitation.email">
              {{ invitation.email }}
            </td>
            <td>
              <span class="badge whitespace-nowrap" :class="statusClass(invitation.status)">
                {{ t(`groupManagement.invitations.statuses.${invitation.status}`) }}
              </span>
            </td>
            <td class="max-w-[10rem] truncate text-gray-600 dark:text-dark-300">{{ invitation.inviter_name || '-' }}</td>
            <td class="whitespace-nowrap text-gray-600 dark:text-dark-300">
              <time :datetime="invitation.created_at">{{ formatDateTimeToMinute(invitation.created_at) }}</time>
            </td>
            <td class="whitespace-nowrap text-right">
              <button
                v-if="invitation.status === 'pending'"
                type="button"
                class="btn btn-secondary btn-sm"
                :disabled="busy"
                :aria-label="`${t('groupManagement.invitations.revoke')} · ${invitation.email}`"
                @click="emit('revoke', invitation)"
              >
                {{ t('groupManagement.invitations.revoke') }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { GroupInvitation, GroupInvitationStatus } from '@/api/groupManagement'
import { formatDateTimeToMinute } from '@/utils/format'

defineProps<{
  invitations: GroupInvitation[]
  loading: boolean
  busy: boolean
}>()

const emit = defineEmits<{
  (e: 'invite'): void
  (e: 'revoke', invitation: GroupInvitation): void
}>()

const { t } = useI18n()

function statusClass(status: GroupInvitationStatus) {
  return {
    pending: 'badge-warning',
    accepted: 'badge-success',
    declined: 'badge-gray',
    revoked: 'badge-gray'
  }[status]
}
</script>
