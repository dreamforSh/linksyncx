<template>
  <span
    class="inline-flex items-center gap-1 whitespace-nowrap rounded-md px-2 py-0.5 text-xs font-medium"
    :class="platformBadgeLightClass(platform)"
  >
    <PlatformIcon :platform="iconPlatform" size="xs" />
    {{ platformLabel(platform) }}
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import type { GroupPlatform } from '@/types'
import { platformBadgeLightClass, platformLabel } from '@/utils/platformColors'

const props = defineProps<{ platform: string }>()

// Account platforms arrive as plain strings; PlatformIcon falls back to a generic mark for unknown values.
const iconPlatform = computed(() => props.platform as GroupPlatform)
</script>
