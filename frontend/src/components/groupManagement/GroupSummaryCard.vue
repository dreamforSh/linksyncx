<template>
  <section class="card overflow-hidden" :aria-labelledby="headingId">
    <div class="flex items-start gap-3 p-5 sm:gap-4 sm:p-6">
      <div
        class="flex h-12 w-12 shrink-0 items-center justify-center rounded-2xl bg-gradient-to-br from-primary-500 to-primary-600 text-lg font-semibold text-white shadow-md shadow-primary-500/20"
        aria-hidden="true"
      >
        {{ initialOf(group.name) }}
      </div>
      <div class="min-w-0 flex-1">
        <h2 :id="headingId" class="truncate text-lg font-semibold text-gray-900 dark:text-white">
          {{ group.name }}
        </h2>
        <div class="mt-2 flex flex-wrap items-center gap-2">
          <template v-if="managed">
            <div
              v-if="categoryEditable"
              role="radiogroup"
              :aria-label="t('groupManagement.category.label')"
              class="inline-flex rounded-lg bg-gray-100 p-0.5 dark:bg-dark-700"
              data-testid="group-category-switch"
            >
              <button
                v-for="option in categoryOptions"
                :key="option.value"
                type="button"
                role="radio"
                :aria-checked="group.category === option.value"
                :disabled="categorySaving"
                :data-testid="`group-category-${option.value}`"
                class="flex items-center gap-1 rounded-md px-2.5 py-1 text-xs font-medium transition-colors duration-150 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/50 disabled:cursor-wait"
                :class="group.category === option.value
                  ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-600 dark:text-white'
                  : 'text-gray-600 hover:text-gray-900 dark:text-dark-300 dark:hover:text-white'"
                @click="group.category !== option.value && emit('update-category', option.value)"
              >
                <Icon :name="option.icon" size="xs" />
                {{ option.label }}
              </button>
            </div>
            <GroupKindBadge v-else kind="managed" :category="group.category" />
            <span v-if="group.managed_type" class="badge badge-primary" data-testid="group-managed-type">
              <Icon :name="group.managed_type === 'quota' ? 'dollar' : 'server'" size="xs" />
              {{ group.managed_type === 'quota' ? t('groupManagement.type.quota') : t('groupManagement.type.subscription') }}
            </span>
          </template>
          <span class="badge" :class="group.enabled ? 'badge-success' : 'badge-gray'">
            <span
              class="h-1.5 w-1.5 rounded-full"
              :class="group.enabled ? 'bg-emerald-500' : 'bg-gray-400 dark:bg-dark-400'"
              aria-hidden="true"
            />
            {{ group.enabled ? t('groupManagement.state.enabled') : t('groupManagement.state.disabled') }}
          </span>
          <span class="badge badge-gray">
            <Icon :name="group.allocation_mode === 'manual' ? 'link' : 'sync'" size="xs" />
            {{ group.allocation_mode === 'manual' ? t('groupManagement.settings.manual') : t('groupManagement.settings.auto') }}
          </span>
          <span class="badge" :class="group.manager ? 'badge-primary' : 'badge-gray'">
            <Icon :name="group.manager ? 'shield' : 'eye'" size="xs" />
            {{ group.manager ? t('groupManagement.state.manager') : t('groupManagement.state.member') }}
          </span>
        </div>
      </div>
      <button
        type="button"
        class="btn btn-secondary btn-sm"
        :disabled="refreshing"
        :title="t('common.refresh')"
        :aria-label="t('common.refresh')"
        @click="emit('refresh')"
      >
        <Icon
          name="refresh"
          size="sm"
          :class="refreshing && 'animate-spin motion-reduce:animate-none'"
        />
        <span class="hidden sm:inline">{{ t('common.refresh') }}</span>
      </button>
    </div>

    <dl
      class="grid grid-cols-2 divide-gray-100 border-t border-gray-100 dark:divide-dark-700 dark:border-dark-700 sm:grid-cols-4 sm:divide-x"
    >
      <div v-for="stat in stats" :key="stat.key" class="px-5 py-4 sm:px-6">
        <dt class="text-xs font-medium text-gray-500 dark:text-dark-400">{{ stat.label }}</dt>
        <dd class="mt-1 truncate text-xl font-semibold tabular-nums text-gray-900 dark:text-white">
          {{ stat.value }}
        </dd>
      </div>
    </dl>

    <div
      v-if="!group.enabled"
      class="flex flex-wrap items-center gap-x-3 gap-y-2 border-t border-amber-200/70 bg-amber-50 px-5 py-3 text-sm text-amber-800 dark:border-amber-500/20 dark:bg-amber-500/10 dark:text-amber-200 sm:px-6"
    >
      <Icon name="exclamationTriangle" size="sm" class="shrink-0" />
      <p class="min-w-0 flex-1">
        {{ group.manager ? t('groupManagement.summary.disabledManager') : t('groupManagement.summary.disabledMember') }}
      </p>
      <button
        v-if="group.manager"
        type="button"
        class="rounded font-medium underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-amber-500/50"
        @click="emit('open-settings')"
      >
        {{ t('groupManagement.summary.openSettings') }}
      </button>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import GroupKindBadge from '@/components/admin/group/GroupKindBadge.vue'
import type { GroupManagementGroup } from '@/api/groupManagement'
import type { GroupCategory } from '@/types'
import { initialOf } from './helpers'

export interface SummaryStat {
  key: string
  label: string
  value: string | number
}

const props = withDefaults(defineProps<{
  group: GroupManagementGroup
  stats: SummaryStat[]
  refreshing: boolean
  // 组管理员 / 超管可以直接切换管理分组的分类
  categoryEditable?: boolean
  categorySaving?: boolean
}>(), {
  categoryEditable: false,
  categorySaving: false
})

const emit = defineEmits<{
  (e: 'refresh'): void
  (e: 'open-settings'): void
  (e: 'update-category', category: GroupCategory): void
}>()

const { t } = useI18n()

const headingId = computed(() => `group-summary-${props.group.id}`)
const managed = computed(() => props.group.kind === 'managed')
const categoryOptions = computed(() => [
  { value: 'enterprise' as const, icon: 'badge' as const, label: t('groupManagement.category.enterprise') },
  { value: 'team' as const, icon: 'users' as const, label: t('groupManagement.category.team') }
])
</script>
