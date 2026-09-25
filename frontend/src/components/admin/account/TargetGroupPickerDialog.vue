<template>
  <BaseDialog
    :show="show"
    :title="t('admin.accounts.targetGroup.title')"
    width="normal"
    :z-index="zIndex"
    @close="emit('close')"
  >
    <div class="space-y-4">
      <p class="text-sm leading-relaxed text-gray-600 dark:text-dark-300">
        {{ t('admin.accounts.targetGroup.description') }}
      </p>

      <div
        role="tablist"
        :aria-label="t('admin.groups.groupKind.label')"
        class="grid grid-cols-2 gap-1 rounded-lg bg-gray-100 p-1 dark:bg-dark-700"
        @keydown="onTabKeydown"
      >
        <button
          v-for="tab in tabs"
          :id="`target-group-tab-${tab.kind}`"
          :key="tab.kind"
          type="button"
          role="tab"
          :aria-selected="activeKind === tab.kind"
          aria-controls="target-group-panel"
          :tabindex="activeKind === tab.kind ? 0 : -1"
          :data-testid="`target-group-tab-${tab.kind}`"
          class="flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium transition-colors duration-150 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/50"
          :class="activeKind === tab.kind
            ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-600 dark:text-white'
            : 'text-gray-600 hover:text-gray-900 dark:text-dark-300 dark:hover:text-white'"
          @click="switchKind(tab.kind)"
        >
          <Icon :name="tab.icon" size="sm" />
          {{ tab.label }}
          <span
            class="rounded-full px-1.5 py-px text-[11px] font-semibold tabular-nums"
            :class="activeKind === tab.kind
              ? 'bg-primary-100 text-primary-700 dark:bg-primary-900/40 dark:text-primary-300'
              : 'bg-gray-200/70 text-gray-600 dark:bg-dark-600 dark:text-dark-200'"
          >
            {{ tab.count }}
          </span>
        </button>
      </div>

      <div id="target-group-panel" role="tabpanel" :aria-labelledby="`target-group-tab-${activeKind}`" class="space-y-3">
        <p class="text-xs leading-relaxed text-gray-500 dark:text-dark-400">
          {{ activeKind === 'managed' ? t('admin.groups.groupKind.managedDesc') : t('admin.groups.groupKind.channelDesc') }}
        </p>

        <div v-if="kindGroups.length > searchThreshold" class="relative">
          <Icon name="search" size="sm" class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
          <input
            v-model="search"
            type="search"
            class="input pl-9"
            :placeholder="t('admin.accounts.targetGroup.search')"
            :aria-label="t('admin.accounts.targetGroup.search')"
            data-testid="target-group-search"
          />
        </div>

        <div
          v-if="!kindGroups.length"
          class="flex flex-col items-center rounded-xl border border-dashed border-gray-300 px-6 py-8 text-center dark:border-dark-600"
          data-testid="target-group-empty"
        >
          <span class="mb-3 flex h-11 w-11 items-center justify-center rounded-xl bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-dark-300">
            <Icon name="inbox" size="md" />
          </span>
          <p class="text-sm font-medium text-gray-900 dark:text-white">
            {{ activeKind === 'managed' ? t('admin.accounts.targetGroup.emptyManaged') : t('admin.accounts.targetGroup.emptyChannel') }}
          </p>
          <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.targetGroup.emptyHint') }}</p>
          <button type="button" class="btn btn-primary btn-sm mt-4" @click="emit('create-group', activeKind)">
            <Icon name="plus" size="sm" />
            {{ t('admin.accounts.targetGroup.createGroup') }}
          </button>
        </div>

        <p v-else-if="!visibleGroups.length" class="py-6 text-center text-sm text-gray-500 dark:text-dark-400">
          {{ t('admin.accounts.targetGroup.noMatch') }}
        </p>

        <fieldset v-else data-tour="account-target-group-list">
          <legend class="sr-only">{{ t('admin.accounts.targetGroup.title') }}</legend>
          <ul class="max-h-80 divide-y divide-gray-100 overflow-y-auto rounded-xl border border-gray-200 dark:divide-dark-700 dark:border-dark-600">
            <li v-for="group in visibleGroups" :key="group.id">
              <label
                class="flex cursor-pointer items-center gap-3 px-4 py-3 transition-colors duration-150 hover:bg-gray-50 dark:hover:bg-dark-700/50"
                :class="selectedId === group.id && 'bg-primary-50/60 dark:bg-primary-900/10'"
                :data-testid="`target-group-option-${group.id}`"
                @dblclick="confirm(group)"
              >
                <input
                  v-model="selectedId"
                  type="radio"
                  name="account-target-group"
                  :value="group.id"
                  class="h-4 w-4 shrink-0 cursor-pointer accent-primary-600"
                />
                <span class="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-200">
                  <PlatformIcon :platform="group.platform" size="sm" />
                </span>
                <span class="min-w-0 flex-1">
                  <span class="block truncate text-sm font-medium text-gray-900 dark:text-white">{{ group.name }}</span>
                  <span class="block truncate text-xs text-gray-500 dark:text-dark-400">
                    {{ platformLabel(group.platform) }}<template v-if="group.description"> · {{ group.description }}</template>
                  </span>
                </span>
                <GroupKindBadge :kind="groupKindOf(group)" :category="group.category" />
                <span
                  v-if="group.status !== 'active'"
                  class="whitespace-nowrap rounded-md bg-gray-100 px-2 py-0.5 text-xs text-gray-500 dark:bg-dark-700 dark:text-dark-400"
                >
                  {{ t('admin.accounts.targetGroup.inactive') }}
                </span>
                <span class="hidden whitespace-nowrap text-xs tabular-nums text-gray-500 dark:text-dark-400 sm:inline">
                  {{ t('admin.accounts.targetGroup.accountsCount', { count: group.account_count ?? 0 }) }}
                </span>
              </label>
            </li>
          </ul>
        </fieldset>
      </div>
    </div>

    <template #footer>
      <button
        v-if="kindGroups.length"
        type="button"
        class="btn btn-ghost mr-auto"
        data-testid="target-group-create"
        @click="emit('create-group', activeKind)"
      >
        <Icon name="plus" size="sm" />
        {{ t('admin.accounts.targetGroup.createGroup') }}
      </button>
      <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
      <button
        type="button"
        class="btn btn-primary"
        :disabled="!selectedGroup"
        data-tour="account-target-group-next"
        data-testid="target-group-next"
        @click="selectedGroup && confirm(selectedGroup)"
      >
        {{ t('admin.accounts.targetGroup.next') }}
        <Icon name="arrowRight" size="sm" />
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import Icon from '@/components/icons/Icon.vue'
import GroupKindBadge from '@/components/admin/group/GroupKindBadge.vue'
import { useAuthStore } from '@/stores'
import type { AdminGroup, GroupKind } from '@/types'
import { groupKindOf } from '@/utils/groupKind'
import { platformLabel } from '@/utils/platformColors'

const props = withDefaults(defineProps<{
  show: boolean
  groups: AdminGroup[]
  initialGroupId?: number | null
  zIndex?: number
}>(), {
  initialGroupId: null,
  zIndex: 50
})

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'select', group: AdminGroup): void
  (e: 'create-group', kind: GroupKind): void
}>()

const { t } = useI18n()
const authStore = useAuthStore()

const searchThreshold = 6
const activeKind = ref<GroupKind>('channel')
const selectedId = ref<number | null>(null)
const search = ref('')

// 简易模式下 composite 分组不能绑定账号；其余模式下 composite 可接收任意平台账号
const candidates = computed(() => {
  const list = authStore.isSimpleMode ? props.groups.filter(group => group.platform !== 'composite') : props.groups
  return [...list].sort((a, b) => Number(b.status === 'active') - Number(a.status === 'active'))
})

const channelGroups = computed(() => candidates.value.filter(group => groupKindOf(group) === 'channel'))
const managedGroups = computed(() => candidates.value.filter(group => groupKindOf(group) === 'managed'))
const kindGroups = computed(() => (activeKind.value === 'managed' ? managedGroups.value : channelGroups.value))

const visibleGroups = computed(() => {
  const query = search.value.trim().toLowerCase()
  if (!query) return kindGroups.value
  return kindGroups.value.filter(group =>
    group.name.toLowerCase().includes(query) || group.description?.toLowerCase().includes(query)
  )
})

const selectedGroup = computed(() => kindGroups.value.find(group => group.id === selectedId.value) ?? null)

const tabs = computed(() => [
  { kind: 'channel' as const, icon: 'server' as const, label: t('admin.accounts.targetGroup.channelTab'), count: channelGroups.value.length },
  { kind: 'managed' as const, icon: 'shield' as const, label: t('admin.accounts.targetGroup.managedTab'), count: managedGroups.value.length }
])

// 只有一个候选时直接选中，省一次点击
function soleCandidateId(kind: GroupKind): number | null {
  const list = kind === 'managed' ? managedGroups.value : channelGroups.value
  return list.length === 1 ? list[0].id : null
}

function switchKind(kind: GroupKind) {
  if (activeKind.value === kind) return
  activeKind.value = kind
  search.value = ''
  if (!kindGroups.value.some(group => group.id === selectedId.value)) {
    selectedId.value = soleCandidateId(kind)
  }
}

function onTabKeydown(event: KeyboardEvent) {
  if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
  event.preventDefault()
  const next: GroupKind = activeKind.value === 'channel' ? 'managed' : 'channel'
  switchKind(next)
  document.getElementById(`target-group-tab-${next}`)?.focus()
}

function confirm(group: AdminGroup) {
  emit('select', group)
}

watch(() => props.show, open => {
  if (!open) return
  search.value = ''
  const initial = props.initialGroupId === null ? undefined : candidates.value.find(group => group.id === props.initialGroupId)
  if (initial) {
    activeKind.value = groupKindOf(initial)
    selectedId.value = initial.id
    return
  }
  activeKind.value = channelGroups.value.length || !managedGroups.value.length ? 'channel' : 'managed'
  selectedId.value = soleCandidateId(activeKind.value)
}, { immediate: true })
</script>
