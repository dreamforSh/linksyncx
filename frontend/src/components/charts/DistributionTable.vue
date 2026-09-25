<template>
  <div class="relative">
    <!-- 首次加载骨架；刷新时保留旧数据并半透明，避免版面跳动 -->
    <div v-if="loading && rows.length === 0" class="space-y-3 px-5 py-3" aria-hidden="true">
      <div v-for="i in 5" :key="i" class="space-y-1.5">
        <div class="flex justify-between gap-4">
          <div class="skeleton h-3.5 w-40 rounded" />
          <div class="skeleton h-3.5 w-24 rounded" />
        </div>
        <div class="skeleton h-1 w-full rounded-full" />
      </div>
    </div>

    <div
      v-else-if="rows.length === 0"
      class="flex min-h-[10rem] items-center justify-center px-5 text-sm text-gray-500 dark:text-dark-400"
    >
      {{ emptyText || t('admin.dashboard.noDataAvailable') }}
    </div>

    <div v-else class="overflow-x-auto transition-opacity" :class="loading ? 'opacity-50' : ''">
      <table class="w-full min-w-[30rem] text-[13px]">
        <thead>
          <tr class="text-xs text-gray-500 dark:text-dark-400">
            <th scope="col" class="pb-2 pl-5 pr-3 text-left font-medium">{{ nameHeader }}</th>
            <th
              v-for="col in columns"
              :key="col"
              scope="col"
              class="whitespace-nowrap px-2.5 pb-2 text-right font-medium last:pr-5"
            >
              {{ columnLabel(col) }}
            </th>
          </tr>
        </thead>
        <tbody>
          <template v-for="(row, rowIndex) in visibleRows" :key="row.key">
            <tr
              class="border-t border-gray-100 transition-colors dark:border-dark-800"
              :class="rowInteractive(row) ? 'cursor-pointer hover:bg-gray-50 dark:hover:bg-dark-800/60' : ''"
              @click="onRowClick(row)"
            >
              <td class="w-full max-w-0 py-1.5 pl-5 pr-3">
                <div class="flex min-w-0 items-center gap-1.5">
                  <span
                    v-if="ranked"
                    class="w-5 shrink-0 text-xs font-medium tabular-nums text-gray-500 dark:text-dark-400"
                    aria-hidden="true"
                  >{{ row.muted ? 'Σ' : rowIndex + 1 }}</span>
                  <button
                    v-if="rowInteractive(row)"
                    type="button"
                    class="-ml-1 flex min-w-0 items-center gap-1 rounded text-left font-medium text-gray-900 hover:text-primary-700 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/50 dark:text-gray-100 dark:hover:text-primary-300"
                    :aria-expanded="expandable ? expandedKey === row.key : undefined"
                    :title="row.label"
                    @click.stop="onRowClick(row)"
                  >
                    <Icon
                      v-if="expandable"
                      name="chevronRight"
                      size="xs"
                      class="h-3 w-3 shrink-0 text-gray-400 transition-transform duration-150"
                      :class="expandedKey === row.key ? 'rotate-90' : ''"
                      :stroke-width="2.5"
                    />
                    <span class="truncate">{{ row.label }}</span>
                  </button>
                  <span
                    v-else
                    class="truncate font-medium"
                    :class="row.muted ? 'text-gray-500 dark:text-dark-400' : 'text-gray-900 dark:text-gray-100'"
                    :title="row.label"
                  >{{ row.label }}</span>
                </div>
                <!-- 占比条：名义类别只用一种颜色，长度即数值，不再二次编码 -->
                <div class="mt-1 flex items-center gap-2">
                  <div class="h-1 flex-1 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-800">
                    <div
                      class="h-full rounded-full"
                      :class="row.muted ? 'bg-gray-300 dark:bg-dark-600' : 'bg-primary-500 dark:bg-primary-400'"
                      :style="{ width: `${shareWidth(row)}%` }"
                    />
                  </div>
                  <span class="w-11 shrink-0 text-right text-[11px] tabular-nums text-gray-500 dark:text-dark-400">
                    {{ shareText(row) }}
                  </span>
                </div>
              </td>
              <td
                v-for="col in columns"
                :key="col"
                class="whitespace-nowrap px-2.5 py-1.5 text-right tabular-nums last:pr-5"
                :class="cellClass(col)"
              >
                {{ formatCell(col, row) }}
              </td>
            </tr>
            <!-- 下钻：按用户拆分，与父表同列对齐 -->
            <template v-if="expandable && expandedKey === row.key">
              <tr v-if="breakdownLoading" class="bg-gray-50/70 dark:bg-dark-800/40">
                <td :colspan="columns.length + 1" class="py-2.5 pl-10 text-xs text-gray-500 dark:text-dark-400">
                  <span class="spinner mr-2 inline-block h-3.5 w-3.5 align-[-2px]" aria-hidden="true" />{{ t('common.loading') }}
                </td>
              </tr>
              <tr v-else-if="breakdownItems.length === 0" class="bg-gray-50/70 dark:bg-dark-800/40">
                <td :colspan="columns.length + 1" class="py-2.5 pl-10 text-xs text-gray-400 dark:text-dark-500">
                  {{ t('admin.dashboard.noDataAvailable') }}
                </td>
              </tr>
              <template v-else>
                <tr
                  v-for="user in breakdownItems"
                  :key="`${row.key}-${user.user_id}`"
                  class="bg-gray-50/70 text-xs dark:bg-dark-800/40"
                >
                  <td class="max-w-0 py-1.5 pl-10 pr-3">
                    <span class="block truncate text-gray-600 dark:text-dark-300" :title="user.email">
                      {{ user.email || t('admin.redeem.userPrefix', { id: user.user_id }) }}
                    </span>
                  </td>
                  <td
                    v-for="col in columns"
                    :key="col"
                    class="whitespace-nowrap px-2.5 py-1.5 text-right tabular-nums text-gray-500 last:pr-5 dark:text-dark-400"
                  >
                    {{ formatBreakdownCell(col, user) }}
                  </td>
                </tr>
              </template>
            </template>
          </template>
        </tbody>
      </table>
    </div>

    <div
      v-if="rows.length > limit"
      class="border-t border-gray-100 px-5 py-2 dark:border-dark-800"
    >
      <button
        type="button"
        class="inline-flex items-center gap-1 text-xs font-medium text-primary-700 hover:text-primary-800 dark:text-primary-300 dark:hover:text-primary-200"
        :aria-expanded="showAll"
        @click="showAll = !showAll"
      >
        {{ showAll ? t('common.collapse') : t('admin.dashboard.distribution.showAll', { count: rows.length }) }}
        <Icon name="chevronDown" size="xs" class="h-3.5 w-3.5 transition-transform" :class="showAll ? 'rotate-180' : ''" :stroke-width="2" />
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { UserBreakdownItem } from '@/types'
import { formatCompact, formatUSD } from './chartTheme'

export type DistributionColumn = 'requests' | 'tokens' | 'actual' | 'accountCost' | 'standard'

export interface DistributionRow {
  key: string
  label: string
  requests: number
  tokens: number
  actual: number
  accountCost?: number
  standard?: number
  /** “其他”等汇总行：灰色、不可点击 */
  muted?: boolean
  /** 正常显示但不可展开/点击（如“无分组”） */
  disabled?: boolean
}

const props = withDefaults(
  defineProps<{
    rows: DistributionRow[]
    nameHeader: string
    /** 决定占比条长度与强调列：tokens | actual_cost */
    metric?: 'tokens' | 'actual_cost'
    columns?: DistributionColumn[]
    loading?: boolean
    expandable?: boolean
    clickable?: boolean
    expandedKey?: string | null
    breakdownItems?: UserBreakdownItem[]
    breakdownLoading?: boolean
    limit?: number
    emptyText?: string
    /** 名称前显示名次（排行视图） */
    ranked?: boolean
    columnLabels?: Partial<Record<DistributionColumn, string>>
  }>(),
  {
    ranked: false,
    columnLabels: () => ({}),
    metric: 'tokens',
    columns: () => ['requests', 'tokens', 'actual', 'standard'],
    loading: false,
    expandable: false,
    clickable: false,
    expandedKey: null,
    breakdownItems: () => [],
    breakdownLoading: false,
    limit: 8,
    emptyText: ''
  }
)

const emit = defineEmits<{
  toggle: [key: string]
  select: [key: string]
}>()

const { t } = useI18n()
const showAll = ref(false)

const visibleRows = computed(() => (showAll.value ? props.rows : props.rows.slice(0, props.limit)))

const metricValue = (row: DistributionRow) => (props.metric === 'actual_cost' ? row.actual : row.tokens)

const total = computed(() => props.rows.reduce((sum, row) => sum + (Number(metricValue(row)) || 0), 0))

const shareOf = (row: DistributionRow) => (total.value > 0 ? (Number(metricValue(row)) || 0) / total.value : 0)

// 占比条以最大项为满格，小项仍可辨认；文字显示真实占比
const maxShare = computed(() => props.rows.reduce((max, row) => Math.max(max, shareOf(row)), 0))

const shareWidth = (row: DistributionRow) => {
  if (maxShare.value <= 0) return 0
  const width = (shareOf(row) / maxShare.value) * 100
  return width > 0 ? Math.max(width, 1.5) : 0
}

const shareText = (row: DistributionRow) => {
  const share = shareOf(row) * 100
  if (share > 0 && share < 0.1) return '<0.1%'
  return `${share.toFixed(1)}%`
}

const rowInteractive = (row: DistributionRow) =>
  !row.muted && !row.disabled && (props.expandable || props.clickable)

function onRowClick(row: DistributionRow) {
  if (!rowInteractive(row)) return
  if (props.expandable) emit('toggle', row.key)
  else emit('select', row.key)
}

function columnLabel(col: DistributionColumn) {
  const override = props.columnLabels[col]
  if (override) return override
  switch (col) {
    case 'requests':
      return t('admin.dashboard.requests')
    case 'tokens':
      return t('admin.dashboard.tokens')
    case 'actual':
      return t('admin.dashboard.actual')
    case 'accountCost':
      return t('admin.dashboard.accountCost')
    case 'standard':
      return t('admin.dashboard.standard')
  }
}

function formatCell(col: DistributionColumn, row: DistributionRow) {
  switch (col) {
    case 'requests':
      return (Number(row.requests) || 0).toLocaleString()
    case 'tokens':
      return formatCompact(row.tokens)
    case 'actual':
      return formatUSD(row.actual)
    case 'accountCost':
      return formatUSD(row.accountCost ?? 0)
    case 'standard':
      return formatUSD(row.standard ?? 0)
  }
}

function formatBreakdownCell(col: DistributionColumn, user: UserBreakdownItem) {
  switch (col) {
    case 'requests':
      return (Number(user.requests) || 0).toLocaleString()
    case 'tokens':
      return formatCompact(user.total_tokens)
    case 'actual':
      return formatUSD(user.actual_cost)
    case 'accountCost':
      return formatUSD(user.account_cost)
    case 'standard':
      return formatUSD(user.cost)
  }
}

// 文字只用中性墨色：当前度量列加粗，其余弱化
function cellClass(col: DistributionColumn) {
  const emphasized =
    (col === 'tokens' && props.metric === 'tokens') || (col === 'actual' && props.metric === 'actual_cost')
  if (emphasized) return 'font-semibold text-gray-900 dark:text-white'
  if (col === 'standard') return 'text-gray-500 dark:text-dark-400'
  return 'text-gray-600 dark:text-dark-300'
}
</script>
