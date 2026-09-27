<template>
  <div :class="active ? ['min-h-screen bg-gray-50 dark:bg-dark-950', { 'overflow-x-clip': shifting }] : 'contents'">
    <AppSidebar v-if="active" />

    <div
      ref="contentRef"
      :class="active ? ['relative min-h-screen', sidebarCollapsed ? 'lg:ml-[72px]' : 'lg:ml-64'] : 'contents'"
    >
      <AppHeader v-if="active" />

      <div :role="active ? 'main' : undefined" :class="active ? 'p-4 md:p-6 lg:p-8' : 'contents'">
        <slot />
      </div>
    </div>

    <AppOnboardingTour v-if="active" />
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, provide, ref, watch } from 'vue'
import { useAppStore } from '@/stores'
import AppSidebar from './AppSidebar.vue'
import AppHeader from './AppHeader.vue'
import AppOnboardingTour from './AppOnboardingTour'
import {
  APP_SHELL_KEY,
  SIDEBAR_COLLAPSED_WIDTH,
  SIDEBAR_EXPANDED_WIDTH,
  SIDEBAR_TRANSITION_EASING,
  SIDEBAR_TRANSITION_MS
} from './appShellContext'

const props = defineProps<{
  /** 独立使用（外层没有常驻壳）时始终显示侧边栏与顶栏 */
  standalone?: boolean
}>()

const appStore = useAppStore()
const sidebarCollapsed = computed(() => appStore.sidebarCollapsed)

const layoutCount = ref(0)
provide(APP_SHELL_KEY, {
  register: () => {
    layoutCount.value++
  },
  unregister: () => {
    layoutCount.value = Math.max(0, layoutCount.value - 1)
  }
})

// 没有页面登记时，外层节点全部 display: contents，对登录页等独立页面透明；
// 元素类型始终不变，因此登记前后插槽内容不会被重新挂载。
const active = computed(() => props.standalone || layoutCount.value > 0)

// 侧栏折叠：内容区的 margin 直接切到终值（只触发一次布局），再用 transform 从原位置滑过去。
// 相比逐帧过渡 margin，表格/图表不会在动画期间每帧重排、重绘。
const contentRef = ref<HTMLElement | null>(null)
const shifting = ref(false)
const runningShifts = new Set<Animation>()

function shiftContent(offset: number) {
  const el = contentRef.value
  if (!el || typeof el.animate !== 'function') return
  shifting.value = true
  const animation = el.animate(
    [{ transform: `translateX(${offset}px)` }, { transform: 'translateX(0)' }],
    // composite: 'add' 让连续点击时的位移叠加，中途反向也不会跳帧
    { duration: SIDEBAR_TRANSITION_MS, easing: SIDEBAR_TRANSITION_EASING, composite: 'add' }
  )
  runningShifts.add(animation)
  const settle = () => {
    runningShifts.delete(animation)
    if (runningShifts.size === 0) shifting.value = false
  }
  animation.onfinish = settle
  animation.oncancel = settle
}

watch(
  sidebarCollapsed,
  (collapsed) => {
    if (!active.value || typeof window.matchMedia !== 'function') return
    if (!window.matchMedia('(min-width: 1024px)').matches) return
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return
    const delta = SIDEBAR_EXPANDED_WIDTH - SIDEBAR_COLLAPSED_WIDTH
    shiftContent(collapsed ? delta : -delta)
  },
  { flush: 'post' }
)

onBeforeUnmount(() => {
  runningShifts.forEach((animation) => animation.cancel())
})
</script>
