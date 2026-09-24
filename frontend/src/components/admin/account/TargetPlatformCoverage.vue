<template>
  <div v-if="items.length" class="mt-2 flex flex-wrap gap-2" data-testid="target-platform-coverage">
    <span
      v-for="item in items"
      :key="item.platform"
      class="inline-flex items-center gap-1 rounded-md px-2 py-0.5 text-xs font-medium ring-1 ring-inset"
      :class="item.covered
        ? 'bg-emerald-50 text-emerald-700 ring-emerald-200 dark:bg-emerald-500/10 dark:text-emerald-300 dark:ring-emerald-500/30'
        : 'bg-amber-50 text-amber-700 ring-amber-200 dark:bg-amber-500/10 dark:text-amber-300 dark:ring-amber-500/30'"
      :data-covered="item.covered"
    >
      <PlatformIcon :platform="item.platform" size="xs" />
      {{ platformLabel(item.platform) }} × {{ item.count }}
      <span class="opacity-70">· {{ item.covered ? t('admin.accounts.targetPlatformCovered') : t('admin.accounts.targetPlatformMissing') }}</span>
    </span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import type { GroupPlatform } from '@/types'
import { groupTargetsByPlatform, type TargetGroupLike } from '@/utils/importTargetGroups'
import { platformLabel } from '@/utils/platformColors'

// 多平台导入时逐平台提示「是否已选目标分组」，未覆盖的平台账号不会被创建
const props = defineProps<{
  platformCounts: Record<string, number>
  groupIds: number[]
  groups: TargetGroupLike[]
}>()

const { t } = useI18n()

const items = computed(() => {
  const covered = groupTargetsByPlatform(props.groupIds, props.groups)
  return Object.entries(props.platformCounts).map(([platform, count]) => ({
    platform: platform as GroupPlatform,
    count,
    covered: (covered.get(platform)?.length ?? 0) > 0
  }))
})
</script>
