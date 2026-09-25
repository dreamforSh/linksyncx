<template>
  <aside class="min-w-0 min-[1400px]:sticky min-[1400px]:top-24 min-[1400px]:self-start">
    <div class="card p-2">
      <div class="flex items-center justify-between gap-2 px-2 pb-2 pt-1">
        <h2 class="text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-dark-400">
          {{ t('groupManagement.groups.title') }}
        </h2>
        <span class="badge badge-gray tabular-nums">{{ groups.length }}</span>
      </div>

      <div v-if="searchable" class="relative px-1 pb-2">
        <Icon
          name="search"
          size="sm"
          class="pointer-events-none absolute left-4 top-1/2 -translate-y-1/2 text-gray-400 dark:text-dark-400"
        />
        <input
          v-model.trim="query"
          type="search"
          class="input py-2 pl-9"
          :placeholder="t('groupManagement.groups.search')"
          :aria-label="t('groupManagement.groups.search')"
        />
      </div>

      <nav
        :aria-label="t('groupManagement.groups.title')"
        class="-mx-0.5 flex gap-1.5 overflow-x-auto px-0.5 pb-1 min-[1400px]:max-h-[calc(100vh-15rem)] min-[1400px]:flex-col min-[1400px]:gap-1 min-[1400px]:overflow-y-auto min-[1400px]:overflow-x-hidden"
      >
        <button
          v-for="group in filteredGroups"
          :key="group.id"
          type="button"
          :aria-current="group.id === modelValue ? 'true' : undefined"
          class="flex min-w-[13rem] shrink-0 items-center gap-3 rounded-xl px-3 py-2.5 text-left transition-colors duration-150 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/50 min-[1400px]:min-w-0"
          :class="group.id === modelValue
            ? 'bg-primary-50 text-primary-700 ring-1 ring-inset ring-primary-200 dark:bg-primary-900/20 dark:text-primary-300 dark:ring-primary-800/60'
            : 'text-gray-700 hover:bg-gray-50 dark:text-gray-300 dark:hover:bg-dark-700/60'"
          @click="emit('update:modelValue', group.id)"
        >
          <span
            class="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg text-sm font-semibold"
            :class="group.id === modelValue
              ? 'bg-primary-500 text-white'
              : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-200'"
            aria-hidden="true"
          >
            {{ initialOf(group.name) }}
          </span>
          <span class="min-w-0 flex-1">
            <span class="block truncate text-sm font-medium">{{ group.name }}</span>
            <span class="mt-0.5 flex items-center gap-1.5 text-xs text-gray-500 dark:text-dark-400">
              <span
                class="h-1.5 w-1.5 shrink-0 rounded-full"
                :class="group.enabled ? 'bg-emerald-500' : 'bg-gray-300 dark:bg-dark-500'"
                aria-hidden="true"
              />
              <span class="truncate">
                <template v-if="group.kind === 'managed' && group.category">{{ categoryLabel(group.category) }} · </template>
                {{ group.enabled ? t('groupManagement.state.enabled') : t('groupManagement.state.disabled') }}
                · {{ group.manager ? t('groupManagement.state.manager') : t('groupManagement.state.member') }}
              </span>
            </span>
          </span>
        </button>
        <p v-if="!filteredGroups.length" class="px-3 py-6 text-center text-sm text-gray-500 dark:text-dark-400">
          {{ t('groupManagement.groups.noMatch') }}
        </p>
      </nav>
    </div>
  </aside>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { GroupManagementGroup } from '@/api/groupManagement'
import { initialOf } from './helpers'

const props = defineProps<{
  groups: GroupManagementGroup[]
  modelValue: number | null
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', id: number): void
}>()

const { t } = useI18n()

function categoryLabel(category: string) {
  return category === 'enterprise' ? t('groupManagement.category.enterprise') : t('groupManagement.category.team')
}

const SEARCH_THRESHOLD = 6
const query = ref('')
const searchable = computed(() => props.groups.length > SEARCH_THRESHOLD)
const filteredGroups = computed(() => {
  const keyword = query.value.toLowerCase()
  if (!keyword) return props.groups
  return props.groups.filter(group => group.name.toLowerCase().includes(keyword) || String(group.id) === keyword)
})
</script>
