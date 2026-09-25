<template>
  <section class="card overflow-hidden">
    <header class="flex flex-wrap items-center gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
      <div class="w-full sm:w-64">
        <Select
          :model-value="memberFilter"
          :options="memberOptions"
          :aria-label="t('groupManagement.transfers.filterLabel')"
          @update:model-value="selectMember"
        />
      </div>
      <span class="ml-auto text-xs tabular-nums text-gray-500 dark:text-dark-400">
        {{ t('common.total') }} {{ total }}
      </span>
    </header>

    <div v-if="loading && !items.length" class="space-y-3 p-5" role="status" :aria-label="t('common.loading')">
      <div v-for="index in 4" :key="index" class="skeleton h-10 rounded-lg" />
    </div>

    <div v-else-if="!items.length" class="empty-state">
      <div class="mb-4 flex h-14 w-14 items-center justify-center rounded-2xl bg-gray-100 dark:bg-dark-700">
        <Icon name="dollar" size="lg" class="text-gray-400 dark:text-dark-400" />
      </div>
      <h3 class="empty-state-title text-base">{{ t('groupManagement.transfers.empty') }}</h3>
      <p class="empty-state-description">{{ t('groupManagement.transfers.emptyHint') }}</p>
    </div>

    <div v-else class="overflow-x-auto" :class="loading && 'opacity-60'">
      <table class="table min-w-[720px]">
        <thead>
          <tr class="whitespace-nowrap">
            <th scope="col">{{ t('groupManagement.transfers.time') }}</th>
            <th scope="col">{{ t('groupManagement.transfers.member') }}</th>
            <th scope="col">{{ t('groupManagement.transfers.direction') }}</th>
            <th scope="col" class="!text-right">{{ t('groupManagement.transfers.amount') }}</th>
            <th scope="col">{{ t('groupManagement.transfers.operator') }}</th>
            <th scope="col">{{ t('groupManagement.transfers.notes') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="item in items" :key="item.id">
            <td class="whitespace-nowrap text-sm text-gray-600 dark:text-dark-300">{{ formatDateTime(item.created_at) }}</td>
            <td class="max-w-[12rem] truncate font-medium text-gray-900 dark:text-white">{{ item.member_name || t('groupManagement.transfers.deletedUser') }}</td>
            <td>
              <span class="badge" :class="item.direction === 'grant' ? 'badge-success' : 'badge-warning'">
                <Icon :name="item.direction === 'grant' ? 'arrowRight' : 'arrowLeft'" size="xs" />
                {{ item.direction === 'grant' ? t('groupManagement.transfers.grant') : t('groupManagement.transfers.reclaim') }}
              </span>
            </td>
            <td
              class="whitespace-nowrap text-right font-semibold tabular-nums"
              :class="item.direction === 'grant' ? 'text-emerald-600 dark:text-emerald-400' : 'text-amber-600 dark:text-amber-400'"
            >
              {{ item.direction === 'grant' ? '+' : '-' }}{{ formatCurrency(item.amount) }}
            </td>
            <td class="max-w-[10rem] truncate text-sm text-gray-600 dark:text-dark-300">{{ item.manager_name || t('groupManagement.transfers.deletedUser') }}</td>
            <td class="max-w-[16rem] truncate text-sm text-gray-500 dark:text-dark-400" :title="item.notes">{{ item.notes || '—' }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <footer
      v-if="pages > 1"
      class="flex items-center justify-between gap-3 border-t border-gray-100 px-5 py-3 text-sm dark:border-dark-700"
    >
      <button type="button" class="btn btn-secondary btn-sm" :disabled="page <= 1 || loading" @click="go(page - 1)">
        {{ t('groupManagement.transfers.prev') }}
      </button>
      <span class="tabular-nums text-gray-500 dark:text-dark-400">{{ t('groupManagement.transfers.page', { page, pages }) }}</span>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="page >= pages || loading" @click="go(page + 1)">
        {{ t('groupManagement.transfers.next') }}
      </button>
    </footer>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import Select from '@/components/common/Select.vue'
import { groupManagementAPI, type GroupBalanceTransfer, type GroupManagementMember } from '@/api/groupManagement'
import { formatCurrency, formatDateTime } from '@/utils/format'
import { memberDisplayName } from './helpers'

const PAGE_SIZE = 20

const props = defineProps<{
  groupId: number
  members: GroupManagementMember[]
  // 父组件在划拨成功后递增，触发重新加载
  refreshKey: number
}>()

const emit = defineEmits<{
  (e: 'error', error: unknown): void
}>()

const { t } = useI18n()

const items = ref<GroupBalanceTransfer[]>([])
const total = ref(0)
const page = ref(1)
const pages = ref(1)
const loading = ref(false)
const memberFilter = ref<number>(0)
let request = 0

const memberOptions = computed(() => [
  { value: 0, label: t('groupManagement.transfers.filterAll') },
  ...props.members.map(member => ({ value: member.user_id, label: `${memberDisplayName(member)} · #${member.user_id}` }))
])

async function load() {
  const current = ++request
  loading.value = true
  try {
    const result = await groupManagementAPI.listTransfers(props.groupId, {
      page: page.value,
      page_size: PAGE_SIZE,
      ...(memberFilter.value > 0 ? { user_id: memberFilter.value } : {})
    })
    if (current !== request) return
    items.value = result.items
    total.value = result.total
    pages.value = Math.max(result.pages, 1)
  } catch (error) {
    if (current === request) emit('error', error)
  } finally {
    if (current === request) loading.value = false
  }
}

function go(next: number) {
  page.value = next
  void load()
}

function selectMember(value: string | number | boolean | null) {
  memberFilter.value = typeof value === 'number' ? value : 0
  page.value = 1
  void load()
}

watch(() => [props.groupId, props.refreshKey] as const, ([groupId], previous) => {
  if (previous && previous[0] !== groupId) memberFilter.value = 0
  page.value = 1
  void load()
}, { immediate: true })
</script>
