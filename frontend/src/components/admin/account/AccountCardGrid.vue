<template>
  <div class="flex min-h-0 flex-1 flex-col" data-testid="account-card-grid">
    <!-- 卡片视图没有表头：全选本页与排序放在网格上方，不随卡片滚动 -->
    <div class="toolbar-compact flex flex-shrink-0 flex-wrap items-center justify-between gap-2 border-b border-gray-100 py-2 dark:border-dark-700 lg:px-4">
      <label class="inline-flex cursor-pointer select-none items-center gap-2 text-sm text-gray-600 dark:text-dark-300">
        <input
          type="checkbox"
          class="h-4 w-4 cursor-pointer rounded border-gray-300 text-primary-600 focus:ring-primary-500 disabled:cursor-not-allowed disabled:opacity-50 dark:border-dark-500 dark:bg-dark-800"
          :checked="pageFullySelected"
          :indeterminate="pagePartiallySelected"
          :disabled="accounts.length === 0"
          data-testid="account-cards-select-page"
          @change="emit('toggle-select-page', ($event.target as HTMLInputElement).checked)"
        />
        {{ t('admin.accounts.card.selectPage') }}
      </label>
      <div class="flex items-center gap-2">
        <span class="hidden text-xs text-gray-500 dark:text-dark-400 sm:inline">{{ t('admin.accounts.card.sortBy') }}</span>
        <Select
          class="w-36"
          :model-value="sortKey"
          :options="sortOptions"
          :searchable="false"
          :aria-label="t('admin.accounts.card.sortBy')"
          data-testid="account-cards-sort"
          @update:model-value="selectSortKey"
        />
        <button
          type="button"
          class="btn btn-secondary h-9 px-2.5"
          :title="sortOrderLabel"
          :aria-label="sortOrderLabel"
          data-testid="account-cards-sort-order"
          @click="toggleSortOrder"
        >
          <Icon :name="sortOrder === 'asc' ? 'arrowUp' : 'arrowDown'" size="sm" />
        </button>
      </div>
    </div>

    <!-- 自带纵向滚动：TablePageLayout 的表格容器本身 overflow-hidden -->
    <div
      ref="scrollRef"
      class="min-h-0 flex-1 overflow-y-auto bg-gray-50/60 py-3 dark:bg-dark-950/30 lg:p-4"
      :aria-busy="loading"
      data-testid="account-cards"
    >
      <div v-if="loading && accounts.length === 0" class="account-card-columns grid gap-4">
        <div v-for="index in 6" :key="index" class="skeleton h-72 rounded-xl" data-testid="account-cards-skeleton"></div>
      </div>
      <EmptyState v-else-if="accounts.length === 0" :title="t('empty.noData')" data-testid="account-cards-empty" />
      <div
        v-else
        class="account-card-columns grid gap-4 transition-opacity"
        :class="loading ? 'opacity-60' : ''"
      >
        <template v-for="account in accounts" :key="account.id">
          <slot name="card" :account="account" />
        </template>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import EmptyState from '@/components/common/EmptyState.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Column } from '@/components/common/types'
import type { AccountListItem } from '@/types'

type SortOrder = 'asc' | 'desc'

const props = withDefaults(
  defineProps<{
    accounts: AccountListItem[]
    loading?: boolean
    selectedIds?: number[]
    /** 可排序的列（与表头可排序列同源） */
    sortColumns?: Column[]
    sortKey: string
    sortOrder: SortOrder
  }>(),
  { loading: false, selectedIds: () => [], sortColumns: () => [] }
)

const emit = defineEmits<{
  (e: 'toggle-select-page', checked: boolean): void
  (e: 'sort', key: string, order: SortOrder): void
}>()

const { t } = useI18n()

const scrollRef = ref<HTMLElement | null>(null)

// 换页、排序、筛选都会触发加载：回到顶部（表格加载时骨架屏替换内容，效果相同）。
// 自动刷新走增量合并、不置 loading，不会打断浏览位置。
watch(
  () => props.loading,
  (isLoading) => {
    if (isLoading && scrollRef.value) scrollRef.value.scrollTop = 0
  }
)

const sortOptions = computed(() => props.sortColumns.map((column) => ({ value: column.key, label: column.label })))
const sortOrderLabel = computed(() =>
  props.sortOrder === 'asc' ? t('admin.accounts.card.sortAsc') : t('admin.accounts.card.sortDesc')
)

const selectedSet = computed(() => new Set(props.selectedIds))
const pageSelectedCount = computed(() => props.accounts.filter((account) => selectedSet.value.has(account.id)).length)
const pageFullySelected = computed(() => props.accounts.length > 0 && pageSelectedCount.value === props.accounts.length)
const pagePartiallySelected = computed(() => pageSelectedCount.value > 0 && !pageFullySelected.value)

// 换排序字段时沿用当前方向；方向按钮只翻转方向
function selectSortKey(value: string | number | boolean | null) {
  if (typeof value !== 'string' || !value || value === props.sortKey) return
  emit('sort', value, props.sortOrder)
}

function toggleSortOrder() {
  if (!props.sortKey) return
  emit('sort', props.sortKey, props.sortOrder === 'asc' ? 'desc' : 'asc')
}
</script>

<style scoped>
/* 按容器实际宽度排列：侧栏展开/收起都不会把卡片挤到用量窗口换行；窄屏退化为单列 */
.account-card-columns {
  grid-template-columns: repeat(auto-fill, minmax(min(100%, 20rem), 1fr));
}
</style>
