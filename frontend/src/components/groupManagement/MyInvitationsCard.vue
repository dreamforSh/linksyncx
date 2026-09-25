<template>
  <section
    class="card overflow-hidden border-primary-200 dark:border-primary-500/30"
    aria-labelledby="gm-my-invitations"
    data-testid="my-invitations"
  >
    <header class="flex items-center gap-2 border-b border-gray-100 bg-primary-50/60 px-5 py-3 dark:border-dark-700 dark:bg-primary-900/10">
      <Icon name="mail" size="sm" class="text-primary-600 dark:text-primary-400" />
      <h2 id="gm-my-invitations" class="text-sm font-semibold text-gray-900 dark:text-white">
        {{ t('groupManagement.invitations.inboxTitle', { count: invitations.length }) }}
      </h2>
    </header>
    <ul class="divide-y divide-gray-100 dark:divide-dark-700">
      <li
        v-for="invitation in invitations"
        :key="invitation.id"
        class="flex flex-wrap items-center gap-3 px-5 py-3"
        :data-invitation="invitation.id"
      >
        <div class="min-w-0 flex-1">
          <p class="flex flex-wrap items-center gap-2 text-sm font-medium text-gray-900 dark:text-white">
            <span class="truncate">{{ invitation.group_name }}</span>
            <span v-if="typeLabel(invitation)" class="badge badge-primary">{{ typeLabel(invitation) }}</span>
          </p>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
            {{ t('groupManagement.invitations.invitedBy', { name: invitation.inviter_name || '-', time: formatDateTimeToMinute(invitation.created_at) }) }}
          </p>
          <p v-if="invitation.managed_type" class="mt-1 text-xs text-gray-500 dark:text-dark-400">
            {{ invitation.managed_type === 'subscription' ? t('groupManagement.invitations.subscriptionNote') : t('groupManagement.invitations.quotaNote') }}
          </p>
        </div>
        <div class="flex shrink-0 gap-2">
          <button
            type="button"
            class="btn btn-secondary btn-sm"
            :disabled="busyId !== null"
            @click="emit('decline', invitation)"
          >
            {{ t('groupManagement.invitations.decline') }}
          </button>
          <button
            type="button"
            class="btn btn-primary btn-sm"
            :disabled="busyId !== null"
            @click="emit('accept', invitation)"
          >
            <span v-if="busyId === invitation.id" class="spinner h-4 w-4" aria-hidden="true" />
            {{ t('groupManagement.invitations.accept') }}
          </button>
        </div>
      </li>
    </ul>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { GroupInvitation } from '@/api/groupManagement'
import { formatDateTimeToMinute } from '@/utils/format'

defineProps<{
  invitations: GroupInvitation[]
  // 正在处理的邀请
  busyId: number | null
}>()

const emit = defineEmits<{
  (e: 'accept', invitation: GroupInvitation): void
  (e: 'decline', invitation: GroupInvitation): void
}>()

const { t } = useI18n()

function typeLabel(invitation: GroupInvitation) {
  if (invitation.managed_type === 'quota') return t('groupManagement.type.quota')
  if (invitation.managed_type === 'subscription') return t('groupManagement.type.subscription')
  return ''
}
</script>
