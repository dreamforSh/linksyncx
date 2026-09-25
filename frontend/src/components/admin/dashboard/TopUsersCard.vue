<template>
  <section class="card flex h-full flex-col">
    <header class="card-section-header">
      <div class="min-w-0">
        <h3 class="card-section-title">{{ t('admin.dashboard.topUsers.title') }}</h3>
        <p class="card-section-subtitle">{{ t('admin.dashboard.topUsers.subtitle') }}</p>
      </div>
      <slot name="actions" />
    </header>

    <div class="flex-1 px-2 pb-2">
      <!-- 首次加载：骨架行；刷新时保留旧数据并降低不透明度，避免版面跳动 -->
      <ul v-if="loading && rows.length === 0" class="space-y-1 p-2" aria-hidden="true">
        <li v-for="i in limit" :key="i" class="flex items-center gap-3 py-2">
          <div class="skeleton h-4 w-4 rounded" />
          <div class="skeleton h-7 w-7 rounded-full" />
          <div class="skeleton h-3.5 flex-1 rounded" />
          <div class="skeleton h-3.5 w-16 rounded" />
        </li>
      </ul>

      <div
        v-else-if="rows.length === 0"
        class="flex h-full min-h-[12rem] items-center justify-center text-sm text-gray-500 dark:text-dark-400"
      >
        {{ t('admin.dashboard.noDataAvailable') }}
      </div>

      <table v-else class="w-full text-sm transition-opacity" :class="loading ? 'opacity-50' : ''">
        <caption class="sr-only">{{ t('admin.dashboard.topUsers.title') }}</caption>
        <thead class="sr-only">
          <tr>
            <th scope="col">#</th>
            <th scope="col">{{ t('admin.dashboard.spendingRankingUser') }}</th>
            <th scope="col">{{ t('admin.dashboard.topUsers.trend') }}</th>
            <th scope="col">{{ t('admin.dashboard.tokens') }}</th>
            <th scope="col">{{ t('admin.dashboard.spendingRankingSpend') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="(row, index) in rows"
            :key="row.userId"
            class="group cursor-pointer rounded-lg transition-colors hover:bg-gray-50 focus-within:bg-gray-50 dark:hover:bg-dark-800/60 dark:focus-within:bg-dark-800/60"
            @click="emit('select', row.userId)"
          >
            <td class="w-8 py-2 pl-2 text-xs font-medium tabular-nums text-gray-500 dark:text-dark-400">{{ index + 1 }}</td>
            <td class="max-w-0 py-2 pr-3">
              <button
                type="button"
                class="flex w-full min-w-0 items-center gap-2.5 text-left focus:outline-none"
                :title="row.email || row.name"
                @click.stop="emit('select', row.userId)"
              >
                <span
                  class="flex h-7 w-7 shrink-0 items-center justify-center rounded-full text-[11px] font-semibold"
                  :class="avatarClass(row.userId)"
                  aria-hidden="true"
                >{{ initials(row.name) }}</span>
                <span class="min-w-0">
                  <span class="block truncate font-medium text-gray-900 group-hover:text-primary-700 dark:text-gray-100 dark:group-hover:text-primary-300">{{ row.name }}</span>
                  <span v-if="row.email && row.email !== row.name" class="block truncate text-xs text-gray-500 dark:text-dark-400">{{ row.email }}</span>
                </span>
              </button>
            </td>
            <td class="hidden w-28 py-2 pr-3 sm:table-cell">
              <Sparkline :values="row.series" :height="24" :aria-label="t('admin.dashboard.topUsers.trend')" />
            </td>
            <td class="w-20 py-2 pr-3 text-right font-medium tabular-nums text-gray-900 dark:text-gray-100">{{ formatCompact(row.tokens) }}</td>
            <td class="w-24 py-2 pr-2 text-right tabular-nums text-gray-500 dark:text-dark-300">{{ formatUSD(row.cost) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { UserUsageTrendPoint } from '@/types'
import Sparkline from '@/components/charts/Sparkline.vue'
import { formatCompact, formatUSD } from '@/components/charts/chartTheme'
import { avatarToneClass, userInitials } from '@/utils/avatar'

const props = withDefaults(
  defineProps<{
    trend: UserUsageTrendPoint[]
    loading?: boolean
    limit?: number
  }>(),
  {
    loading: false,
    limit: 8
  }
)

const emit = defineEmits<{
  select: [userId: number]
}>()

const { t } = useI18n()

interface Row {
  userId: number
  name: string
  email: string
  tokens: number
  cost: number
  series: number[]
}

const rows = computed<Row[]>(() => {
  const points = props.trend ?? []
  if (!points.length) return []
  const dates = Array.from(new Set(points.map((p) => p.date))).sort()
  // 按 user_id 聚合，避免同名用户被合并
  const byUser = new Map<number, Row & { byDate: Map<string, number> }>()
  for (const p of points) {
    let row = byUser.get(p.user_id)
    if (!row) {
      const name = p.username?.trim() || p.email?.trim() || t('admin.redeem.userPrefix', { id: p.user_id })
      row = { userId: p.user_id, name, email: p.email?.trim() || '', tokens: 0, cost: 0, series: [], byDate: new Map() }
      byUser.set(p.user_id, row)
    }
    row.tokens += Number(p.tokens) || 0
    row.cost += Number(p.actual_cost) || 0
    row.byDate.set(p.date, (row.byDate.get(p.date) ?? 0) + (Number(p.tokens) || 0))
  }
  return Array.from(byUser.values())
    .sort((a, b) => b.tokens - a.tokens)
    .slice(0, props.limit)
    .map(({ byDate, ...row }) => ({ ...row, series: dates.map((d) => byDate.get(d) ?? 0) }))
})

const initials = userInitials
const avatarClass = avatarToneClass
</script>
