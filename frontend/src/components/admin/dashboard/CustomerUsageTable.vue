<template>
  <section class="card flex flex-col">
    <header class="card-section-header">
      <div class="min-w-0">
        <h3 class="card-section-title">{{ t('admin.dashboard.customers.title') }}</h3>
        <p class="card-section-subtitle">{{ t('admin.dashboard.customers.subtitle', { count: users.length }) }}</p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <SegmentedControl
          :model-value="sortBy"
          size="sm"
          :options="sortOptions"
          :aria-label="t('admin.dashboard.customers.sortBy')"
          @update:model-value="(value) => emit('update:sortBy', value as CustomerSortKey)"
        />
        <slot name="actions" />
      </div>
    </header>

    <div class="px-2 pb-3">
      <div v-if="loading && users.length === 0" class="space-y-2 px-3 py-2" aria-hidden="true">
        <div v-for="i in 6" :key="i" class="skeleton h-5 w-full rounded" />
      </div>
      <div v-else-if="error && users.length === 0" class="flex min-h-[10rem] items-center justify-center text-sm text-gray-500 dark:text-dark-400">
        {{ t('admin.dashboard.failedToLoad') }}
      </div>
      <div v-else-if="users.length === 0" class="flex min-h-[10rem] items-center justify-center text-sm text-gray-500 dark:text-dark-400">
        {{ t('admin.dashboard.noDataAvailable') }}
      </div>
      <div v-else class="overflow-x-auto transition-opacity" :class="loading ? 'opacity-50' : ''">
        <table class="w-full min-w-[52rem] text-xs">
          <caption class="sr-only">{{ t('admin.dashboard.customers.title') }}</caption>
          <thead>
            <tr class="text-gray-500 dark:text-dark-400">
              <th scope="col" class="w-8 px-3 py-2 text-left font-medium">#</th>
              <th scope="col" class="px-3 py-2 text-left font-medium">{{ t('admin.dashboard.customers.customer') }}</th>
              <th scope="col" class="w-[18%] px-3 py-2 text-left font-medium">{{ t('admin.dashboard.customers.revenueShare') }}</th>
              <th scope="col" class="px-3 py-2 text-right font-medium">{{ t('admin.dashboard.customers.requests') }}</th>
              <th scope="col" class="px-3 py-2 text-right font-medium">{{ t('admin.dashboard.customers.tokens') }}</th>
              <th scope="col" class="px-3 py-2 text-right font-medium">{{ t('admin.dashboard.customers.cacheHit') }}</th>
              <th scope="col" class="px-3 py-2 text-right font-medium">{{ t('admin.dashboard.customers.revenue') }}</th>
              <th scope="col" class="px-3 py-2 text-right font-medium">{{ t('admin.dashboard.customers.accountCost') }}</th>
              <th scope="col" class="px-3 py-2 text-right font-medium">{{ t('admin.dashboard.customers.profit') }}</th>
              <th scope="col" class="px-3 py-2 text-right font-medium">{{ t('admin.dashboard.customers.margin') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="(row, index) in rows"
              :key="row.userId"
              class="cursor-pointer border-t border-gray-100 transition-colors hover:bg-gray-50 focus-within:bg-gray-50 dark:border-dark-800 dark:hover:bg-dark-800/60 dark:focus-within:bg-dark-800/60"
              @click="emit('select', row.userId)"
            >
              <td class="px-3 py-2 tabular-nums text-gray-500 dark:text-dark-400">{{ index + 1 }}</td>
              <th scope="row" class="max-w-[16rem] px-3 py-2 text-left font-normal">
                <button
                  type="button"
                  class="block w-full truncate text-left text-gray-900 hover:text-primary-700 focus:outline-none focus-visible:underline dark:text-gray-100 dark:hover:text-primary-300"
                  :title="row.email"
                  @click.stop="emit('select', row.userId)"
                >{{ row.email || `#${row.userId}` }}</button>
              </th>
              <td class="px-3 py-2">
                <span class="flex items-center gap-2">
                  <span class="h-1.5 flex-1 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-800" aria-hidden="true">
                    <span
                      class="block h-full rounded-full"
                      :style="{ width: `${Math.max(row.share * 100, row.share > 0 ? 1.5 : 0)}%`, backgroundColor: theme.accent }"
                    />
                  </span>
                  <span class="w-12 text-right tabular-nums text-gray-600 dark:text-dark-300">{{ formatPercent(row.share) }}</span>
                </span>
              </td>
              <td class="whitespace-nowrap px-3 py-2 text-right tabular-nums text-gray-900 dark:text-gray-100">{{ formatCompact(row.requests) }}</td>
              <td class="whitespace-nowrap px-3 py-2 text-right tabular-nums text-gray-900 dark:text-gray-100">{{ formatCompact(row.tokens) }}</td>
              <td class="whitespace-nowrap px-3 py-2 text-right tabular-nums text-gray-900 dark:text-gray-100">{{ formatRatio(row.cacheHit) }}</td>
              <td class="whitespace-nowrap px-3 py-2 text-right font-medium tabular-nums text-gray-900 dark:text-gray-100">{{ formatUSD(row.revenue) }}</td>
              <td class="whitespace-nowrap px-3 py-2 text-right tabular-nums text-gray-600 dark:text-dark-300">{{ formatUSD(row.accountCost) }}</td>
              <td class="whitespace-nowrap px-3 py-2 text-right tabular-nums text-gray-900 dark:text-gray-100">{{ formatUSD(row.profit) }}</td>
              <td class="whitespace-nowrap px-3 py-2 text-right tabular-nums text-gray-900 dark:text-gray-100">{{ formatRatio(row.margin) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import type { UserBreakdownItem } from '@/types'
import {
  cacheHitRatio,
  formatCompact,
  formatPercent,
  formatUSD,
  marginRatio,
  useChartTheme
} from '@/components/charts/chartTheme'

type CustomerSortKey = 'actual_cost' | 'total_tokens' | 'requests'

const props = withDefaults(
  defineProps<{
    users: UserBreakdownItem[]
    sortBy: CustomerSortKey
    /** 区间总营收，用于占比；缺省时按当前列出的客户合计 */
    totalRevenue?: number
    loading?: boolean
    error?: boolean
  }>(),
  { totalRevenue: 0, loading: false, error: false }
)

const emit = defineEmits<{
  'update:sortBy': [value: CustomerSortKey]
  select: [userId: number]
}>()

const { t } = useI18n()
const theme = useChartTheme()

const sortOptions = computed(() => [
  { value: 'actual_cost' as CustomerSortKey, label: t('admin.dashboard.customers.byRevenue') },
  { value: 'total_tokens' as CustomerSortKey, label: t('admin.dashboard.customers.byTokens') },
  { value: 'requests' as CustomerSortKey, label: t('admin.dashboard.customers.byRequests') }
])

const num = (v: unknown) => {
  const n = Number(v)
  return Number.isFinite(n) ? n : 0
}
const formatRatio = (ratio: number | null) => (ratio === null ? '—' : formatPercent(ratio))

const rows = computed(() => {
  const list = props.users ?? []
  const listed = list.reduce((acc, u) => acc + num(u.actual_cost), 0)
  const total = props.totalRevenue > 0 ? Math.max(props.totalRevenue, listed) : listed
  return list.map((u) => {
    const revenue = num(u.actual_cost)
    const accountCost = num(u.account_cost)
    const cacheRead = num(u.cache_read_tokens)
    // 旧版接口只返回缓存合计，拿不到读写拆分时不显示命中率
    const hasSplit = u.cache_read_tokens !== undefined && u.cache_creation_tokens !== undefined
    return {
      userId: u.user_id,
      email: u.email,
      requests: num(u.requests),
      tokens: num(u.total_tokens),
      cacheHit: hasSplit ? cacheHitRatio(u.input_tokens, u.cache_creation_tokens, cacheRead) : null,
      revenue,
      accountCost,
      profit: revenue - accountCost,
      margin: marginRatio(revenue, accountCost),
      share: total > 0 ? revenue / total : 0
    }
  })
})
</script>
