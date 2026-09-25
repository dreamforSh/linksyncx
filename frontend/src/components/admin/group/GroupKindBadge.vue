<template>
  <span
    v-if="managed || showChannel"
    class="inline-flex items-center gap-1 whitespace-nowrap rounded-md px-2 py-0.5 text-xs font-medium ring-1 ring-inset"
    :class="toneClass"
  >
    <Icon :name="managed ? 'shield' : 'server'" size="xs" />
    <span>{{ managed ? t('admin.groups.groupKind.managed') : t('admin.groups.groupKind.channel') }}</span>
    <template v-if="managed && categoryLabel">
      <span class="opacity-40" aria-hidden="true">·</span>
      <span>{{ categoryLabel }}</span>
    </template>
    <template v-if="managed && managedTypeLabel">
      <span class="opacity-40" aria-hidden="true">·</span>
      <span data-testid="group-managed-type-label">{{ managedTypeLabel }}</span>
    </template>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { GroupCategory, GroupKind, ManagedGroupType } from '@/types'

const props = withDefaults(defineProps<{
  kind?: GroupKind
  category?: GroupCategory | null
  // 管理分组类型：额度组 / 订阅组
  managedType?: ManagedGroupType | null
  // 渠道分组默认不显示徽章，避免列表里满屏「渠道分组」
  showChannel?: boolean
}>(), {
  kind: 'channel',
  category: null,
  managedType: null,
  showChannel: false
})

const { t } = useI18n()

const managed = computed(() => props.kind === 'managed')

const categoryLabel = computed(() => {
  if (props.category === 'enterprise') return t('admin.groups.groupKind.enterprise')
  if (props.category === 'team') return t('admin.groups.groupKind.team')
  return ''
})

const managedTypeLabel = computed(() => {
  if (props.managedType === 'quota') return t('admin.groups.groupKind.quota')
  if (props.managedType === 'subscription') return t('admin.groups.groupKind.subscription')
  return ''
})

const toneClass = computed(() => {
  if (!managed.value) {
    return 'bg-gray-50 text-gray-600 ring-gray-200 dark:bg-dark-700/60 dark:text-dark-300 dark:ring-dark-600'
  }
  if (props.category === 'team') {
    return 'bg-teal-50 text-teal-700 ring-teal-200 dark:bg-teal-500/10 dark:text-teal-300 dark:ring-teal-500/30'
  }
  return 'bg-indigo-50 text-indigo-700 ring-indigo-200 dark:bg-indigo-500/10 dark:text-indigo-300 dark:ring-indigo-500/30'
})
</script>
