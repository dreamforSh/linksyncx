<template>
  <section
    class="overflow-hidden rounded-2xl border border-gray-100 bg-white shadow-card dark:border-dark-700/50 dark:bg-dark-800/50"
    data-testid="plaza-catalog"
  >
    <!-- 筛选:平台 + 模型名搜索(纯前端过滤) -->
    <header class="space-y-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700/60">
      <div class="flex flex-wrap items-center gap-2">
        <button
          v-for="p in ['all', ...platforms]"
          :key="`catalog-platform-${p}`"
          type="button"
          class="inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-sm font-medium transition"
          :class="chipClass(platform === p)"
          :data-testid="`plaza-catalog-platform-${p}`"
          @click="platform = p"
        >
          <PlatformIcon v-if="p !== 'all'" :platform="p as GroupPlatform" size="xs" />
          {{ p === 'all' ? t('modelPlaza.filters.all') : platformLabel(p) }}
        </button>
      </div>
      <div class="relative w-full sm:w-72">
        <Icon
          name="search"
          size="sm"
          class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 dark:text-dark-500"
        />
        <input
          v-model="search"
          type="text"
          :placeholder="t('modelPlaza.filters.searchPlaceholder')"
          class="input rounded-lg py-1.5 pl-9"
          data-testid="plaza-catalog-search"
        />
      </div>
      <p class="flex items-start gap-1.5 text-xs text-gray-500 dark:text-dark-400">
        <Icon name="infoCircle" size="xs" class="mt-px h-3.5 w-3.5 shrink-0" />
        {{ t('modelPlaza.catalog.hint') }}
      </p>
    </header>

    <div v-if="rows.length > 0" class="overflow-x-auto">
      <table class="w-full min-w-[760px] text-sm">
        <thead>
          <tr class="border-b border-gray-100 text-left text-xs font-medium text-gray-500 dark:border-dark-700/60 dark:text-dark-400">
            <th class="px-5 py-2.5">{{ t('modelPlaza.table.model') }}</th>
            <th class="px-3 py-2.5 text-right">{{ t('modelPlaza.table.input') }}</th>
            <th class="px-3 py-2.5 text-right">{{ t('modelPlaza.table.output') }}</th>
            <th class="px-3 py-2.5 text-right">{{ t('modelPlaza.catalog.cacheWrite') }}</th>
            <th class="px-3 py-2.5 text-right">{{ t('modelPlaza.catalog.cacheRead') }}</th>
            <th class="px-5 py-2.5">{{ t('modelPlaza.catalog.availableIn') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100 dark:divide-dark-700/60">
          <tr
            v-for="m in rows"
            :key="`${m.platform}:${m.name}`"
            class="align-top transition-colors hover:bg-gray-50/70 dark:hover:bg-dark-700/30"
            data-testid="plaza-catalog-row"
          >
            <td class="px-5 py-3">
              <div class="font-mono text-[13px] font-medium text-gray-900 dark:text-white">{{ m.name }}</div>
              <div class="mt-1 flex flex-wrap items-center gap-1.5">
                <span class="rounded px-1.5 py-0.5 text-[11px] font-medium" :class="platformBadgeLightClass(m.platform)">
                  {{ platformLabel(m.platform) }}
                </span>
                <span
                  v-if="tierLines(m).length > 0"
                  class="cursor-help rounded bg-amber-50 px-1.5 py-0.5 text-[11px] font-medium text-amber-700 dark:bg-amber-900/20 dark:text-amber-300"
                  :title="tierLines(m).join('\n')"
                  data-testid="plaza-catalog-tiered"
                >
                  {{ t('modelPlaza.catalog.tiered') }}
                </span>
              </div>
            </td>
            <td class="price-cell">{{ perMillion(m.official_pricing?.input_price) }}</td>
            <td class="price-cell">{{ perMillion(m.official_pricing?.output_price) }}</td>
            <td class="price-cell">{{ perMillion(m.official_pricing?.cache_write_price) }}</td>
            <td class="price-cell">{{ perMillion(m.official_pricing?.cache_read_price) }}</td>
            <td class="px-5 py-3">
              <div class="flex flex-wrap gap-1.5">
                <span
                  v-for="g in m.groups"
                  :key="g.id"
                  class="inline-flex items-center gap-1 rounded-md bg-gray-100 px-1.5 py-0.5 text-xs text-gray-700 dark:bg-dark-700 dark:text-dark-200"
                >
                  {{ g.name }}
                  <span class="font-mono text-gray-500 dark:text-dark-400">×{{ effectiveRate(g) }}</span>
                </span>
                <span
                  v-if="m.ungrouped"
                  class="inline-flex items-center gap-1 rounded-md bg-gray-100 px-1.5 py-0.5 text-xs text-gray-700 dark:bg-dark-700 dark:text-dark-200"
                  :title="t('modelPlaza.catalog.ungroupedHint')"
                >
                  {{ t('modelPlaza.catalog.ungrouped') }}
                  <span class="font-mono text-gray-500 dark:text-dark-400">×1</span>
                </span>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-else class="px-5 py-12 text-center text-sm text-gray-500 dark:text-dark-400">
      {{ search.trim() ? t('modelPlaza.noSearchResult') : t('modelPlaza.catalog.empty') }}
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import { formatScaled } from '@/utils/pricing'
import { platformBadgeLightClass, platformLabel } from '@/utils/platformColors'
import type { ModelPlazaCatalogGroupRef, ModelPlazaCatalogModel } from '@/api/modelPlaza'
import type { GroupPlatform } from '@/types'

const props = defineProps<{
  models: ModelPlazaCatalogModel[]
}>()

const { t } = useI18n()

const PER_MILLION = 1_000_000
/** 价格统一保底 2 位小数,更长的有效小数原样保留(与分组价目表一致)。 */
const MIN_DECIMALS = 2

const platform = ref('all')
const search = ref('')

const platforms = computed(() => [...new Set(props.models.map((m) => m.platform))].sort())

/**
 * 平台内排序与分组价目表一致:有官方价的在前,按输出价从高到低;同价按名称降序(新版本号在前)。
 */
const rows = computed(() => {
  const q = search.value.trim().toLowerCase()
  return props.models
    .filter((m) => platform.value === 'all' || m.platform === platform.value)
    .filter((m) => !q || m.name.toLowerCase().includes(q))
    .sort((a, b) => {
      if (a.platform !== b.platform) return a.platform.localeCompare(b.platform)
      const pa = a.official_pricing?.output_price ?? null
      const pb = b.official_pricing?.output_price ?? null
      if (pa != null && pb != null && pa !== pb) return pb - pa
      if (pa != null && pb == null) return -1
      if (pa == null && pb != null) return 1
      return b.name.localeCompare(a.name)
    })
})

function perMillion(value: number | null | undefined): string {
  return value == null ? '-' : formatScaled(value, PER_MILLION, MIN_DECIMALS)
}

function effectiveRate(g: ModelPlazaCatalogGroupRef): number {
  return g.user_rate_multiplier ?? g.rate_multiplier
}

/** 长上下文阶梯的逐档说明(悬浮提示),仅多档模型有。 */
function tierLines(m: ModelPlazaCatalogModel): string[] {
  const intervals = m.official_pricing?.intervals ?? []
  if (intervals.length < 2) return []
  return [...intervals]
    .sort((a, b) => a.min_tokens - b.min_tokens)
    .map((iv) =>
      t('modelPlaza.catalog.tierLine', {
        range: iv.max_tokens == null ? `> ${formatTokens(iv.min_tokens)}` : `≤ ${formatTokens(iv.max_tokens)}`,
        input: perMillion(iv.input_price),
        output: perMillion(iv.output_price)
      })
    )
}

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${+(n / 1_000_000).toFixed(2)}M`
  if (n >= 1_000) return `${+(n / 1_000).toFixed(1)}K`
  return String(n)
}

function chipClass(active: boolean): string {
  return active
    ? 'bg-gradient-to-r from-primary-500 to-primary-600 text-white shadow-sm shadow-primary-500/30'
    : 'bg-white text-gray-600 ring-1 ring-inset ring-gray-200 hover:bg-gray-50 hover:text-gray-900 dark:bg-dark-800/60 dark:text-dark-300 dark:ring-dark-700 dark:hover:bg-dark-800 dark:hover:text-white'
}
</script>

<style scoped>
.price-cell {
  @apply whitespace-nowrap px-3 py-3 text-right font-mono text-[13px] tabular-nums text-gray-700 dark:text-dark-200;
}
</style>
